package bailian

import (
	"context"
	"crypto/rand"
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

// maxASRReadMessageBytes 定义百炼 ASR WebSocket 单帧最大读取字节数（1 MiB）。
const maxASRReadMessageBytes = 1 * 1024 * 1024

// ASRClient 实现基于百炼 WebSocket 流式协议的语音识别客户端。
type ASRClient struct {
	endpoint       string
	apiKey         string
	model          string
	connectTimeout time.Duration
	httpClient     *http.Client
}

// NewASRClient 基于数据库 ASR 配置实体构造百炼 ASR 客户端实例。
func NewASRClient(cfg *database.ASRConfig) (*ASRClient, error) {
	if cfg == nil {
		return nil, errors.New("asr config cannot be nil")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("dashscope api key is required")
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("bailian ws endpoint is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("bailian asr model is required")
	}

	timeout := time.Duration(cfg.ConnectTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
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

	return &ASRClient{
		endpoint:       strings.TrimSpace(cfg.Endpoint),
		apiKey:         strings.TrimSpace(cfg.APIKey),
		model:          strings.TrimSpace(cfg.Model),
		connectTimeout: timeout,
		httpClient:     httpClient,
	}, nil
}

type asrRequestHeader struct {
	Action    string `json:"action"`
	TaskId    string `json:"task_id"`
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
	SentenceId    int    `json:"sentence_id"`
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
		TaskId       string `json:"task_id"`
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
	conn.SetReadLimit(maxASRReadMessageBytes)

	taskId := newUUID()
	runMsg := asrRunTaskMessage{
		Header: asrRequestHeader{
			Action:    "run-task",
			TaskId:    taskId,
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
		code := initResp.Header.ErrorCode
		if code == "" {
			code = "UNKNOWN_ERROR"
		}
		msg := initResp.Header.ErrorMessage
		if msg == "" {
			msg = "task start failed"
		}
		_ = conn.Close(websocket.StatusNormalClosure, "task-failed")
		return nil, fmt.Errorf("asr task start failed: [%s] %s", code, msg)
	}

	if event != "task-started" {
		_ = conn.Close(websocket.StatusPolicyViolation, "unexpected initial event: "+event)
		return nil, fmt.Errorf("unexpected initial event: %s", event)
	}

	streamCtx, streamCancel := context.WithCancel(ctx)

	stream := &ASRStream{
		conn:           conn,
		taskId:         taskId,
		ctx:            streamCtx,
		cancel:         streamCancel,
		vadReady:       make(chan struct{}),
		taskFinishedCh: make(chan struct{}),
	}

	go stream.readLoop()

	return stream, nil
}

// ASRStream 实现百炼流式语音识别会话。
type ASRStream struct {
	conn   *websocket.Conn
	taskId string

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once

	writeMu sync.Mutex

	mu             sync.RWMutex
	closed         bool
	finished       bool
	finishCalled   bool
	partialText    string
	finalText      string
	err            error
	vadReady       chan struct{}
	taskFinishedCh chan struct{}
}

func (s *ASRStream) readLoop() {
	defer func() {
		if s.conn != nil {
			_ = s.conn.Close(websocket.StatusNormalClosure, "stream closed")
		}
		s.mu.Lock()
		select {
		case <-s.vadReady:
		default:
			close(s.vadReady)
		}
		select {
		case <-s.taskFinishedCh:
		default:
			close(s.taskFinishedCh)
		}
		s.mu.Unlock()
	}()

	for {
		msgType, data, err := s.conn.Read(s.ctx)
		if err != nil {
			s.recordError(fmt.Errorf("read bailian asr websocket: %w", err))
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
			sentence := resp.Payload.Output.Sentence
			if sentence != nil {
				if sentence.SentenceEnd {
					var text string
					if resp.Payload.Output.Text != "" {
						text = resp.Payload.Output.Text
					} else if sentence.Text != "" {
						text = sentence.Text
					}
					if text != "" {
						s.updateFinalText(text)
						s.markVADReady()
					} else {
						s.mu.RLock()
						hasFinal := (s.finalText != "")
						s.mu.RUnlock()
						if hasFinal {
							s.markVADReady()
						}
					}
				} else {
					if resp.Payload.Output.Text != "" {
						s.updatePartialText(resp.Payload.Output.Text)
					} else if sentence.Text != "" {
						s.updatePartialText(sentence.Text)
					}
				}
			} else if resp.Payload.Output.Text != "" {
				s.updatePartialText(resp.Payload.Output.Text)
			}

		case "task-finished":
			if resp.Payload.Output.Text != "" {
				s.updateFinalText(resp.Payload.Output.Text)
			} else if resp.Payload.Output.Sentence != nil && resp.Payload.Output.Sentence.Text != "" {
				s.updateFinalText(resp.Payload.Output.Sentence.Text)
			} else {
				s.mu.Lock()
				if s.finalText == "" && s.partialText != "" {
					s.finalText = s.partialText
				}
				s.mu.Unlock()
			}
			s.markFinished()
			return

		case "task-failed":
			code := resp.Header.ErrorCode
			if code == "" {
				code = "UNKNOWN_ERROR"
			}
			msg := resp.Header.ErrorMessage
			if msg == "" {
				msg = "asr task failed on server"
			}
			s.recordError(fmt.Errorf("bailian asr task failed: [%s] %s", code, msg))
			return

		default:
			// 忽略百炼未知事件，保证状态不混乱、不崩溃
		}
	}
}

func (s *ASRStream) updatePartialText(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partialText = text
}

func (s *ASRStream) updateFinalText(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalText = text
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
	case <-s.vadReady:
	default:
		close(s.vadReady)
	}
	select {
	case <-s.taskFinishedCh:
	default:
		close(s.taskFinishedCh)
	}
}

func (s *ASRStream) markVADReady() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.vadReady:
	default:
		close(s.vadReady)
	}
}

func (s *ASRStream) markFinished() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.vadReady:
	default:
		close(s.vadReady)
	}
	select {
	case <-s.taskFinishedCh:
	default:
		close(s.taskFinishedCh)
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
	if s.err != nil {
		err := s.err
		s.mu.RUnlock()
		return err
	}
	s.mu.RUnlock()

	if err := s.conn.Write(ctx, websocket.MessageBinary, data); err != nil {
		s.recordError(fmt.Errorf("write pcm binary: %w", err))
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
	s.finishCalled = true
	if s.finished {
		s.mu.Unlock()
		return nil
	}
	if s.err != nil {
		err := s.err
		s.mu.Unlock()
		return err
	}
	taskId := s.taskId
	s.mu.Unlock()

	finishMsg := asrFinishTaskMessage{
		Header: asrRequestHeader{
			Action:    "finish-task",
			TaskId:    taskId,
			Streaming: "duplex",
		},
	}
	msgBytes, err := json.Marshal(finishMsg)
	if err != nil {
		return fmt.Errorf("marshal finish-task: %w", err)
	}

	if err := s.conn.Write(ctx, websocket.MessageText, msgBytes); err != nil {
		s.recordError(fmt.Errorf("write finish-task: %w", err))
		return fmt.Errorf("write finish-task: %w", err)
	}
	return nil
}

// Result 等待并返回最终非空识别文本。
func (s *ASRStream) Result(ctx context.Context) (string, error) {
	s.mu.RLock()
	finishCalled := s.finishCalled
	s.mu.RUnlock()

	var waitCh chan struct{}
	if finishCalled {
		waitCh = s.taskFinishedCh
	} else {
		waitCh = s.vadReady
	}

	select {
	case <-waitCh:
		s.mu.RLock()
		defer s.mu.RUnlock()
		if s.err != nil {
			return "", s.err
		}
		return s.finalText, nil
	case <-s.taskFinishedCh:
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
		case <-s.vadReady:
		default:
			close(s.vadReady)
		}
		select {
		case <-s.taskFinishedCh:
		default:
			close(s.taskFinishedCh)
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
