package router

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// AdminResponse 通用管理员 API 返回结构。
type AdminResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// parsePagination 从 HTTP 请求的 URL query 中提取 page 与 page_size 参数。
// page 缺省为 1；page_size 缺省为 10，最大限制为 100。
func parsePagination(r *http.Request) (int, int) {
	query := r.URL.Query()
	page, _ := strconv.Atoi(query.Get("page"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(query.Get("page_size"))
	if pageSize <= 0 {
		pageSize = 10
	} else if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

// decodeJSON 解析 JSON 请求体，若解析失败则直接向客户端输出 400 错误。
func decodeJSON(w http.ResponseWriter, r *http.Request, dest any) bool {
	if err := json.NewDecoder(r.Body).Decode(dest); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return false
	}
	return true
}
