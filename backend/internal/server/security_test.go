package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSRejectsUnexpectedOrigin(t *testing.T) {
	srv := &Server{cfg: Config{AllowOrigin: "https://chat.example.test"}}
	handler := srv.withCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.Header.Set("Origin", "https://attacker.example")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("許可外Originを拒否していない: status=%d", recorder.Code)
	}
}

func TestCORSAllowsConfiguredOrigin(t *testing.T) {
	const origin = "https://chat.example.test"
	srv := &Server{cfg: Config{AllowOrigin: origin}}
	handler := srv.withCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.Header.Set("Origin", origin)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("許可Originを拒否した: status=%d", recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("CORSヘッダーが不正: %q", got)
	}
}

func TestCORSReturnsTheMatchingOriginFromAList(t *testing.T) {
	const origin = "http://127.0.0.1:5173"
	srv := &Server{cfg: Config{AllowOrigin: "https://chat.example.test, http://localhost:5173"}}
	handler := srv.withCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/login", nil)
	req.Header.Set("Origin", origin)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("一致したOriginを返していない: %q", got)
	}
}

func TestOriginAllowsLoopbackAliasesOnlyOnSamePort(t *testing.T) {
	configured := "http://localhost:5173/"
	for _, origin := range []string{"http://localhost:5173", "http://127.0.0.1:5173", "http://[::1]:5173"} {
		if !originAllowed(origin, configured) {
			t.Errorf("ローカル表記を同一Originとして扱っていない: %s", origin)
		}
	}
	for _, origin := range []string{"http://localhost:8080", "https://localhost:5173", "https://attacker.example"} {
		if originAllowed(origin, configured) {
			t.Errorf("異なるOriginを許可した: %s", origin)
		}
	}
}

func TestAppCookieUsesCrossSiteSecurityAttributes(t *testing.T) {
	cookie := appCookie("test", "value", 60)
	if !cookie.HttpOnly || !cookie.Secure || !cookie.Partitioned || cookie.SameSite != http.SameSiteNoneMode {
		t.Fatalf("Cookieのセキュリティ属性が不足: %+v", cookie)
	}
}

func TestOversizedJSONReturnsRequestEntityTooLarge(t *testing.T) {
	srv, _ := testServer(t, nil)
	body := `{"username":"利用者","password":"` + strings.Repeat("x", maxSmallRequestBodyBytes) + `"}`
	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body)))
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("大きすぎる本文の状態コード=%d want=%d", res.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestJSONRejectsTrailingValue(t *testing.T) {
	srv, _ := testServer(t, nil)
	res := httptest.NewRecorder()
	req := srv.testRequest(http.MethodPut, "/api/profile/icon", `{"icon":""} {"icon":"data:image/png;base64,AA=="}`, "利用者")
	srv.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("JSONの後続値を受理した: status=%d body=%s", res.Code, res.Body.String())
	}
}
