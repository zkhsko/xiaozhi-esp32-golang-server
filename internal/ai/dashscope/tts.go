package dashscope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
)

// maxTTSReadMessageBytes 定义 DashScope TTS WebSocket 单帧最大读取字节数（4 MiB）。
const maxTTSReadMessageBytes = 4 * 1024 * 1024

// TTSClient 实现基于 DashScope WebSocket 流式协议的语音合成客户端。
type TTSClient struct {
	endpoint       string
	apiKey         string
	model          string
	voice          string
	connectTimeout time.Duration
	httpClient     *http.Client
}

// NewTTSClient 基于领域配置构造 DashScope TTS 客户端实例。
func NewTTSClient(opts ai.TTSOptions) (*TTSClient, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, errors.New("dashscope api key is required")
	}
	if strings.TrimSpace(opts.Endpoint) == "" {
		return nil, errors.New("dashscope ws endpoint is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, errors.New("dashscope tts model is required")
	}
	trimmedVoice := strings.TrimSpace(opts.Voice)
	if trimmedVoice == "" {
		return nil, errors.New("voice cannot be empty")
	}

	timeout := opts.ConnectTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	var httpClient *http.Client
	if strings.TrimSpace(opts.ProxyURL) != "" {
		proxyURL, err := url.Parse(strings.TrimSpace(opts.ProxyURL))
		if err != nil {
			return nil, fmt.Errorf("parse proxy url: %w", err)
		}
		httpClient = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}
	}

	return &TTSClient{
		endpoint:       strings.TrimSpace(opts.Endpoint),
		apiKey:         strings.TrimSpace(opts.APIKey),
		model:          strings.TrimSpace(opts.Model),
		voice:          trimmedVoice,
		connectTimeout: timeout,
		httpClient:     httpClient,
	}, nil
}

type ttsRequestHeader struct {
	Action    string `json:"action"`
	TaskId    string `json:"task_id"`
	Streaming string `json:"streaming,omitempty"`
}

type ttsClientMessage struct {
	Header  ttsRequestHeader `json:"header"`
	Payload any              `json:"payload,omitempty"`
}

type ttsResponseMessage struct {
	Header struct {
		Action       string `json:"action"`
		TaskId       string `json:"task_id"`
		Event        string `json:"event"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	} `json:"header"`
	Payload struct {
		Output struct {
			Text string `json:"text,omitempty"`
		} `json:"output"`
	} `json:"payload"`
}

// CreateSession 为单轮问答创建并建立一条 DashScope WebSocket 长连接会话。
func (c *TTSClient) CreateSession(ctx context.Context) (ai.TTSSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, c.connectTimeout)
	defer dialCancel()

	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + c.apiKey},
		},
		HTTPClient: c.httpClient,
	}

	conn, _, err := websocket.Dial(dialCtx, c.endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("dial dashscope tts websocket: %w", err)
	}
	conn.SetReadLimit(maxTTSReadMessageBytes)

	sessCtx, sessCancel := context.WithCancel(ctx)

	return &TTSSession{
		conn:   conn,
		model:  c.model,
		voice:  c.voice,
		ctx:    sessCtx,
		cancel: sessCancel,
	}, nil
}

// TTSSession 实现基于 DashScope WebSocket 单长连接的多句复用语音合成会话。
type TTSSession struct {
	conn   *websocket.Conn
	model  string
	voice  string

	ctx    context.Context
	cancel context.CancelFunc

	writeMu sync.Mutex
	mu      sync.Mutex
	closed  bool
}

func (s *TTSSession) writeJSON(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.conn == nil {
		return errors.New("tts websocket connection is nil")
	}
	return s.conn.Write(ctx, websocket.MessageText, data)
}

func (s *TTSSession) readTaskStarted(ctx context.Context, expectedTaskId string) error {
	for {
		msgType, firstData, err := s.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read task-started: %w", err)
		}
		if msgType != websocket.MessageText {
			continue
		}

		var initResp ttsResponseMessage
		if err := json.Unmarshal(firstData, &initResp); err != nil {
			return fmt.Errorf("unmarshal task-started: %w", err)
		}

		if initResp.Header.TaskId != "" && initResp.Header.TaskId != expectedTaskId {
			// 忽略非当前任务的消息
			continue
		}

		event := initResp.Header.Event
		if event == "" {
			event = initResp.Header.Action
		}

		if event == "task-failed" {
			code := initResp.Header.ErrorCode
			if code == "" {
				code = "UNKNOWN_ERROR"
			}
			msg := initResp.Header.ErrorMessage
			if msg == "" {
				msg = "task start failed"
			}
			return fmt.Errorf("tts task start failed: [%s] %s", code, msg)
		}

		if event == "task-started" {
			return nil
		}
	}
}

// Synthesize 在当前 WebSocket 会话连接上发起单句流式语音合成，同步阻塞直至该句音频全部输出。
func (s *TTSSession) Synthesize(ctx context.Context, text string, pcm chan<- ai.PCMChunk) error {
	if ctx == nil {
		ctx = context.Background()
	}

	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("tts session is closed")
	}
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return s.ctx.Err()
	}
	s.mu.Unlock()

	// 保证同一 session 串行合成
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	taskId := newUuid()
	runMsg := ttsClientMessage{
		Header: ttsRequestHeader{
			Action:    "run-task",
			TaskId:    taskId,
			Streaming: "duplex",
		},
		Payload: map[string]any{
			"task_group": "audio",
			"task":       "tts",
			"function":   "SpeechSynthesizer",
			"model":      s.model,
			"parameters": map[string]any{
				"text_type":   "PlainText",
				"voice":       s.voice,
				"format":      "pcm",
				"sample_rate": 24000,
			},
			"input": map[string]any{},
		},
	}

	runData, err := json.Marshal(runMsg)
	if err != nil {
		return fmt.Errorf("marshal run-task: %w", err)
	}
	if err := s.conn.Write(ctx, websocket.MessageText, runData); err != nil {
		return fmt.Errorf("write run-task: %w", err)
	}

	if err := s.readTaskStarted(ctx, taskId); err != nil {
		return err
	}

	continueMsg := ttsClientMessage{
		Header: ttsRequestHeader{
			Action:    "continue-task",
			TaskId:    taskId,
			Streaming: "duplex",
		},
		Payload: map[string]any{
			"input": map[string]string{
				"text": trimmedText,
			},
		},
	}
	continueData, err := json.Marshal(continueMsg)
	if err != nil {
		return fmt.Errorf("marshal continue-task: %w", err)
	}
	if err := s.conn.Write(ctx, websocket.MessageText, continueData); err != nil {
		return fmt.Errorf("write continue-task: %w", err)
	}

	finishMsg := ttsClientMessage{
		Header: ttsRequestHeader{
			Action:    "finish-task",
			TaskId:    taskId,
			Streaming: "duplex",
		},
		Payload: map[string]any{
			"input": map[string]any{},
		},
	}
	finishData, err := json.Marshal(finishMsg)
	if err != nil {
		return fmt.Errorf("marshal finish-task: %w", err)
	}
	if err := s.conn.Write(ctx, websocket.MessageText, finishData); err != nil {
		return fmt.Errorf("write finish-task: %w", err)
	}

	var (
		totalPCMBytes int
		firstChunk    = true
	)

	for {
		msgType, data, err := s.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read dashscope tts websocket: %w", err)
		}

		if msgType == websocket.MessageBinary {
			if len(data) == 0 {
				continue
			}
			totalPCMBytes += len(data)

			chunk := ai.PCMChunk{
				Data: data,
			}
			if firstChunk {
				chunk.SentenceStart = trimmedText
				firstChunk = false
			}

			select {
			case pcm <- chunk:
			case <-ctx.Done():
				return ctx.Err()
			case <-s.ctx.Done():
				return s.ctx.Err()
			}
			continue
		}

		if msgType == websocket.MessageText {
			var resp ttsResponseMessage
			if err := json.Unmarshal(data, &resp); err != nil {
				continue
			}

			if resp.Header.TaskId != "" && resp.Header.TaskId != taskId {
				continue
			}

			event := resp.Header.Event
			if event == "" {
				event = resp.Header.Action
			}

			switch event {
			case "task-finished":
				if totalPCMBytes == 0 {
					return errors.New("non-empty sentence yielded zero pcm")
				}
				return nil

			case "task-failed":
				code := resp.Header.ErrorCode
				if code == "" {
					code = "UNKNOWN_ERROR"
				}
				msg := resp.Header.ErrorMessage
				if msg == "" {
					msg = "tts task failed on server"
				}
				return fmt.Errorf("dashscope tts task failed: [%s] %s", code, msg)

			default:
				// 忽略未知事件
			}
		}
	}
}

// Close 关闭底层 WebSocket 连接并释放会话所持有的全部资源。
func (s *TTSSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	s.cancel()

	var err error
	if s.conn != nil {
		err = s.conn.Close(websocket.StatusNormalClosure, "session closed")
	}
	return err
}
