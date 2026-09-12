package server

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/discord"
	"github.com/97kuek/wasa-chat/backend/internal/index"
	"github.com/97kuek/wasa-chat/backend/internal/recap"
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

// Discordは表もMermaidの図もレンダリングしない。画面向けの回答をそのまま流すと、
// 表は等幅でないフォントで崩れ、図はただの文字列になる。
func TestDiscordStyleAvoidsUnsupportedFormats(t *testing.T) {
	for _, want := range []string{"表は使わない", "図（mermaid）は使わない", "1200文字以内"} {
		if !strings.Contains(discordStyle.Instruction, want) {
			t.Fatalf("%q の指定が無い:\n%s", want, discordStyle.Instruction)
		}
	}
	// **参照範囲は絞らない。** まず全範囲で動かす（2026-09-13にPMが判断）
	if discordStyle.Origin != "" || discordStyle.Team != "" {
		t.Fatalf("参照範囲を絞っている: origin=%q team=%q", discordStyle.Origin, discordStyle.Team)
	}
}

// `/wasa この会話を要約して` は質問として流さない。会話の内容は索引に無いので
// 「記載なし」と返ってしまう。**枠を使う前に気づかせる**（送信しないので回数も減らない）
func TestAnswerForDiscordRedirectsRecapRequests(t *testing.T) {
	srv := &Server{cfg: Config{DailyLimit: 30}, state: state.NewMemory()}

	cases := []struct{ question, want string }{
		{"この会話を要約して", "/要約"},
		{"ここまでのやりとりをまとめて", "/要約"},
		{"この会話からToDoを作って", "/todo"},
		{"この会話からタスクを抜き出して", "/todo"},
	}
	for _, c := range cases {
		t.Run(c.question, func(t *testing.T) {
			got := srv.answerForDiscord(t.Context(), c.question, "u1", "部員A")
			if !strings.Contains(got, c.want) {
				t.Fatalf("%s へ案内していない: %s", c.want, got)
			}
		})
	}

	// 資料への質問は素通しする。「会話」を含むだけで奪われては困る
	used, err := srv.state.Take(t.Context(), "u1", "2026-09-13", 30)
	if err != nil || !used {
		t.Fatalf("案内だけで回数を減らしている: %v %v", used, err)
	}
}

// 要約系はBOTトークンが要る。未設定なら**枠を使わずに**そう伝える
func TestRecapForDiscordNeedsBotToken(t *testing.T) {
	srv := &Server{cfg: Config{DailyLimit: 30}, state: state.NewMemory()}
	got := srv.recapForDiscord(t.Context(), recap.KindSummary,
		recapTarget{guildID: "g1", channelID: "c1"}, "u1", "部員A")
	if !strings.Contains(got, "DISCORD_BOT_TOKEN") {
		t.Fatalf("設定不足を伝えていない: %s", got)
	}
}

// ⚠️ **候補に出したものだけが選ばれるとは限らない。**「範囲」は文字列オプション
// なので、非公開チャンネルのIDを手で打てる。実行前に必ず確かめること
func TestRecapTargetResolve(t *testing.T) {
	srv := &Server{cfg: Config{DiscordBotToken: "token"}}
	base := recapTarget{guildID: "g1", channelID: "c1"}

	// 指定なし → 打ったチャンネル
	got, refusal := base.resolve(t.Context(), srv.cfg.DiscordBotToken)
	if refusal != "" || got.channelID != "c1" || got.label != "このチャンネル" {
		t.Fatalf("既定がこのチャンネルでない: %+v %q", got, refusal)
	}

	// 横断
	all := base
	all.scope = discord.ScopeAllChannels
	got, refusal = all.resolve(t.Context(), srv.cfg.DiscordBotToken)
	if refusal != "" || !got.options.AllChannels {
		t.Fatalf("横断にならない: %+v %q", got, refusal)
	}

	// 候補に無いチャンネルID → 断る（公開かどうか確かめられないため）
	other := base
	other.scope = "非公開チャンネルのID"
	if _, refusal := other.resolve(t.Context(), srv.cfg.DiscordBotToken); refusal == "" {
		t.Fatal("確かめずに読もうとしている")
	}

	// 打ったチャンネル自身を選んだときは、公開かどうかを問わない
	self := base
	self.scope = "c1"
	if got, refusal := self.resolve(t.Context(), srv.cfg.DiscordBotToken); refusal != "" || got.channelID != "c1" {
		t.Fatalf("自分のチャンネルを拒んだ: %+v %q", got, refusal)
	}
}

// 補完は「範囲」のときだけ返す。3秒以内に同期で返るので、余計な問い合わせをしない
func TestDiscordChoicesOnlyForScope(t *testing.T) {
	srv := &Server{cfg: Config{DiscordBotToken: ""}}
	var interaction discord.Interaction
	interaction.Data.Options = []discord.Option{
		{Name: discord.OptionDays, Value: json.RawMessage(`7`), Focused: true},
	}
	if got := string(srv.discordChoices(t.Context(), &interaction)); !strings.Contains(got, `"choices":[]`) {
		t.Fatalf("期間に候補を返している: %s", got)
	}
}
