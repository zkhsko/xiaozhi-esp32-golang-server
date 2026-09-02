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
	"xiaozhi-esp32-golang-server/internal/database"
)

// TargetTTSModel 定义 DashScope TTS 目标模型名称（唯一支持模型）。
const TargetTTSModel = "qwen-audio-3.0-tts-flash"

// maxTTSReadMessageBytes 定义 DashScope TTS WebSocket 单帧最大读取字节数（1 MiB）。
const maxTTSReadMessageBytes = 1 * 1024 * 1024

// ErrConcurrentSynthesize 表示同一 Stream 上存在并发的 SynthesizeSentence 调用或当前状态非空闲。
var ErrConcurrentSynthesize = errors.New("concurrent SynthesizeSentence calls are not allowed")

// ErrStreamClosed 表示流式语音合成会话已经关闭。
var ErrStreamClosed = errors.New("tts stream is closed")

// TTSClient 实现基于 DashScope WebSocket 流式协议的语音合成客户端。
type TTSClient struct {
	endpoint          string
	apiKey            string
	model             string
	voice             string
	connectTimeout    time.Duration
	firstAudioTimeout time.Duration
	sentenceTimeout   time.Duration
	httpClient        *http.Client
}

// 确保 TTSClient 实现了 ai.TTSClient 接口。
var _ ai.TTSClient = (*TTSClient)(nil)

// NewTTSClient 基于数据库 TTS 配置和指定音色构造 DashScope TTS 客户端实例。
func NewTTSClient(cfg *database.TTSConfig, voice string) (*TTSClient, error) {
	if cfg == nil {
		return nil, errors.New("tts config cannot be nil")
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, errors.New("dashscope ws endpoint is required")
	}

	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, errors.New("dashscope api key is required")
	}

	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, errors.New("dashscope tts model is required")
	}
	if model != TargetTTSModel {
		return nil, fmt.Errorf("unsupported dashscope tts model: %s (only %s is supported)", model, TargetTTSModel)
	}

	v := strings.TrimSpace(voice)
	if v == "" {
		return nil, errors.New("tts voice is required")
	}

	connectTimeout := time.Duration(cfg.ConnectTimeoutMS) * time.Millisecond
	if connectTimeout <= 0 {
		connectTimeout = 5 * time.Second
	}
	firstAudioTimeout := time.Duration(cfg.FirstAudioTimeoutMS) * time.Millisecond
	if firstAudioTimeout <= 0 {
		firstAudioTimeout = 5 * time.Second
	}
	sentenceTimeout := time.Duration(cfg.SentenceTimeoutMS) * time.Millisecond
	if sentenceTimeout <= 0 {
		sentenceTimeout = 10 * time.Second
	}

	var httpClient *http.Client
	if strings.TrimSpace(cfg.ProxyURL) != "" {
		proxyURL, err := url.Parse(strings.TrimSpace(cfg.ProxyURL))
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
		endpoint:          endpoint,
		apiKey:            apiKey,
		model:             model,
		voice:             v,
		connectTimeout:    connectTimeout,
		firstAudioTimeout: firstAudioTimeout,
		sentenceTimeout:   sentenceTimeout,
		httpClient:        httpClient,
	}, nil
}

// CreateStream 创建一条回答级的流式语音合成会话并建立 WebSocket 连接。
func (c *TTSClient) CreateStream(ctx context.Context) (ai.TTSStream, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, c.connectTimeout)
	defer dialCancel()

	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":              []string{"Bearer " + c.apiKey},
			"X-DashScope-DataInspection": []string{"enable"},
		},
		HTTPClient: c.httpClient,
	}

	conn, _, err := websocket.Dial(dialCtx, c.endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("dial dashscope tts websocket: %w", err)
	}
	conn.SetReadLimit(maxTTSReadMessageBytes)

	return &TTSStream{
		client: c,
		conn:   conn,
		state:  stateIdle,
	}, nil
}

type ttsRequestHeader struct {
	Action    string `json:"action"`
	TaskId    string `json:"task_id"`
	Streaming string `json:"streaming"`
}

type ttsParameters struct {
	TextType   string  `json:"text_type"`
	Voice      string  `json:"voice"`
	Format     string  `json:"format"`
	SampleRate int     `json:"sample_rate"`
	Volume     int     `json:"volume"`
	Rate       float64 `json:"rate"`
	Pitch      float64 `json:"pitch"`
	EnableSSML bool    `json:"enable_ssml"`
}

type ttsRunPayload struct {
	TaskGroup  string        `json:"task_group"`
	Task       string        `json:"task"`
	Function   string        `json:"function"`
	Model      string        `json:"model"`
	Parameters ttsParameters `json:"parameters"`
	Input      struct{}      `json:"input"`
}

type ttsRunTaskMessage struct {
	Header  ttsRequestHeader `json:"header"`
	Payload ttsRunPayload    `json:"payload"`
}

type ttsContinuePayload struct {
	Input struct {
		Text string `json:"text"`
	} `json:"input"`
}

type ttsContinueTaskMessage struct {
	Header  ttsRequestHeader   `json:"header"`
	Payload ttsContinuePayload `json:"payload"`
}

type ttsFinishPayload struct {
	Input struct {
		Directive string `json:"directive,omitempty"`
	} `json:"input"`
}

type ttsFinishTaskMessage struct {
	Header  ttsRequestHeader `json:"header"`
	Payload ttsFinishPayload `json:"payload"`
}

type ttsResponseMessage struct {
	Header struct {
		Action       string `json:"action"`
		TaskId       string `json:"task_id"`
		Event        string `json:"event"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	} `json:"header"`
	Payload json.RawMessage `json:"payload"`
}

type streamState int

const (
	stateIdle streamState = iota
	stateWaitingTaskStarted
	stateReceivingAudio
	stateCancelling
	stateFailed
	stateClosed
)

// TTSStream 实现基于 DashScope WebSocket 流式协议的单轮流式语音合成会话。
type TTSStream struct {
	client       *TTSClient
	conn         *websocket.Conn
	mu           sync.Mutex
	writeMu      sync.Mutex
	state        streamState
	activeTaskId string
	err          error
	closeOnce    sync.Once
}

// 确保 TTSStream 实现了 ai.TTSStream 接口。
var _ ai.TTSStream = (*TTSStream)(nil)

// SynthesizeSentence 同步合成单句，并通过 onPCM 回调交付 PCM 数据。
func (s *TTSStream) SynthesizeSentence(
	ctx context.Context,
	text string,
	onPCM func(context.Context, []byte) error,
) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("synthesis text cannot be empty")
	}
	if onPCM == nil {
		return errors.New("onPCM callback cannot be nil")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.state == stateClosed {
		s.mu.Unlock()
		return ErrStreamClosed
	}
	if s.state == stateFailed {
		err := s.err
		s.mu.Unlock()
		return fmt.Errorf("tts stream has failed: %w", err)
	}
	if s.state != stateIdle {
		s.mu.Unlock()
		return ErrConcurrentSynthesize
	}

	taskId := newUUID()
	s.activeTaskId = taskId
	s.state = stateWaitingTaskStarted
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.state != stateClosed && s.state != stateFailed {
			s.state = stateIdle
		}
		s.activeTaskId = ""
		s.mu.Unlock()
	}()

	// 1. 发送 run-task 消息
	runMsg := ttsRunTaskMessage{
		Header: ttsRequestHeader{
			Action:    "run-task",
			TaskId:    taskId,
			Streaming: "duplex",
		},
		Payload: ttsRunPayload{
			TaskGroup: "audio",
			Task:      "tts",
			Function:  "SpeechSynthesizer",
			Model:     s.client.model,
			Parameters: ttsParameters{
				TextType:   "PlainText",
				Voice:      s.client.voice,
				Format:     "pcm",
				SampleRate: 24000,
				Volume:     50,
				Rate:       1.0,
				Pitch:      1.0,
				EnableSSML: false,
			},
			Input: struct{}{},
		},
	}

	runBytes, err := json.Marshal(runMsg)
	if err != nil {
		s.markFailed(err)
		return fmt.Errorf("marshal run-task: %w", err)
	}

	s.writeMu.Lock()
	err = s.conn.Write(ctx, websocket.MessageText, runBytes)
	s.writeMu.Unlock()
	if err != nil {
		s.markFailed(err)
		return fmt.Errorf("write run-task: %w", err)
	}

	// 2. 等待 task-started 响应
	msgType, firstData, err := s.conn.Read(ctx)
	if err != nil {
		s.markFailed(err)
		return fmt.Errorf("read task-started: %w", err)
	}
	if msgType != websocket.MessageText {
		err := errors.New("expected text message for task-started")
		s.markFailed(err)
		return err
	}

	var initResp ttsResponseMessage
	if err := json.Unmarshal(firstData, &initResp); err != nil {
		s.markFailed(err)
		return fmt.Errorf("unmarshal task-started: %w", err)
	}

	if initResp.Header.TaskId != taskId {
		err := fmt.Errorf("task_id mismatch in task-started: got %s, want %s", initResp.Header.TaskId, taskId)
		s.markFailed(err)
		return err
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
		err := fmt.Errorf("tts task failed: [%s] %s (task_id: %s)", code, msg, taskId)
		s.markFailed(err)
		return err
	}

	if event != "task-started" {
		err := fmt.Errorf("unexpected event waiting for task-started: %s", event)
		s.markFailed(err)
		return err
	}

	s.mu.Lock()
	if s.state != stateClosed && s.state != stateFailed {
		s.state = stateReceivingAudio
	}
	s.mu.Unlock()

	// 3. 发送 continue-task (携带待朗读文本) 与 finish-task
	continueMsg := ttsContinueTaskMessage{
		Header: ttsRequestHeader{
			Action:    "continue-task",
			TaskId:    taskId,
			Streaming: "duplex",
		},
		Payload: ttsContinuePayload{
			Input: struct {
				Text string `json:"text"`
			}{
				Text: text,
			},
		},
	}

	continueBytes, err := json.Marshal(continueMsg)
	if err != nil {
		s.markFailed(err)
		return fmt.Errorf("marshal continue-task: %w", err)
	}

	finishMsg := ttsFinishTaskMessage{
		Header: ttsRequestHeader{
			Action:    "finish-task",
			TaskId:    taskId,
			Streaming: "duplex",
		},
		Payload: ttsFinishPayload{},
	}

	finishBytes, err := json.Marshal(finishMsg)
	if err != nil {
		s.markFailed(err)
		return fmt.Errorf("marshal finish-task: %w", err)
	}

	s.writeMu.Lock()
	err = s.conn.Write(ctx, websocket.MessageText, continueBytes)
	if err == nil {
		err = s.conn.Write(ctx, websocket.MessageText, finishBytes)
	}
	s.writeMu.Unlock()
	if err != nil {
		s.markFailed(err)
		return fmt.Errorf("write continue/finish task: %w", err)
	}

	// 4. 读取 PCM 二进制数据及事件响应，直至 task-finished
	for {
		msgType, data, err := s.conn.Read(ctx)
		if err != nil {
			s.markFailed(err)
			return fmt.Errorf("read tts message: %w", err)
		}

		if msgType == websocket.MessageBinary {
			if err := onPCM(ctx, data); err != nil {
				s.markFailed(err)
				return fmt.Errorf("onPCM callback: %w", err)
			}
			continue
		}

		if msgType == websocket.MessageText {
			var resp ttsResponseMessage
			if err := json.Unmarshal(data, &resp); err != nil {
				s.markFailed(err)
				return fmt.Errorf("unmarshal tts response: %w", err)
			}

			if resp.Header.TaskId != taskId {
				err := fmt.Errorf("task_id mismatch: got %s, want %s", resp.Header.TaskId, taskId)
				s.markFailed(err)
				return err
			}

			event := resp.Header.Event
			if event == "" {
				event = resp.Header.Action
			}

			switch event {
			case "result-generated":
				// 合法中间事件，忽略并继续
				continue
			case "task-finished":
				return nil
			case "task-failed":
				code := resp.Header.ErrorCode
				if code == "" {
					code = "UNKNOWN_ERROR"
				}
				msg := resp.Header.ErrorMessage
				if msg == "" {
					msg = "task failed"
				}
				err := fmt.Errorf("tts task failed: [%s] %s (task_id: %s)", code, msg, taskId)
				s.markFailed(err)
				return err
			default:
				err := fmt.Errorf("unknown tts event: %s", event)
				s.markFailed(err)
				return err
			}
		}

		err = fmt.Errorf("unexpected websocket message type: %v", msgType)
		s.markFailed(err)
		return err
	}
}

func (s *TTSStream) markFailed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateClosed {
		s.state = stateFailed
		s.err = err
	}
}

// Cancel 取消当前活跃单句任务。无活跃任务时幂等返回。不启动第二读取者。
func (s *TTSStream) Cancel(ctx context.Context) error {
	s.mu.Lock()
	if s.state == stateClosed || s.state == stateFailed || s.state == stateIdle {
		s.mu.Unlock()
		return nil
	}
	taskId := s.activeTaskId
	if taskId == "" {
		s.mu.Unlock()
		return nil
	}
	s.state = stateCancelling
	s.mu.Unlock()

	cancelMsg := ttsFinishTaskMessage{
		Header: ttsRequestHeader{
			Action:    "finish-task",
			TaskId:    taskId,
			Streaming: "duplex",
		},
		Payload: ttsFinishPayload{
			Input: struct {
				Directive string `json:"directive,omitempty"`
			}{
				Directive: "cancel",
			},
		},
	}
	msgBytes, err := json.Marshal(cancelMsg)
	if err != nil {
		return fmt.Errorf("marshal cancel message: %w", err)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.conn == nil {
		return nil
	}
	if err := s.conn.Write(ctx, websocket.MessageText, msgBytes); err != nil {
		return fmt.Errorf("write cancel task: %w", err)
	}
	return nil
}

// Close 幂等关闭流式语音合成会话并释放底层 WebSocket 连接。
func (s *TTSStream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.state = stateClosed
		conn := s.conn
		s.mu.Unlock()

		if conn != nil {
			err = conn.Close(websocket.StatusNormalClosure, "stream closed")
		}
	})
	return err
}
