package server

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/index"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

func discordServer(t *testing.T, publicKey string) *Server {
	t.Helper()
	return &Server{
		cfg: Config{
			SessionSecret: "テスト用の固定鍵テスト用の固定鍵", DailyLimit: 30,
			DiscordPublicKey: publicKey, DiscordAppID: "app",
		},
		live:  index.NewLive(&index.Index{}, "test"),
		state: state.NewMemory(),
	}
}

func discordRequest(t *testing.T, private ed25519.PrivateKey, body string, sign bool) *http.Request {
	t.Helper()
	timestamp := "1789000000"
	req := httptest.NewRequest(http.MethodPost, "/api/discord", strings.NewReader(body))
	req.Header.Set("X-Signature-Timestamp", timestamp)
	signature := strings.Repeat("00", ed25519.SignatureSize)
	if sign {
		signature = hex.EncodeToString(ed25519.Sign(private, append([]byte(timestamp), body...)))
	}
	req.Header.Set("X-Signature-Ed25519", signature)
	return req
}

// ⚠️ **署名を見ないと誰でも叩ける。** この口は公開されるので、通ると無料枠を
// 好きなだけ消費させられる。Discordも登録時にわざと壊した署名を送ってくる。
func TestDiscordRejectsBadSignature(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := discordServer(t, hex.EncodeToString(public))

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, discordRequest(t, private, `{"type":1}`, false))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("壊れた署名を通した: %d", res.Code)
	}

	// 署名そのものが無い場合も通さない
	req := httptest.NewRequest(http.MethodPost, "/api/discord", strings.NewReader(`{"type":1}`))
	res = httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("署名なしを通した: %d", res.Code)
	}
}

// 疎通確認に応えられないと、Discordはエンドポイントを登録させてくれない。
func TestDiscordAnswersPing(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := discordServer(t, hex.EncodeToString(public))

	res := httptest.NewRecorder()
	srv.Routes().ServeHTTP(res, discordRequest(t, private, `{"type":1}`, true))
	if res.Code != http.StatusOK {
		t.Fatalf("疎通確認に失敗: %d", res.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("応答を読めない: %v", err)
	}
	if body["type"] != float64(1) {
		t.Fatalf("PONGを返していない: %v", body)
	}
}

// 公開鍵が未設定なら、口ごと開かない。設定を忘れたまま公開される事故を防ぐ。
func TestDiscordClosedWithoutKey(t *testing.T) {
	srv := discordServer(t, "")
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/discord", strings.NewReader(`{"type":1}`))
	srv.Routes().ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("鍵が無いのに応答した: %d", res.Code)
	}
}
