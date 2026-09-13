package server

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/discord"
	"github.com/97kuek/wasa-chat/backend/internal/index"
	"github.com/97kuek/wasa-chat/backend/internal/recap"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

type chatListErrorStore struct{ *state.Memory }

func (s *chatListErrorStore) ListChats(context.Context, string, int) ([]state.Chat, error) {
	return nil, errors.New("履歴の読込失敗")
}

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
		{"この会話を要約して", "`/要約`"},
		{"ここまでのやりとりをまとめて", "`/要約`"},
		{"この会話からToDoを作って", "`/todo`"},
		{"この会話からタスクを抜き出して", "`/todo`"},
	}
	for _, c := range cases {
		t.Run(c.question, func(t *testing.T) {
			if got := recapRedirect(c.question); got != c.want {
				t.Fatalf("%s へ案内していない: %q", c.want, got)
			}
			// 案内は**本人だけに見える**。チャンネル全員に出す必要が無い
			reply := srv.answerForDiscord(t.Context(), c.question, "", "c1", "u1", "部員A")
			if len(reply.messages) != 0 || !strings.Contains(reply.refusal, c.want) {
				t.Fatalf("公開で出している: %+v", reply)
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
	if !strings.Contains(got.refusal, "DISCORD_BOT_TOKEN") {
		t.Fatalf("設定不足を伝えていない: %+v", got)
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

// ⚠️ **指示語を必須にしている。**「会話」を含むだけで拾っていたため、
// 普通の質問まで案内に化けていた（2026-09-13に指摘）
func TestAnswerForDiscordDoesNotHijackNormalQuestions(t *testing.T) {
	for _, question := range []string{
		"過去の会話ログの要約はWikiのどこにある？",
		"議事録のまとめ方のルールを教えて",
		"タスク管理はどうしている？",
		"スレッド強度の計算方法は？",
	} {
		t.Run(question, func(t *testing.T) {
			if got := recapRedirect(question); got != "" {
				t.Fatalf("普通の質問を %s へ案内した", got)
			}
		})
	}
}

// 履歴はチャンネルごと・利用者ごと。混ざると文脈がおかしくなる
func TestDiscordHistoryIsPerChannel(t *testing.T) {
	srv := &Server{cfg: Config{SessionSecret: "テスト用の固定鍵テスト用の固定鍵"}, state: state.NewMemory()}

	srv.saveDiscordHistory(t.Context(), "u1", "c1", "荷重試験は？", "新宿で申請します。")
	srv.saveDiscordHistory(t.Context(), "u1", "c2", "翼型は？", "NACA4412です。")

	got := srv.discordHistory(t.Context(), "u1", "c1")
	if len(got) != 1 || got[0].Question != "荷重試験は？" {
		t.Fatalf("チャンネルが混ざっている: %+v", got)
	}
	if other := srv.discordHistory(t.Context(), "u2", "c1"); len(other) != 0 {
		t.Fatalf("別の利用者の履歴が漏れている: %+v", other)
	}
}

// **際限なく伸ばさない。** 履歴は毎回プロンプトへ載る
func TestDiscordHistoryKeepsRecentTurns(t *testing.T) {
	srv := &Server{cfg: Config{SessionSecret: "テスト用の固定鍵テスト用の固定鍵"}, state: state.NewMemory()}
	for i := 0; i < 10; i++ {
		srv.saveDiscordHistory(t.Context(), "u1", "c1", fmt.Sprintf("質問%d", i), fmt.Sprintf("回答%d", i))
	}
	got := srv.discordHistory(t.Context(), "u1", "c1")
	if len(got) != discordHistoryTurns {
		t.Fatalf("履歴の数が違う: %d", len(got))
	}
	if got[len(got)-1].Question != "質問9" {
		t.Fatalf("直前のやりとりが残っていない: %+v", got)
	}
}

func TestDiscordHistoryReadFailureDoesNotOverwriteExistingHistory(t *testing.T) {
	shared := state.NewMemory()
	srv := discordServer(t, "")
	srv.state = &chatListErrorStore{Memory: shared}
	userKey := srv.userKey("discord:user")
	existing := state.Chat{
		ID: discordChatID("channel"), Title: "Discord", UpdatedAt: "2026-09-13T00:00:00Z",
		Turns: []state.Turn{{Question: "前の質問", Answer: "前の回答"}},
	}
	if err := shared.SaveChat(context.Background(), userKey, existing, maxChats); err != nil {
		t.Fatal(err)
	}

	srv.saveDiscordHistory(context.Background(), "user", "channel", "新しい質問", "新しい回答")
	chats, err := shared.ListChats(context.Background(), userKey, maxChats)
	if err != nil || len(chats) != 1 || len(chats[0].Turns) != 1 || chats[0].Turns[0].Question != "前の質問" {
		t.Fatalf("読めなかった履歴を新規チャットで上書きした: chats=%+v err=%v", chats, err)
	}
}

// アシスタントを指定すると、その指示に**Discord向けの書き方を重ねる**。
// 画面と同じものをそのまま使うと、表やMermaidを書いてきて崩れる
func TestDiscordAssistantMergesStyle(t *testing.T) {
	shared := state.NewMemory()
	srv := &Server{state: shared}
	if err := shared.CreateAssistant(t.Context(), state.Assistant{
		ID: "senpai", Name: "先輩", Instruction: "敬語は使わず、後輩に教える口調で答える。",
	}); err != nil {
		t.Fatal(err)
	}

	// 指定なしは Discord 向けの指定だけ
	got, err := srv.discordAssistant(t.Context(), "")
	if err != nil || got != discordStyle {
		t.Fatalf("既定が discordStyle でない: %+v %v", got, err)
	}

	got, err = srv.discordAssistant(t.Context(), "senpai")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Instruction, "後輩に教える口調") {
		t.Fatalf("アシスタントの指示が消えている:\n%s", got.Instruction)
	}
	if !strings.Contains(got.Instruction, "表は使わない") {
		t.Fatalf("Discord向けの書き方が重なっていない:\n%s", got.Instruction)
	}

	if _, err := srv.discordAssistant(t.Context(), "そんなIDは無い"); err == nil {
		t.Fatal("存在しないアシスタントを通した")
	}
}

// ⚠️ **リアクションでは作れない。** リアクションはGatewayでしか届かず、
// 常時接続は無料枠の約7%しかカバーできない。右クリックのメッセージコマンドは
// HTTPで届くので、ゼロスケールのまま同じことができる（2026-09-13）
func TestMessageCommandUsesTheClickedMessage(t *testing.T) {
	var interaction discord.Interaction
	if err := json.Unmarshal([]byte(`{
		"type": 2,
		"data": {
			"name": "資料に聞く", "type": 3, "target_id": "m1",
			"resolved": {"messages": {"m1": {"id": "m1", "content": "  荷重試験の申請ってどこ？  "}}}
		}
	}`), &interaction); err != nil {
		t.Fatal(err)
	}
	got, ok := interaction.TargetMessage()
	if !ok || got.Content != "  荷重試験の申請ってどこ？  " {
		t.Fatalf("右クリックしたメッセージを取れない: %+v", got)
	}
	// **中身は通知に入って届く。** 履歴を取りに行く必要が無い
	if interaction.Data.Type != discord.CommandTypeMessage {
		t.Fatalf("メッセージコマンドとして扱えていない: %d", interaction.Data.Type)
	}
}

// 右クリックでないコマンドでは、対象のメッセージは無い
func TestSlashCommandHasNoTargetMessage(t *testing.T) {
	var interaction discord.Interaction
	interaction.Data.Name = discord.CommandAsk
	if _, ok := interaction.TargetMessage(); ok {
		t.Fatal("対象のメッセージがあることになっている")
	}
}
