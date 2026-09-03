package dashscope

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

// maxASRReadMessageBytes 定义 DashScope ASR WebSocket 单帧最大读取字节数（1 MiB）。
const maxASRReadMessageBytes = 1 * 1024 * 1024

// ASRClient 实现基于 DashScope WebSocket 流式协议的语音识别客户端。
type ASRClient struct {
	endpoint       string
	apiKey         string
	model          string
	connectTimeout time.Duration
	httpClient     *http.Client
}

// NewASRClient 基于数据库 ASR 配置实体构造 DashScope ASR 客户端实例。
func NewASRClient(cfg *database.ASRConfig) (*ASRClient, error) {
	if cfg == nil {
		return nil, errors.New("asr config cannot be nil")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("dashscope api key is required")
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("dashscope ws endpoint is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("dashscope asr model is required")
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

// Recognize 消费 16 kHz 单声道 PCM 帧流，返回 DashScope 最终识别文本。
func (c *ASRClient) Recognize(
	ctx context.Context,
	req ai.ASRRequest,
	pcm <-chan []byte,
) (string, error) {
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
		return "", fmt.Errorf("dial dashscope asr websocket: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "asr recognize completed")
	conn.SetReadLimit(maxASRReadMessageBytes)

	sampleRate := req.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}

	taskId := newUuid()
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
				SampleRate: sampleRate,
			},
		},
	}

	runBytes, err := json.Marshal(runMsg)
	if err != nil {
		return "", fmt.Errorf("marshal run-task: %w", err)
	}

	if err := conn.Write(dialCtx, websocket.MessageText, runBytes); err != nil {
		return "", fmt.Errorf("write run-task: %w", err)
	}

	msgType, firstData, err := conn.Read(dialCtx)
	if err != nil {
		return "", fmt.Errorf("read task-started: %w", err)
	}
	if msgType != websocket.MessageText {
		return "", errors.New("expected text message for task-started")
	}

	var initResp asrResponseMessage
	if err := json.Unmarshal(firstData, &initResp); err != nil {
		return "", fmt.Errorf("unmarshal task-started: %w", err)
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
		return "", fmt.Errorf("asr task start failed: [%s] %s", code, msg)
	}

	if event != "task-started" {
		return "", fmt.Errorf("unexpected initial event: %s", event)
	}

	opCtx, opCancel := context.WithCancel(ctx)
	defer opCancel()

	var (
		mu          sync.Mutex
		finalText   string
		partialText string
		readerErr   error
		vadReady    = make(chan struct{})
		taskDone    = make(chan struct{})
	)

	// 启动后台读取协程
	go func() {
		defer close(taskDone)
		for {
			mType, data, rErr := conn.Read(opCtx)
			if rErr != nil {
				mu.Lock()
				if readerErr == nil && opCtx.Err() == nil {
					readerErr = fmt.Errorf("read dashscope asr websocket: %w", rErr)
				}
				mu.Unlock()
				return
			}

			if mType != websocket.MessageText {
				continue
			}

			var resp asrResponseMessage
			if err := json.Unmarshal(data, &resp); err != nil {
				continue
			}

			ev := resp.Header.Event
			if ev == "" {
				ev = resp.Header.Action
			}

			switch ev {
			case "result-generated":
				sentence := resp.Payload.Output.Sentence
				if sentence != nil {
					if sentence.SentenceEnd {
						var t string
						if resp.Payload.Output.Text != "" {
							t = resp.Payload.Output.Text
						} else if sentence.Text != "" {
							t = sentence.Text
						}
						if t != "" {
							mu.Lock()
							finalText = t
							mu.Unlock()
							select {
							case <-vadReady:
							default:
								close(vadReady)
							}
						}
					} else {
						mu.Lock()
						if resp.Payload.Output.Text != "" {
							partialText = resp.Payload.Output.Text
						} else if sentence.Text != "" {
							partialText = sentence.Text
						}
						mu.Unlock()
					}
				} else if resp.Payload.Output.Text != "" {
					mu.Lock()
					partialText = resp.Payload.Output.Text
					mu.Unlock()
				}

			case "task-finished":
				mu.Lock()
				if resp.Payload.Output.Text != "" {
					finalText = resp.Payload.Output.Text
				} else if resp.Payload.Output.Sentence != nil && resp.Payload.Output.Sentence.Text != "" {
					finalText = resp.Payload.Output.Sentence.Text
				} else if finalText == "" && partialText != "" {
					finalText = partialText
				}
				mu.Unlock()
				select {
				case <-vadReady:
				default:
					close(vadReady)
				}
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
				mu.Lock()
				readerErr = fmt.Errorf("dashscope asr task failed: [%s] %s", code, msg)
				mu.Unlock()
				return

			default:
				// 忽略未知事件
			}
		}
	}()

	// 写入循环
	var finishSent bool
	var writeErr error

sendLoop:
	for {
		if req.Mode == ai.ASRModeAuto {
			select {
			case <-opCtx.Done():
				break sendLoop
			case <-taskDone:
				break sendLoop
			case <-vadReady:
				// Auto VAD 已识别完毕
				break sendLoop
			case pcmData, ok := <-pcm:
				if !ok {
					// 输入通道关闭，发送 finish-task
					if !finishSent {
						finishSent = true
						finishMsg := asrFinishTaskMessage{
							Header: asrRequestHeader{
								Action:    "finish-task",
								TaskId:    taskId,
								Streaming: "duplex",
							},
						}
						if fBytes, fErr := json.Marshal(finishMsg); fErr == nil {
							_ = conn.Write(opCtx, websocket.MessageText, fBytes)
						}
					}
					break sendLoop
				}
				if len(pcmData) > 0 {
					if wErr := conn.Write(opCtx, websocket.MessageBinary, pcmData); wErr != nil {
						writeErr = fmt.Errorf("write pcm binary: %w", wErr)
						break sendLoop
					}
				}
			}
		} else {
			// Manual 模式
			select {
			case <-opCtx.Done():
				break sendLoop
			case <-taskDone:
				break sendLoop
			case pcmData, ok := <-pcm:
				if !ok {
					// Manual stop，发送 finish-task 并等待 task-finished
					if !finishSent {
						finishSent = true
						finishMsg := asrFinishTaskMessage{
							Header: asrRequestHeader{
								Action:    "finish-task",
								TaskId:    taskId,
								Streaming: "duplex",
							},
						}
						if fBytes, fErr := json.Marshal(finishMsg); fErr == nil {
							if fErr := conn.Write(opCtx, websocket.MessageText, fBytes); fErr != nil {
								writeErr = fmt.Errorf("write finish-task: %w", fErr)
							}
						}
					}
					// 持续等待 reader 读完 task-finished
					select {
					case <-opCtx.Done():
					case <-taskDone:
					}
					break sendLoop
				}
				if len(pcmData) > 0 {
					if wErr := conn.Write(opCtx, websocket.MessageBinary, pcmData); wErr != nil {
						writeErr = fmt.Errorf("write pcm binary: %w", wErr)
						break sendLoop
					}
				}
			}
		}
	}

	// 退出前等待 reader 协程结束
	opCancel()
	<-taskDone

	mu.Lock()
	rErr := readerErr
	fText := strings.TrimSpace(finalText)
	mu.Unlock()

	if writeErr != nil && !errors.Is(writeErr, context.Canceled) {
		return "", writeErr
	}
	if rErr != nil && !errors.Is(rErr, context.Canceled) {
		return "", rErr
	}
	if opCtx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return "", ctx.Err()
	}

	if req.Mode == ai.ASRModeAuto && fText == "" {
		return "", errors.New("dashscope asr returned empty text in auto mode")
	}

	return fText, nil
}

func newUuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
