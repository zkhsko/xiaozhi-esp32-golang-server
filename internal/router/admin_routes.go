package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// AdminRoutes 统一装配管理端所有子资源的 HTTP 路由。
func AdminRoutes(h *AdminHandler) http.Handler {
	r := chi.NewRouter()

	// Device HMAC Credential 接口
	if h.credentialHandler != nil {
		r.Get("/device-hmac-credential", h.credentialHandler.handleListCredentials)
		r.Get("/device-hmac-credential/list", h.credentialHandler.handleListCredentials)
		r.Post("/device-hmac-credential/generate", h.credentialHandler.handleGenerateCredential)
		r.Post("/device-hmac-credential/update", h.credentialHandler.handleUpdateCredential)
		r.Post("/device-hmac-credential/delete", h.credentialHandler.handleDeleteCredential)
		r.Post("/device-hmac-credential/batch-delete", h.credentialHandler.handleBatchDeleteCredentials)
	}

	// Device Activation 接口
	if h.activationHandler != nil {
		r.Get("/device-activation", h.activationHandler.handleListActivations)
		r.Get("/device-activation/list", h.activationHandler.handleListActivations)
		r.Post("/device-activation/update", h.activationHandler.handleUpdateActivation)
		r.Post("/device-activation/delete", h.activationHandler.handleDeleteActivation)
		r.Post("/device-activation/batch-delete", h.activationHandler.handleBatchDeleteActivations)
	}

	// ASR Config 接口
	if h.asrHandler != nil {
		r.Get("/asr-config", h.asrHandler.handleListASRConfigs)
		r.Get("/asr-config/list", h.asrHandler.handleListASRConfigs)
		r.Post("/asr-config/save", h.asrHandler.handleSaveASRConfig)
		r.Post("/asr-config/update", h.asrHandler.handleSaveASRConfig)
		r.Post("/asr-config/delete", h.asrHandler.handleDeleteASRConfig)
		r.Post("/asr-config/batch-delete", h.asrHandler.handleBatchDeleteASRConfigs)
	}

	// LLM Config 接口
	if h.llmHandler != nil {
		r.Get("/llm-config", h.llmHandler.handleListLLMConfigs)
		r.Get("/llm-config/list", h.llmHandler.handleListLLMConfigs)
		r.Post("/llm-config/save", h.llmHandler.handleSaveLLMConfig)
		r.Post("/llm-config/update", h.llmHandler.handleSaveLLMConfig)
		r.Post("/llm-config/delete", h.llmHandler.handleDeleteLLMConfig)
		r.Post("/llm-config/batch-delete", h.llmHandler.handleBatchDeleteLLMConfigs)
	}

	// TTS Config 接口
	if h.ttsHandler != nil {
		r.Get("/tts-config", h.ttsHandler.handleListTTSConfigs)
		r.Get("/tts-config/list", h.ttsHandler.handleListTTSConfigs)
		r.Post("/tts-config/save", h.ttsHandler.handleSaveTTSConfig)
		r.Post("/tts-config/update", h.ttsHandler.handleSaveTTSConfig)
		r.Post("/tts-config/delete", h.ttsHandler.handleDeleteTTSConfig)
		r.Post("/tts-config/batch-delete", h.ttsHandler.handleBatchDeleteTTSConfigs)
	}

	// Agent Config 接口
	if h.agentHandler != nil {
		r.Get("/agent-config", h.agentHandler.handleListAgentConfigs)
		r.Get("/agent-config/list", h.agentHandler.handleListAgentConfigs)
		r.Post("/agent-config/save", h.agentHandler.handleSaveAgentConfig)
		r.Post("/agent-config/update", h.agentHandler.handleSaveAgentConfig)
		r.Post("/agent-config/delete", h.agentHandler.handleDeleteAgentConfig)
		r.Post("/agent-config/batch-delete", h.agentHandler.handleBatchDeleteAgentConfigs)
		r.Post("/agent-config/activate", h.agentHandler.handleActivateAgentConfig)
	}

	// Device Type 接口
	if h.deviceTypeHandler != nil {
		r.Get("/device-type", h.deviceTypeHandler.handleListDeviceTypes)
		r.Get("/device-type/list", h.deviceTypeHandler.handleListDeviceTypes)
		r.Post("/device-type/save", h.deviceTypeHandler.handleSaveDeviceType)
		r.Post("/device-type/update", h.deviceTypeHandler.handleSaveDeviceType)
		r.Post("/device-type/delete", h.deviceTypeHandler.handleDeleteDeviceType)
		r.Post("/device-type/batch-delete", h.deviceTypeHandler.handleBatchDeleteDeviceTypes)
	}

	return r
}
