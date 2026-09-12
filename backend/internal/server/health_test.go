package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/index"
)

func TestHealthEndpoint(t *testing.T) {
	srv := &Server{live: index.NewLive(&index.Index{}, "test")}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	recorder := httptest.NewRecorder()

	srv.Routes().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("ヘルスチェックが成功しない: status=%d", recorder.Code)
	}
}
