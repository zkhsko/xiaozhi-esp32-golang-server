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
	"xiaozhi-esp32-golang-server/internal/audio"
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

	queueCap := opts.QueueCapacity
	if queueCap <= 0 {
		queueCap = 100
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

// SynthesizeSentence 发起单句流式语音合成，建立底层会话并流式产出 Opus 编码音频包。
func (c *TTSClient) SynthesizeSentence(ctx context.Context, text string) (ai.TTSPacketStream, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if text == "" {
		return newEmptyPacketStream(), nil
	}

	encoder, err := audio.NewEncoder(audio.DefaultMaxOpusPacketBytes)
	if err != nil {
		return nil, fmt.Errorf("create opus encoder: %w", err)
	}
	streamEncoder := audio.NewStreamEncoder(encoder)

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
		_ = encoder.Close()
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
		_ = encoder.Close()
		return nil, fmt.Errorf("marshal run-task: %w", err)
	}

	if err := conn.Write(dialCtx, websocket.MessageText, runBytes); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "write run-task failed")
		_ = encoder.Close()
		return nil, fmt.Errorf("write run-task: %w", err)
	}

	msgType, firstData, err := conn.Read(dialCtx)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "read task-started failed")
		_ = encoder.Close()
		return nil, fmt.Errorf("read task-started: %w", err)
	}
	if msgType != websocket.MessageText {
		_ = conn.Close(websocket.StatusUnsupportedData, "expected text message for task-started")
		_ = encoder.Close()
		return nil, errors.New("expected text message for task-started")
	}

	var initResp ttsResponseMessage
	if err := json.Unmarshal(firstData, &initResp); err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid task-started response json")
		_ = encoder.Close()
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
		_ = encoder.Close()
		return nil, fmt.Errorf("tts task start failed: [%s] %s", code, msg)
	}

	if event != "task-started" {
		_ = conn.Close(websocket.StatusPolicyViolation, "unexpected initial event: "+event)
		_ = encoder.Close()
		return nil, fmt.Errorf("unexpected initial event: %s", event)
	}

	// 发送单句文本
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
		_ = conn.Close(websocket.StatusInternalError, "marshal continue-task failed")
		_ = encoder.Close()
		return nil, fmt.Errorf("marshal continue-task: %w", err)
	}
	if err := conn.Write(dialCtx, websocket.MessageText, continueBytes); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "write continue-task failed")
		_ = encoder.Close()
		return nil, fmt.Errorf("write continue-task: %w", err)
	}

	// 告知输入结束
	finishMsg := ttsFinishTaskMessage{
		Header: ttsRequestHeader{
			Action:    "finish-task",
			TaskId:    taskId,
			Streaming: "duplex",
		},
	}
	finishBytes, err := json.Marshal(finishMsg)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "marshal finish-task failed")
		_ = encoder.Close()
		return nil, fmt.Errorf("marshal finish-task: %w", err)
	}
	if err := conn.Write(dialCtx, websocket.MessageText, finishBytes); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "write finish-task failed")
		_ = encoder.Close()
		return nil, fmt.Errorf("write finish-task: %w", err)
	}

	streamCtx, streamCancel := context.WithCancel(ctx)

	stream := &TTSPacketStream{
		conn:          conn,
		taskId:        taskId,
		encoder:       encoder,
		streamEncoder: streamEncoder,
		ctx:           streamCtx,
		cancel:        streamCancel,
		packetCh:      make(chan []byte, c.queueCapacity),
	}

	go stream.readLoop()

	return stream, nil
}

// TTSPacketStream 实现单句 DashScope Opus 音频包合成流。
type TTSPacketStream struct {
	conn          *websocket.Conn
	taskId        string
	encoder       *audio.Encoder
	streamEncoder *audio.StreamEncoder

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	writeMu   sync.Mutex

	mu       sync.RWMutex
	closed   bool
	err      error
	packetCh chan []byte
}

func (s *TTSPacketStream) readLoop() {
	defer func() {
		if s.conn != nil {
			_ = s.conn.Close(websocket.StatusNormalClosure, "stream closed")
		}
		if s.encoder != nil {
			_ = s.encoder.Close()
		}
		close(s.packetCh)
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

			pkts, encErr := s.streamEncoder.Feed(data)
			if encErr != nil {
				s.recordError(fmt.Errorf("feed pcm to opus encoder: %w", encErr))
				return
			}

			for _, pkt := range pkts {
				select {
				case s.packetCh <- pkt:
				case <-s.ctx.Done():
					return
				}
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
				flushPkts, flushErr := s.streamEncoder.Flush()
				if flushErr != nil {
					s.recordError(fmt.Errorf("flush opus encoder: %w", flushErr))
					return
				}
				for _, pkt := range flushPkts {
					select {
					case s.packetCh <- pkt:
					case <-s.ctx.Done():
						return
					}
				}
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
				// 忽略未知 DashScope 事件
			}
		}
	}
}

func (s *TTSPacketStream) recordError(err error) {
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

// NextPacket 接收下一个合成并编码完成的 Opus 音频包。
func (s *TTSPacketStream) NextPacket(ctx context.Context) ([]byte, error) {
	select {
	case pkt, ok := <-s.packetCh:
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
		return pkt, nil

	case <-ctx.Done():
		return nil, ctx.Err()

	case <-s.ctx.Done():
		select {
		case pkt, ok := <-s.packetCh:
			if ok {
				return pkt, nil
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

// Cancel 显式向远端服务端发送 cancel-task 中止当前合成。
func (s *TTSPacketStream) Cancel(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	if s.closed {
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
	if cancelBytes, err := json.Marshal(cancelMsg); err == nil && s.conn != nil {
		_ = s.conn.Write(ctx, websocket.MessageText, cancelBytes)
	}

	return s.Close()
}

// Close 关闭并释放流的所有网络与编码资源。
func (s *TTSPacketStream) Close() error {
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

// emptyPacketStream 提供针对空文本的空流实现。
type emptyPacketStream struct{}

func newEmptyPacketStream() *emptyPacketStream {
	return &emptyPacketStream{}
}

func (e *emptyPacketStream) NextPacket(ctx context.Context) ([]byte, error) {
	return nil, io.EOF
}

func (e *emptyPacketStream) Cancel(ctx context.Context) error {
	return nil
}

func (e *emptyPacketStream) Close() error {
	return nil
}
