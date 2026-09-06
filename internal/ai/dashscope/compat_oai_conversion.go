package dashscope

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	genkitai "github.com/firebase/genkit/go/ai"

	"xiaozhi-esp32-golang-server/internal/ai"
)

func toGenkitMessages(messages []ai.Message) ([]*genkitai.Message, error) {
	converted := make([]*genkitai.Message, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case ai.RoleSystem:
			converted = append(converted, genkitai.NewSystemTextMessage(message.Content))
		case ai.RoleUser:
			converted = append(converted, genkitai.NewUserTextMessage(message.Content))
		case ai.RoleAssistant:
			converted = append(converted, toGenkitAssistantMessage(message))
		case ai.RoleTool:
			converted = append(converted, toGenkitToolMessage(message))
		default:
			return nil, fmt.Errorf("unsupported llm message role %q", message.Role)
		}
	}
	return converted, nil
}

func toGenkitAssistantMessage(message ai.Message) *genkitai.Message {
	if len(message.ToolCalls) == 0 {
		return genkitai.NewModelTextMessage(message.Content)
	}

	parts := make([]*genkitai.Part, 0, len(message.ToolCalls)+1)
	if message.Content != "" {
		parts = append(parts, genkitai.NewTextPart(message.Content))
	}
	for _, toolCall := range message.ToolCalls {
		input := toolCall.Arguments
		if rawInput, ok := input.(string); ok && strings.TrimSpace(rawInput) != "" {
			var parsed map[string]any
			if json.Unmarshal([]byte(rawInput), &parsed) == nil {
				input = parsed
			}
		}
		parts = append(parts, genkitai.NewToolRequestPart(&genkitai.ToolRequest{
			Name:  toolCall.Name,
			Ref:   toolCall.Id,
			Input: input,
		}))
	}
	return genkitai.NewModelMessage(parts...)
}

func toGenkitToolMessage(message ai.Message) *genkitai.Message {
	var output any = message.Content
	if message.Content != "" {
		var parsed any
		if json.Unmarshal([]byte(message.Content), &parsed) == nil {
			output = parsed
		}
	}
	part := genkitai.NewToolResponsePart(&genkitai.ToolResponse{
		Name:   message.ToolName,
		Ref:    message.ToolCallId,
		Output: output,
	})
	return genkitai.NewMessage(genkitai.RoleTool, nil, part)
}

func toGenkitTools(tools []ai.Tool) ([]genkitai.ToolRef, error) {
	converted := make([]genkitai.ToolRef, 0, len(tools))
	for _, tool := range tools {
		tool := tool
		tool.Name = strings.TrimSpace(tool.Name)
		if tool.Name == "" {
			return nil, errors.New("llm tool name is required")
		}
		if tool.Run == nil {
			return nil, fmt.Errorf("tool %s has no run handler", tool.Name)
		}

		toolOptions := make([]genkitai.ToolOption, 0, 1)
		if len(tool.Parameters) > 0 {
			toolOptions = append(toolOptions, genkitai.WithInputSchema(tool.Parameters))
		}
		converted = append(converted, genkitai.NewTool(
			tool.Name,
			tool.Description,
			func(ctx *genkitai.ToolContext, input any) (any, error) {
				return tool.Run(ctx.Context, input)
			},
			toolOptions...,
		))
	}
	return converted, nil
}

func toLLMResult(response *genkitai.ModelResponse, inputMessageCount int) (ai.LLMResult, error) {
	if response == nil {
		return ai.LLMResult{}, errors.New("genkit response is nil")
	}

	var messages []ai.Message
	history := response.History()
	if len(history) > inputMessageCount {
		for _, message := range history[inputMessageCount:] {
			converted, err := fromGenkitMessage(message)
			if err != nil {
				return ai.LLMResult{}, err
			}
			messages = append(messages, converted...)
		}
	}
	if len(messages) == 0 && response.Text() != "" {
		messages = []ai.Message{{Role: ai.RoleAssistant, Content: response.Text()}}
	}

	return ai.LLMResult{
		FinalText: response.Text(),
		Messages:  messages,
	}, nil
}

func fromGenkitMessage(message *genkitai.Message) ([]ai.Message, error) {
	if message == nil {
		return nil, errors.New("genkit history contains nil message")
	}

	switch message.Role {
	case genkitai.RoleSystem:
		return []ai.Message{{Role: ai.RoleSystem, Content: message.Text()}}, nil
	case genkitai.RoleUser:
		return []ai.Message{{Role: ai.RoleUser, Content: message.Text()}}, nil
	case genkitai.RoleModel:
		return []ai.Message{fromGenkitModelMessage(message)}, nil
	case genkitai.RoleTool:
		return fromGenkitToolMessage(message), nil
	default:
		return nil, fmt.Errorf("unsupported genkit message role %q", message.Role)
	}
}

func fromGenkitModelMessage(message *genkitai.Message) ai.Message {
	var textParts []string
	var toolCalls []ai.ToolCall
	for _, part := range message.Content {
		if part == nil {
			continue
		}
		if part.IsText() && part.Text != "" {
			textParts = append(textParts, part.Text)
		} else if part.IsToolRequest() && part.ToolRequest != nil {
			toolCalls = append(toolCalls, ai.ToolCall{
				Id:        part.ToolRequest.Ref,
				Name:      part.ToolRequest.Name,
				Arguments: part.ToolRequest.Input,
			})
		}
	}
	return ai.Message{
		Role:      ai.RoleAssistant,
		Content:   strings.Join(textParts, ""),
		ToolCalls: toolCalls,
	}
}

func fromGenkitToolMessage(message *genkitai.Message) []ai.Message {
	converted := make([]ai.Message, 0, len(message.Content))
	for _, part := range message.Content {
		if part == nil || !part.IsToolResponse() || part.ToolResponse == nil {
			continue
		}
		converted = append(converted, ai.Message{
			Role:       ai.RoleTool,
			Content:    formatToolOutput(part.ToolResponse.Output),
			ToolCallId: part.ToolResponse.Ref,
			ToolName:   part.ToolResponse.Name,
		})
	}
	if len(converted) == 0 {
		converted = append(converted, ai.Message{Role: ai.RoleTool, Content: message.Text()})
	}
	return converted
}

func formatToolOutput(output any) string {
	if text, ok := output.(string); ok {
		return text
	}
	if output == nil {
		return ""
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return fmt.Sprint(output)
	}
	return string(encoded)
}
