package bailian

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai"
	"xiaozhi-esp32-golang-server/internal/config"
)

// ASRClient 实现基于百炼 WebSocket 流式协议的语音识别客户端。
type ASRClient struct {
	endpoint       string
	apiKey         string
	model          string
	connectTimeout time.Duration
	httpClient     *http.Client
}

// NewASRClient 基于服务端配置构造百炼 ASR 客户端实例。
func NewASRClient(cfg *config.Config) (*ASRClient, error) {
	if cfg == nil {
		return nil, errors.New("config cannot be nil")
	}
	if cfg.DashScopeAPIKey == "" {
		return nil, errors.New("dashscope api key is required")
	}
	if cfg.AI.Bailian.WSEndpoint == "" {
		return nil, errors.New("bailian ws endpoint is required")
	}
	if cfg.AI.Bailian.ASRModel == "" {
		return nil, errors.New("bailian asr model is required")
	}

	timeout := cfg.AI.Bailian.ASRConnectTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	var httpClient *http.Client
	if cfg.Proxy.Enabled && cfg.Proxy.URL != "" {
		proxyURL, err := url.Parse(cfg.Proxy.URL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy url: %w", err)
		}
		httpClient = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}
	}

	return &ASRClient{
		endpoint:       cfg.AI.Bailian.WSEndpoint,
		apiKey:         cfg.DashScopeAPIKey,
		model:          cfg.AI.Bailian.ASRModel,
		connectTimeout: timeout,
		httpClient:     httpClient,
	}, nil
}

type asrRequestHeader struct {
	Action    string `json:"action"`
	TaskID    string `json:"task_id"`
	Streaming string `json:"streaming"`
}

type asrParameters struct {
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
}

type asrRunPayload struct {
	TaskGroup  string        `json:"task_group"`
	Task       string        `json:"task"`
	Function   string        `json:"function"`
	Model      string        `json:"model"`
	Parameters asrParameters `json:"parameters"`
	Input      struct{}      `json:"input"`
}

type asrRunTaskMessage struct {
	Header  asrRequestHeader `json:"header"`
	Payload asrRunPayload    `json:"payload"`
}

type asrFinishPayload struct {
	Input struct{} `json:"input"`
}

type asrFinishTaskMessage struct {
	Header  asrRequestHeader `json:"header"`
	Payload asrFinishPayload `json:"payload"`
}

type asrSentenceOutput struct {
	SentenceID    int    `json:"sentence_id"`
	SentenceBegin bool   `json:"sentence_begin"`
	SentenceEnd   bool   `json:"sentence_end"`
	Text          string `json:"text"`
	BeginTime     int64  `json:"begin_time"`
	EndTime       int64  `json:"end_time"`
}

type asrResponsePayload struct {
	Output struct {
		Sentence *asrSentenceOutput `json:"sentence,omitempty"`
		Text     string             `json:"text,omitempty"`
	} `json:"output"`
	Usage struct {
		Duration int64 `json:"duration"`
	} `json:"usage"`
}

type asrResponseMessage struct {
	Header struct {
		Action       string `json:"action"`
		TaskID       string `json:"task_id"`
		Event        string `json:"event"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	} `json:"header"`
	Payload asrResponsePayload `json:"payload"`
}

// CreateStream 创建并启动一条百炼流式语音识别会话。
func (c *ASRClient) CreateStream(ctx context.Context) (ai.ASRStream, error) {
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
		return nil, fmt.Errorf("dial bailian asr websocket: %w", err)
	}

	taskID := newUUID()
	runMsg := asrRunTaskMessage{
		Header: asrRequestHeader{
			Action:    "run-task",
			TaskID:    taskID,
			Streaming: "duplex",
		},
		Payload: asrRunPayload{
			TaskGroup: "audio",
			Task:      "asr",
			Function:  "recognition",
			Model:     c.model,
			Parameters: asrParameters{
				Format:     "pcm",
				SampleRate: 16000,
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

	var initResp asrResponseMessage
	if err := json.Unmarshal(firstData, &initResp); err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid task-started response json")
		return nil, fmt.Errorf("unmarshal task-started: %w", err)
	}

	event := initResp.Header.Event
	if event == "" {
		event = initResp.Header.Action
	}

	if event == "task-failed" {
		_ = conn.Close(websocket.StatusNormalClosure, "task-failed")
		return nil, fmt.Errorf("asr task start failed: [%s] %s", initResp.Header.ErrorCode, initResp.Header.ErrorMessage)
	}

	if event != "task-started" {
		_ = conn.Close(websocket.StatusPolicyViolation, "unexpected initial event: "+event)
		return nil, fmt.Errorf("unexpected initial event: %s", event)
	}

	streamCtx, streamCancel := context.WithCancel(ctx)

	stream := &ASRStream{
		conn:        conn,
		taskID:      taskID,
		ctx:         streamCtx,
		cancel:      streamCancel,
		resultReady: make(chan struct{}),
	}

	go stream.readLoop()

	return stream, nil
}

// ASRStream 实现百炼流式语音识别会话。
type ASRStream struct {
	conn   *websocket.Conn
	taskID string

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once

	writeMu sync.Mutex

	mu          sync.RWMutex
	closed      bool
	finished    bool
	finalText   string
	err         error
	resultReady chan struct{}
}

func (s *ASRStream) readLoop() {
	defer func() {
		s.mu.Lock()
		select {
		case <-s.resultReady:
		default:
			close(s.resultReady)
		}
		s.mu.Unlock()
	}()

	for {
		msgType, data, err := s.conn.Read(s.ctx)
		if err != nil {
			s.recordError(err)
			return
		}

		if msgType != websocket.MessageText {
			continue
		}

		var resp asrResponseMessage
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}

		event := resp.Header.Event
		if event == "" {
			event = resp.Header.Action
		}

		switch event {
		case "result-generated":
			if resp.Payload.Output.Sentence != nil && resp.Payload.Output.Sentence.Text != "" {
				s.updateText(resp.Payload.Output.Sentence.Text)
			} else if resp.Payload.Output.Text != "" {
				s.updateText(resp.Payload.Output.Text)
			}

		case "task-finished":
			if resp.Payload.Output.Sentence != nil && resp.Payload.Output.Sentence.Text != "" {
				s.updateText(resp.Payload.Output.Sentence.Text)
			} else if resp.Payload.Output.Text != "" {
				s.updateText(resp.Payload.Output.Text)
			}
			s.markFinished()
			return

		case "task-failed":
			s.recordError(fmt.Errorf("bailian asr task failed: [%s] %s", resp.Header.ErrorCode, resp.Header.ErrorMessage))
			return
		}
	}
}

func (s *ASRStream) updateText(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if text != "" {
		s.finalText = text
	}
}

func (s *ASRStream) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		if s.closed || s.ctx.Err() != nil {
			s.err = context.Canceled
		} else {
			s.err = err
		}
	}
	select {
	case <-s.resultReady:
	default:
		close(s.resultReady)
	}
}

func (s *ASRStream) markFinished() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.resultReady:
	default:
		close(s.resultReady)
	}
}

// WritePCM 流式写入 PCM 二进制音频帧（16000 Hz、16-bit、单声道小端）。
func (s *ASRStream) WritePCM(ctx context.Context, data []byte) error {
	if len(data) == 0 {
		return nil
	}

	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return errors.New("asr stream is closed")
	}
	if s.finished {
		s.mu.RUnlock()
		return errors.New("cannot write pcm to finished asr stream")
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
		return errors.New("asr stream is closed")
	}
	s.mu.RUnlock()

	if err := s.conn.Write(ctx, websocket.MessageBinary, data); err != nil {
		return fmt.Errorf("write pcm binary: %w", err)
	}
	return nil
}

// Finish 通知百炼服务端音频流输入已结束。
func (s *ASRStream) Finish(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("asr stream is closed")
	}
	if s.finished {
		s.mu.Unlock()
		return nil
	}
	s.finished = true
	taskID := s.taskID
	s.mu.Unlock()

	finishMsg := asrFinishTaskMessage{
		Header: asrRequestHeader{
			Action:    "finish-task",
			TaskID:    taskID,
			Streaming: "duplex",
		},
	}
	msgBytes, err := json.Marshal(finishMsg)
	if err != nil {
		return fmt.Errorf("marshal finish-task: %w", err)
	}

	if err := s.conn.Write(ctx, websocket.MessageText, msgBytes); err != nil {
		return fmt.Errorf("write finish-task: %w", err)
	}
	return nil
}

// Result 等待并返回最终非空识别文本。
func (s *ASRStream) Result(ctx context.Context) (string, error) {
	select {
	case <-s.resultReady:
		s.mu.RLock()
		defer s.mu.RUnlock()
		if s.err != nil {
			return "", s.err
		}
		return s.finalText, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.ctx.Done():
		s.mu.RLock()
		defer s.mu.RUnlock()
		if s.err != nil {
			return "", s.err
		}
		return s.finalText, s.ctx.Err()
	}
}

// Close 关闭并释放流式识别会话的所有网络与内存资源。
func (s *ASRStream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()

		s.cancel()

		if s.conn != nil {
			err = s.conn.Close(websocket.StatusNormalClosure, "stream closed")
		}

		s.mu.Lock()
		select {
		case <-s.resultReady:
		default:
			close(s.resultReady)
		}
		s.mu.Unlock()
	})
	return err
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
