package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminEmbeddedHandler(t *testing.T) {
	handler := Handler()

	t.Run("Serve index.html for root path", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "<div id=\"app\"></div>") {
			t.Errorf("expected body to contain '<div id=\"app\"></div>', got: %s", body)
		}
		if !strings.Contains(body, "小智管理后台") {
			t.Errorf("expected body to contain '小智管理后台', got: %s", body)
		}
	})

	t.Run("Serve SPA route fallback to index.html", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "<div id=\"app\"></div>") {
			t.Errorf("expected body to contain '<div id=\"app\"></div>', got: %s", body)
		}
	})

	t.Run("Serve static asset file", func(t *testing.T) {
		// 先获取 index.html 查找其中引用的 js/css 文件名
		reqRoot := httptest.NewRequest(http.MethodGet, "/", nil)
		recRoot := httptest.NewRecorder()
		handler.ServeHTTP(recRoot, reqRoot)

		body := recRoot.Body.String()
		startIdx := strings.Index(body, "/admin/assets/")
		if startIdx == -1 {
			t.Fatalf("no assets found in index.html")
		}
		endIdx := strings.Index(body[startIdx:], "\"")
		if endIdx == -1 {
			t.Fatalf("cannot parse asset url in index.html")
		}
		assetURL := body[startIdx : startIdx+endIdx]
		// 去掉 /admin 前缀
		assetPath := strings.TrimPrefix(assetURL, "/admin")

		req := httptest.NewRequest(http.MethodGet, assetPath, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200 for asset %s, got %d", assetPath, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("expected non-empty response body for asset %s", assetPath)
		}
	})
}
