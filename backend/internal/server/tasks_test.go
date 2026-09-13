package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/state"
)

func taskServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		cfg:   Config{SessionSecret: "テスト用の固定鍵テスト用の固定鍵", DailyLimit: 30},
		state: state.NewMemory(),
	}
}

// ⚠️ **この口は公開される。** 署名を見ないと誰でも叩けて、無料枠を消費させられる
func TestDiscordWorkNeedsSignature(t *testing.T) {
	srv := taskServer(t)
	body, _ := json.Marshal(discordJob{Command: "wasa", Question: "荷重試験は？"})

	for _, c := range []struct{ name, signature string }{
		{"署名なし", ""},
		{"でたらめな署名", "AAAA"},
		{"別の本文の署名", srv.signJob([]byte(`{"command":"別"}`))},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/discord/work", strings.NewReader(string(body)))
			req.Header.Set(discordJobSignature, c.signature)
			res := httptest.NewRecorder()
			srv.handleDiscordWork(res, req)
			if res.Code != http.StatusUnauthorized {
				t.Fatalf("不正な署名を通した: %d", res.Code)
			}
		})
	}
}

// ⚠️ **必ず 200 を返す。** 2xx以外だと Cloud Tasks が再試行し、
// 同じ質問でLLMを何度も呼ぶ。無料枠を溶かすうえ、Discordのトークンは
// 15分で失効するので再試行に意味が無い
func TestDiscordWorkAlwaysAcceptsValidJobs(t *testing.T) {
	srv := taskServer(t)
	// 解釈できない中身。再試行しても直らないので 200 で捨てる
	broken := []byte(`{"command": 12345}`)
	req := httptest.NewRequest(http.MethodPost, "/api/discord/work", strings.NewReader(string(broken)))
	req.Header.Set(discordJobSignature, srv.signJob(broken))
	res := httptest.NewRecorder()
	srv.handleDiscordWork(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("再試行させてしまう: %d", res.Code)
	}
}

// キューが未設定なら積まない（goroutine で動く経路へ落ちる）
func TestEnqueueSkippedWithoutQueue(t *testing.T) {
	srv := taskServer(t)
	if srv.enqueueDiscordJob(t.Context(), discordJob{}) {
		t.Fatal("設定が無いのに積んだことになっている")
	}
	srv.cfg.TasksQueue = "projects/p/locations/l/queues/q"
	if srv.enqueueDiscordJob(t.Context(), discordJob{}) {
		t.Fatal("戻り先のURLが無いのに積んだことになっている")
	}
}

// 署名は本文ごとに変わる（使い回せない）
func TestJobSignatureBindsToBody(t *testing.T) {
	srv := taskServer(t)
	one := srv.signJob([]byte(`{"question":"A"}`))
	two := srv.signJob([]byte(`{"question":"B"}`))
	if one == two {
		t.Fatal("本文が違うのに同じ署名になる")
	}
	if !srv.verifyJob([]byte(`{"question":"A"}`), one) {
		t.Fatal("正しい署名を拒んだ")
	}
	if srv.verifyJob([]byte(`{"question":"B"}`), one) {
		t.Fatal("別の本文の署名を通した")
	}
}
