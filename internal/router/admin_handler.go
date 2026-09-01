package router

import (
	"log/slog"
	"net/http"

	"xiaozhi-esp32-golang-server/internal/config"
	"xiaozhi-esp32-golang-server/internal/database"
)

// AdminHandler 汇聚管理端各个资源的处理器。
type AdminHandler struct {
	credentialHandler *AdminCredentialHandler
	activationHandler *AdminActivationHandler
	asrHandler        *AdminASRHandler
	llmHandler        *AdminLLMHandler
	ttsHandler        *AdminTTSHandler
	agentHandler      *AdminAgentHandler
	deviceTypeHandler *AdminDeviceTypeHandler
	logger            *slog.Logger
}

// NewAdminHandler 创建汇聚的 AdminHandler 实例，并初始化各个子资源处理器。
func NewAdminHandler(cfg *config.Config, db *database.Database, l *slog.Logger) *AdminHandler {
	if l == nil {
		l = slog.Default()
	}

	var credStore DeviceCredentialStore
	var actStore DeviceActivationStore
	var asrStore ASRConfigStore
	var llmStore LLMConfigStore
	var ttsStore TTSConfigStore
	var agentStore AgentConfigStore
	var dtStore DeviceTypeStore

	if db != nil {
		credStore = db
		actStore = db
		asrStore = db
		llmStore = db
		ttsStore = db
		agentStore = db
		dtStore = db
	}

	return &AdminHandler{
		credentialHandler: NewAdminCredentialHandler(cfg, credStore, l),
		activationHandler: NewAdminActivationHandler(actStore, l),
		asrHandler:        NewAdminASRHandler(asrStore, l),
		llmHandler:        NewAdminLLMHandler(llmStore, l),
		ttsHandler:        NewAdminTTSHandler(ttsStore, l),
		agentHandler:      NewAdminAgentHandler(agentStore, l),
		deviceTypeHandler: NewAdminDeviceTypeHandler(dtStore, l),
		logger:            l,
	}
}

// Routes 返回装配好的管理端 HTTP 路由。
func (h *AdminHandler) Routes() http.Handler {
	return AdminRoutes(h)
}
