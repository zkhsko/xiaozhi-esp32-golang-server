package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouter_AdminStaticMount(t *testing.T) {
	r := NewRouter(Options{})

	t.Run("GET /admin/ returns index.html", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "<div id=\"app\"></div>") {
			t.Errorf("expected body to contain '<div id=\"app\"></div>', got: %s", rec.Body.String())
		}
	})

	t.Run("GET /admin/dashboard returns index.html (SPA Fallback)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "<div id=\"app\"></div>") {
			t.Errorf("expected body to contain '<div id=\"app\"></div>', got: %s", rec.Body.String())
		}
	})
}
