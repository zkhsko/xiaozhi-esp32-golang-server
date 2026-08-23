package router

import (
	"net/http"
)

// handleSession 处理 WebSocket 会话升级请求。
func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionHandler == nil {
		http.Error(w, "session handler not configured", http.StatusInternalServerError)
		return
	}
	h.sessionHandler.ServeHTTP(w, r)
}
