package dashscope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/database"
)

// maxTTSReadMessageBytes 定义 DashScope TTS WebSocket 单帧最大读取字节数（4 MiB），满足 24 kHz PCM 块下发需求。
const maxTTSReadMessageBytes = 4 * 1024 * 1024

// TTSClient 实现基于 DashScope WebSocket 流式协议的语音合成客户端。
type TTSClient struct {
	endpoint       string
	apiKey         string
	model          string
	voice          string
	connectTimeout time.Duration
	queueCapacity  int
	httpClient     *http.Client
}

// NewTTSClient 基于数据库 TTS 配置实体与 Agent 指定音色构造 DashScope TTS 客户端实例。
func NewTTSClient(cfg *database.TTSConfig, voice string, queueCap int) (*TTSClient, error) {
	if cfg == nil {
		return nil, errors.New("tts config cannot be nil")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("dashscope api key is required")
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("dashscope ws endpoint is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("dashscope tts model is required")
	}
	trimmedVoice := strings.TrimSpace(voice)
	if trimmedVoice == "" {
		return nil, errors.New("voice cannot be empty")
	}

	timeout := time.Duration(cfg.ConnectTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	if queueCap <= 0 {
		queueCap = 100
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
		endpoint:       strings.TrimSpace(cfg.Endpoint),
		apiKey:         strings.TrimSpace(cfg.APIKey),
		model:          strings.TrimSpace(cfg.Model),
		voice:          trimmedVoice,
		connectTimeout: timeout,
		queueCapacity:  queueCap,
		httpClient:     httpClient,
	}, nil
}

type ttsRequestHeader struct {
	Action    string `json:"action"`
	TaskId    string `json:"task_id"`
	Streaming string `json:"streaming"`
}

type ttsParameters struct {
	TextType   string `json:"text_type"`
	Voice      string `json:"voice"`
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
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
	Input struct{} `json:"input"`
}

type ttsFinishTaskMessage struct {
	Header  ttsRequestHeader `json:"header"`
	Payload ttsFinishPayload `json:"payload"`
}

type ttsCancelTaskMessage struct {
	Header ttsRequestHeader `json:"header"`
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

// CreateStream 创建并启动一条回答级的流式语音合成会话。
func (c *TTSClient) CreateStream(ctx context.Context) (ai.TTSStream, error) {
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

	taskId := newUUID()
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
			Model:     c.model,
			Parameters: ttsParameters{
				TextType:   "PlainText",
				Voice:      c.voice,
				Format:     "pcm",
				SampleRate: 24000,
			},
		},
	}

	runBytes, err := json.Marshal(runMsg)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "marshal run-task failed")
		return nil, fmt.Errorf("marshal run-task: %w", err)
	}

	if err := conn.Write(dialCtx, websocket.MessageText, runBytes); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "write run-task failed")
		return nil, fmt.Errorf("write run-task: %w", err)
	}

	msgType, firstData, err := conn.Read(dialCtx)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "read task-started failed")
		return nil, fmt.Errorf("read task-started: %w", err)
	}
	if msgType != websocket.MessageText {
		_ = conn.Close(websocket.StatusUnsupportedData, "expected text message for task-started")
		return nil, errors.New("expected text message for task-started")
	}

	var initResp ttsResponseMessage
	if err := json.Unmarshal(firstData, &initResp); err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid task-started response json")
		return nil, fmt.Errorf("unmarshal task-started: %w", err)
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
		_ = conn.Close(websocket.StatusNormalClosure, "task-failed")
		return nil, fmt.Errorf("tts task start failed: [%s] %s", code, msg)
	}

	if event != "task-started" {
		_ = conn.Close(websocket.StatusPolicyViolation, "unexpected initial event: "+event)
		return nil, fmt.Errorf("unexpected initial event: %s", event)
	}

	streamCtx, streamCancel := context.WithCancel(ctx)

	stream := &TTSStream{
		conn:   conn,
		taskId: taskId,
		ctx:    streamCtx,
		cancel: streamCancel,
		pcmCh:  make(chan []byte, c.queueCapacity),
	}

	go stream.readLoop()

	return stream, nil
}

// TTSStream 实现 DashScope 流式语音合成会话。
type TTSStream struct {
	conn   *websocket.Conn
	taskId string

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once

	writeMu sync.Mutex

	mu       sync.RWMutex
	closed   bool
	finished bool
	err      error
	pcmCh    chan []byte
}

func (s *TTSStream) readLoop() {
	defer func() {
		if s.conn != nil {
			_ = s.conn.Close(websocket.StatusNormalClosure, "stream closed")
		}
		close(s.pcmCh)
	}()

	for {
		msgType, data, err := s.conn.Read(s.ctx)
		if err != nil {
			s.recordError(fmt.Errorf("read dashscope tts websocket: %w", err))
			return
		}

		if msgType == websocket.MessageBinary {
			if len(data) == 0 {
				continue
			}

			chunk := make([]byte, len(data))
			copy(chunk, data)

			select {
			case s.pcmCh <- chunk:
			case <-s.ctx.Done():
				return
			}
			continue
		}

		if msgType == websocket.MessageText {
			var resp ttsResponseMessage
			if err := json.Unmarshal(data, &resp); err != nil {
				continue
			}

			event := resp.Header.Event
			if event == "" {
				event = resp.Header.Action
			}

			switch event {
			case "task-finished":
				return

			case "task-failed":
				code := resp.Header.ErrorCode
				if code == "" {
					code = "UNKNOWN_ERROR"
				}
				msg := resp.Header.ErrorMessage
				if msg == "" {
					msg = "tts task failed on server"
				}
				s.recordError(fmt.Errorf("dashscope tts task failed: [%s] %s", code, msg))
				return

			default:
				// 忽略未知 DashScope 事件，保持流正常运转
			}
		}
	}
}

func (s *TTSStream) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		if s.closed {
			s.err = context.Canceled
		} else if s.ctx.Err() != nil {
			s.err = s.ctx.Err()
		} else {
			s.err = err
		}
	}
}

// SendSentence 向合成流中按顺序写入一个待合成的完整句子。
func (s *TTSStream) SendSentence(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}

	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return errors.New("tts stream is closed")
	}
	if s.finished {
		s.mu.RUnlock()
		return errors.New("cannot send sentence to finished tts stream")
	}
	if s.err != nil {
		err := s.err
		s.mu.RUnlock()
		return err
	}
	s.mu.RUnlock()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return errors.New("tts stream is closed")
	}
	if s.finished {
		s.mu.RUnlock()
		return errors.New("cannot send sentence to finished tts stream")
	}
	if s.err != nil {
		err := s.err
		s.mu.RUnlock()
		return err
	}
	taskId := s.taskId
	s.mu.RUnlock()

	msg := ttsContinueTaskMessage{
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

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal continue-task: %w", err)
	}

	if err := s.conn.Write(ctx, websocket.MessageText, msgBytes); err != nil {
		s.recordError(fmt.Errorf("write continue-task: %w", err))
		return fmt.Errorf("write continue-task: %w", err)
	}

	return nil
}

// Finish 通知 DashScope 服务端文本输入已全部结束。
func (s *TTSStream) Finish(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("tts stream is closed")
	}
	if s.finished {
		s.mu.Unlock()
		return nil
	}
	if s.err != nil {
		err := s.err
		s.mu.Unlock()
		return err
	}
	s.finished = true
	taskId := s.taskId
	s.mu.Unlock()

	finishMsg := ttsFinishTaskMessage{
		Header: ttsRequestHeader{
			Action:    "finish-task",
			TaskId:    taskId,
			Streaming: "duplex",
		},
	}
	finishBytes, err := json.Marshal(finishMsg)
	if err != nil {
		return fmt.Errorf("marshal finish-task: %w", err)
	}

	if err := s.conn.Write(ctx, websocket.MessageText, finishBytes); err != nil {
		s.recordError(fmt.Errorf("write finish-task: %w", err))
		return fmt.Errorf("write finish-task: %w", err)
	}

	return nil
}

// NextPCM 接收下一个合成的 PCM 音频块（24000 Hz、16-bit、单声道有符号小端）。
// 当所有音频块接收完毕且任务正常完成时返回 nil, io.EOF。
// 当流发生错误、超时或被取消时返回 nil, err。
func (s *TTSStream) NextPCM(ctx context.Context) ([]byte, error) {
	select {
	case chunk, ok := <-s.pcmCh:
		if !ok {
			s.mu.RLock()
			err := s.err
			closed := s.closed
			s.mu.RUnlock()
			if closed {
				return nil, errors.New("tts stream is closed")
			}
			if err != nil {
				return nil, err
			}
			if s.ctx.Err() != nil {
				return nil, s.ctx.Err()
			}
			return nil, io.EOF
		}
		return chunk, nil

	case <-ctx.Done():
		return nil, ctx.Err()

	case <-s.ctx.Done():
		select {
		case chunk, ok := <-s.pcmCh:
			if ok {
				return chunk, nil
			}
		default:
		}

		s.mu.RLock()
		err := s.err
		closed := s.closed
		s.mu.RUnlock()
		if closed {
			return nil, errors.New("tts stream is closed")
		}
		if err != nil {
			return nil, err
		}
		return nil, s.ctx.Err()
	}
}

// Cancel 尝试向 DashScope 服务端发送 cancel-task 结束指令并安全关闭会话。
func (s *TTSStream) Cancel(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	if s.closed || s.finished {
		s.mu.Unlock()
		return s.Close()
	}
	taskId := s.taskId
	s.mu.Unlock()

	cancelMsg := ttsCancelTaskMessage{
		Header: ttsRequestHeader{
			Action:    "cancel-task",
			TaskId:    taskId,
			Streaming: "duplex",
		},
	}
	if cancelBytes, err := json.Marshal(cancelMsg); err == nil {
		_ = s.conn.Write(ctx, websocket.MessageText, cancelBytes)
	}

	return s.Close()
}

// Close 关闭并释放流式合成会话的所有网络与内存资源。
func (s *TTSStream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()

		s.cancel()

		if s.conn != nil {
			err = s.conn.Close(websocket.StatusNormalClosure, "stream closed")
		}
	})
	return err
}
