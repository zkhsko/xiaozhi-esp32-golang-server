package session

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"

	"xiaozhi-esp32-golang-server/internal/ai/factory"
	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
	"xiaozhi-esp32-golang-server/internal/logger"
)

// Handler 处理 WebSocket 协议升级、设备智能体动态加载、会话准入控制与连接生命周期。
type Handler struct {
	cfg      *config.Config
	db       DeviceAgentResolver
	registry *Registry
	logger   *slog.Logger
}

// HandlerOptions 聚合创建 WebSocket HTTP 升级处理器的依赖与配置。
type HandlerOptions struct {
	Config   *config.Config
	DB       DeviceAgentResolver
	Limiter  *SessionLimiter
	Registry *Registry
	Logger   *slog.Logger
}

// NewHandler 使用具名选项创建配置就绪的 WebSocket HTTP 升级处理器。
func NewHandler(opts HandlerOptions) *Handler {
	l := opts.Logger
	if l == nil {
		l = slog.Default()
	}

	reg := opts.Registry
	if reg == nil {
		limiter := opts.Limiter
		if limiter == nil {
			maxSessions := 1
			if opts.Config != nil && opts.Config.Server.MaxConcurrentSessions > 0 {
				maxSessions = opts.Config.Server.MaxConcurrentSessions
			}
			limiter = NewSessionLimiter(maxSessions)
		}
		reg = NewRegistry(limiter, l)
	}

	return &Handler{
		cfg:      opts.Config,
		db:       opts.DB,
		registry: reg,
		logger:   l,
	}
}

// Registry 返回当前关联的会话注册表。
func (h *Handler) Registry() *Registry {
	return h.registry
}

// Limiter 返回当前关联的会话准入控制器。
func (h *Handler) Limiter() *SessionLimiter {
	if h.registry != nil {
		return h.registry.Limiter()
	}
	return nil
}

// Close 优雅关闭会话处理器及其关联的注册表。
func (h *Handler) Close(ctx context.Context) error {
	if h.registry != nil {
		return h.registry.Shutdown(ctx)
	}
	return nil
}

// Shutdown 优雅关闭会话处理器及其关联的注册表。
func (h *Handler) Shutdown(ctx context.Context) error {
	return h.Close(ctx)
}

// ServeHTTP 校验 HTTP 认证并执行单表分步智能体加载、会话准入控制与 WebSocket 升级。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. 校验请求路径（精确匹配 /xiaozhi/v1/）
	if r.URL.Path != WebSocketPath {
		http.NotFound(w, r)
		return
	}

	// 2. 升级前认证与请求头校验（从数据库查询 Token 鉴权并获取设备 SN 与 DeviceType）
	maxHeaderBytes := MaxSingleHeaderBytes
	if h.cfg != nil && h.cfg.Server.MaxHTTPHeaderBytes > 0 {
		maxHeaderBytes = h.cfg.Server.MaxHTTPHeaderBytes
	}

	tok, err := AuthenticateUpgrade(r, h.db, maxHeaderBytes)
	if err != nil {
		RejectUpgrade(w, r, h.logger, err)
		return
	}
	LogAuthSuccess(h.logger, r, tok.SerialNumber)

	// 3. 单表分步点查设备类型绑定的智能体及其组件快照（无 JOIN、Fail Fast）
	if h.db == nil {
		RejectUpgrade(w, r, h.logger, database.ErrDatabaseInstanceRequired)
		return
	}
	snapshot, err := h.db.ResolveAgentRuntimeSnapshotByDeviceType(r.Context(), tok.DeviceType)
	if err != nil {
		RejectUpgrade(w, r, h.logger, err)
		return
	}

	// 4. 工厂实例化该会话专属的 ASR, LLM, TTS 客户端
	asrClient, err := factory.CreateASRClient(&snapshot.ASRConfig)
	if err != nil {
		RejectUpgrade(w, r, h.logger, err)
		return
	}
	llmClient, err := factory.CreateLLMClient(&snapshot.LLMConfig)
	if err != nil {
		RejectUpgrade(w, r, h.logger, err)
		return
	}
	ttsQueueCap := DefaultWriteQueueCapacity
	if h.cfg != nil && h.cfg.Session.TTSPCMQueueCapacity > 0 {
		ttsQueueCap = h.cfg.Session.TTSPCMQueueCapacity
	}
	ttsClient, err := factory.CreateTTSClient(&snapshot.TTSConfig, snapshot.Agent.Voice, ttsQueueCap)
	if err != nil {
		RejectUpgrade(w, r, h.logger, err)
		return
	}

	// 5. 活跃会话并发准入控制（满载或停服时拒绝升级并返回 503）
	release, ok := h.registry.Acquire()
	if !ok {
		h.logger.Warn("websocket upgrade rejected: max concurrent sessions reached or shutting down",
			"serial_number", logger.TruncateString(tok.SerialNumber),
			"active_sessions", h.registry.Limiter().ActiveCount(),
			"max_sessions", h.registry.Limiter().MaxSessions(),
		)
		http.Error(w, "service unavailable: max concurrent sessions reached", http.StatusServiceUnavailable)
		return
	}

	// 6. 执行 WebSocket 协议升级，显式禁用压缩
	opts := &websocket.AcceptOptions{
		CompressionMode:    websocket.CompressionDisabled,
		InsecureSkipVerify: true,
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		release()
		h.logger.Error("websocket upgrade failed",
			"error", err,
			"serial_number", logger.TruncateString(tok.SerialNumber),
		)
		return
	}
	defer conn.Close(websocket.StatusInternalError, "session closed")

	h.logger.Info("websocket session connected",
		"serial_number", logger.TruncateString(tok.SerialNumber),
		"device_type", tok.DeviceType,
		"agent_id", snapshot.Agent.Id,
		"active_sessions", h.registry.Limiter().ActiveCount(),
	)

	// 7. 构造专属 Session 并注册到会话注册表，统一注销与名额释放顺序
	sess := NewSession(r.Context(), Options{
		Conn:         conn,
		SerialNumber: tok.SerialNumber,
		SystemPrompt: snapshot.Agent.SystemPrompt,
		Config:       h.cfg,
		ASRClient:    asrClient,
		LLMClient:    llmClient,
		TTSClient:    ttsClient,
		Logger:       h.logger,
	})
	unregister, registered := h.registry.Register(sess, release)
	if !registered {
		release()
		sess.Close()
		return
	}
	defer unregister()

	// 8. 移交会话监督流程处理状态机生命周期，直至客户端断开或上下文取消
	_ = sess.Run()

	h.logger.Info("websocket session closed",
		"serial_number", logger.TruncateString(tok.SerialNumber),
	)
}
