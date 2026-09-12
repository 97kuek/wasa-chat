package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/index"
	"github.com/97kuek/wasa-chat/backend/internal/llm"
	"github.com/97kuek/wasa-chat/backend/internal/pipeline"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

// askE2ELLmは意図的に誤った人物ページを選ぶ。型番の決定的検索と会話文脈が
// HTTP層から回答生成まで届かなければ、正解資料はプロンプトへ入らない。
type askE2ELLm struct {
	answerPrompt string
}

func (c *askE2ELLm) Complete(_ context.Context, req llm.Request) (string, error) {
	if strings.Contains(req.Prompt, "節の一覧") {
		return `{"ids":["p1-c0"]}`, nil
	}
	return `{"titles":["人物ページ"],"answerable":true}`, nil
}

func (c *askE2ELLm) Stream(_ context.Context, req llm.Request, onDelta llm.Delta) (string, error) {
	c.answerPrompt = req.Prompt
	onDelta("確認済み")
	return "確認済み", nil
}

func (c *askE2ELLm) Name() string { return "ask-e2e-stub" }

func askE2EIndex(t *testing.T) *index.Index {
	t.Helper()
	dir := t.TempDir()
	payload := map[string]any{"pages": []map[string]any{
		{
			"id": "1", "source": "wiki", "title": "人物ページ", "aliases": []string{},
			"url": "https://wiki.example/person", "last_edited": "2026-04-01", "team": "代まとめ・人物", "chars": 100,
			"chunks": []map[string]any{{"id": "p1-c0", "breadcrumb": "人物ページ", "text": "空力設計について相談できます。", "chars": 100}},
		},
		{
			"id": "2", "source": "wiki", "title": "空力設計", "aliases": []string{},
			"url": "https://wiki.example/aero", "last_edited": "2026-04-01", "team": "空力", "chars": 13_001,
			"chunks": []map[string]any{{
				"id": "p2-c0", "breadcrumb": "空力設計 > 循環分布",
				"text": "TR-797型分布は翼根曲げモーメントを制約した最適循環分布です。", "chars": 13_001,
			}},
		},
	}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "toc.md"), []byte("# 目次\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ix, err := index.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestAskCarriesTR797ContextFromHTTPToAnswerEvidence(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "型番の単独質問",
			body: `{"question":"TR797とは何ですか？","responseMode":"auto","context":[]}`,
		},
		{
			name: "後者を使った追質問",
			body: `{"question":"後者について詳しく教えてください。","responseMode":"auto","context":[{"question":"循環分布にはどのような種類がありますか？","answer":"完全楕円循環分布とTR-797型分布があります。"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			live := index.NewLive(askE2EIndex(t), "test")
			client := &askE2ELLm{}
			shared := state.NewMemory()
			srv := New(
				Config{SessionSecret: "テスト用の固定鍵テスト用の固定鍵", DailyLimit: 30},
				live, pipeline.New(live, client), nil, shared,
			)
			res := httptest.NewRecorder()
			srv.Routes().ServeHTTP(res, srv.testRequest(http.MethodPost, "/api/ask", tc.body, "評価利用者"))

			if res.Code != http.StatusOK {
				t.Fatalf("質問APIが失敗: %d %s", res.Code, res.Body.String())
			}
			if !strings.Contains(client.answerPrompt, "TR-797型分布は翼根曲げモーメント") {
				t.Fatal("HTTPで受け取った質問・会話から正解資料を回答生成へ渡せていない")
			}
			if !strings.Contains(res.Body.String(), `"title":"空力設計"`) ||
				!strings.Contains(res.Body.String(), `"type":"done"`) {
				t.Fatalf("出典または完了イベントがSSEへ流れていない: %s", res.Body.String())
			}
			events, err := shared.ListUsageEvents(context.Background(), 10)
			if err != nil || len(events) != 1 || events[0].Outcome != "success" || events[0].ResolvedMode == "" {
				t.Fatalf("本文なし利用監査ログを保存できていない: events=%+v err=%v", events, err)
			}
		})
	}
}

func TestValidConversationContext(t *testing.T) {
	valid := pipeline.ConversationTurn{Question: "前の質問", Answer: "前の回答"}
	cases := []struct {
		name    string
		context []pipeline.ConversationTurn
		want    bool
	}{
		{"履歴なし", nil, true},
		{"直近2往復", []pipeline.ConversationTurn{valid, valid}, true},
		{"3往復", []pipeline.ConversationTurn{valid, valid, valid}, false},
		{"質問が空", []pipeline.ConversationTurn{{Question: " ", Answer: "回答"}}, false},
		{"回答が空", []pipeline.ConversationTurn{{Question: "質問", Answer: " "}}, false},
		{"質問が長すぎる", []pipeline.ConversationTurn{{Question: strings.Repeat("問", 501), Answer: "回答"}}, false},
		{"回答が長すぎる", []pipeline.ConversationTurn{{Question: "質問", Answer: strings.Repeat("答", 2_001)}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validConversationContext(tc.context); got != tc.want {
				t.Errorf("validConversationContext() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResponseModeValidation(t *testing.T) {
	for _, valid := range []string{"", "auto", "fast", "standard", "deep"} {
		if _, ok := pipeline.ParseResponseMode(valid); !ok {
			t.Errorf("有効な回答モードを拒否した: %q", valid)
		}
	}
	if _, ok := pipeline.ParseResponseMode("gemini-3.6-flash"); ok {
		t.Fatal("利用者が生のモデルIDを指定できている")
	}
}
