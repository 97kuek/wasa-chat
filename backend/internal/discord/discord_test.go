package discord

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// 署名の検証。**ここを外すと誰でも叩けて、無料枠を好きなだけ消費させられる。**
// Discordは登録時にわざと壊した署名を送ってきて、拒否できるかを試す。
func TestVerify(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := hex.EncodeToString(public)
	timestamp := "1789000000"
	body := []byte(`{"type":1}`)
	// 署名の対象は「タイムスタンプ＋本文」。片方だけでは通らない
	good := hex.EncodeToString(ed25519.Sign(private, append([]byte(timestamp), body...)))

	if !Verify(key, good, timestamp, body) {
		t.Fatal("正しい署名を拒否した")
	}

	cases := []struct {
		name      string
		key       string
		signature string
		timestamp string
		body      []byte
	}{
		{"署名が空", key, "", timestamp, body},
		{"署名が壊れている", key, strings.Repeat("00", ed25519.SignatureSize), timestamp, body},
		{"署名が16進数でない", key, "ここは16進数ではない", timestamp, body},
		{"本文が差し替えられている", key, good, timestamp, []byte(`{"type":2}`)},
		{"タイムスタンプが違う", key, good, "1789000001", body},
		{"公開鍵が空", "", good, timestamp, body},
		{"公開鍵の長さが違う", hex.EncodeToString(public[:16]), good, timestamp, body},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if Verify(c.key, c.signature, c.timestamp, c.body) {
				t.Fatal("不正な署名を通した")
			}
		})
	}
}

// 利用回数を個人ごとに数えるために、誰が呼んだかが要る。
// サーバー内では member、DMでは user に入る。
func TestUserID(t *testing.T) {
	var inGuild Interaction
	inGuild.Member.User.ID = "111"
	inGuild.Member.User.Username = "部員A"
	if got := inGuild.UserID(); got != "111" {
		t.Fatalf("サーバー内の利用者を取れない: %q", got)
	}
	if got := inGuild.Username(); got != "部員A" {
		t.Fatalf("サーバー内の名前を取れない: %q", got)
	}

	var inDM Interaction
	inDM.User.ID = "222"
	if got := inDM.UserID(); got != "222" {
		t.Fatalf("DMの利用者を取れない: %q", got)
	}
}

func TestQuestion(t *testing.T) {
	var interaction Interaction
	if got := interaction.Question(); got != "" {
		t.Fatalf("空のはずが %q", got)
	}
	interaction.Data.Options = []Option{
		{Name: "質問", Type: OptionTypeString, Value: json.RawMessage(`"  荷重試験の申請は？  "`)},
	}
	if got := interaction.Question(); got != "荷重試験の申請は？" {
		t.Fatalf("前後の空白を落としていない: %q", got)
	}
}

// **出典は必ず添える。** 画面にはカードがあるがDiscordには無い。
// 「どこを開けば確かめられるか」が無い回答は、引き継ぎ資料の道具として使えない。
func TestFormatAnswerKeepsSources(t *testing.T) {
	sources := []Source{
		{Title: "荷重試験", URL: "https://wiki.example/load"},
		{Title: "構造設計", URL: ""},
	}
	got := strings.Join(FormatAnswer("荷重試験の申請は？", "新宿で申請します。", sources), "\n")

	// 行頭の `- ` でDiscordが実際の箇条書きとして描画する。中点（・）はただの文字
	for _, want := range []string{"> 荷重試験の申請は？", "新宿で申請します。", "**参照**",
		"- [荷重試験](https://wiki.example/load)", "- 構造設計"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q が入っていない:\n%s", want, got)
		}
	}
}

// **切り詰めずに続きを送る。** 出典URLは日本語タイトルのパーセントエンコードで
// 最長214文字あり、4件付くと本文に440字しか残らなかった（2026-09-13に索引で確認）
func TestFormatAnswerSplitsInsteadOfTruncating(t *testing.T) {
	// 実データの最長級URLを4件。以前はここで本文が440字まで削られていた
	sources := make([]Source, 4)
	for i := range sources {
		sources[i] = Source{
			Title: "一般設計学の観点からみた主翼構造の設計手順",
			URL:   "https://wasabirdman.sakura.ne.jp/wbwiki/w/index.php/" + strings.Repeat("%E8%A8%AD", 30),
		}
	}
	body := strings.Repeat("主翼の設計手順をここに書く。", 200) // 2800字

	messages := FormatAnswer("荷重試験の申請は？", body, sources)

	if len(messages) < 2 {
		t.Fatalf("1通に押し込んでいる: %d通", len(messages))
	}
	joined := strings.Join(messages, "")
	if strings.Contains(joined, truncatedMark) {
		t.Fatalf("収まるのに省略している:\n%s", joined[:200])
	}
	// 本文が最後まで残っていること
	if count := strings.Count(joined, "主翼の設計手順をここに書く。"); count != 200 {
		t.Fatalf("本文が欠けている: %d / 200", count)
	}
	if !strings.Contains(messages[len(messages)-1], "**参照**") {
		t.Fatal("出典が最後の通に無い")
	}
}

// それでも収まらないときは**本文を削り、出典は残す**。
// 出典を落とすと、長い回答ほど根拠が分からなくなるという逆の挙動になる。
func TestFormatAnswerTruncatesBodyNotSources(t *testing.T) {
	sources := []Source{{Title: "長い資料", URL: "https://wiki.example/long"}}
	messages := FormatAnswer("質問", strings.Repeat("あ", 20000), sources)

	if len(messages) != MaxMessagesPerAnswer {
		t.Fatalf("通数が違う: %d通", len(messages))
	}
	for i, message := range messages {
		if length := Length(message); length > MessageLimit {
			t.Fatalf("%d通目が上限を超えている: %d文字", i+1, length)
		}
	}
	last := messages[len(messages)-1]
	if !strings.Contains(last, "[長い資料](https://wiki.example/long)") {
		t.Fatalf("出典が落ちている:\n%s", last)
	}
	if !strings.Contains(last, "省略しました") {
		t.Fatal("省略したことを伝えていない")
	}
}

// ⚠️ **Discordは UTF-16 の符号単位で数える。** 絵文字は1文字で2単位になるので、
// rune 数で見積もると境界ぎりぎりの回答が弾かれる
func TestLengthCountsUTF16(t *testing.T) {
	if got := Length("あいう"); got != 3 {
		t.Fatalf("日本語の数え方が違う: %d", got)
	}
	if got := Length("🛩"); got != 2 {
		t.Fatalf("BMP外の文字を1と数えている: %d", got)
	}
	// サロゲートペアの途中で切らない（切ると壊れた文字が残る）
	if got := truncateTo("🛩🛩", 3); Length(got) != 2 || got != "🛩" {
		t.Fatalf("サロゲートペアの途中で切った: %q", got)
	}
}

// 分割は段落・行の切れ目で。文字数だけで切ると箇条書きの途中で割れる
func TestSplitMessagesBreaksOnBoundary(t *testing.T) {
	var lines []string
	for i := 0; i < 400; i++ {
		lines = append(lines, "- 主翼の設計手順をここに書く行です")
	}
	messages := splitMessages("", strings.Join(lines, "\n"), "", MessageLimit, MaxMessagesPerAnswer)

	if len(messages) < 2 {
		t.Fatalf("分割していない: %d通", len(messages))
	}
	for i, message := range messages {
		for _, line := range strings.Split(strings.TrimSuffix(message, truncatedMark), "\n") {
			if line != "" && !strings.HasPrefix(line, "- 主翼") {
				t.Fatalf("%d通目が行の途中で割れている: %q", i+1, line)
			}
		}
	}
}

func TestFormatAnswerWithoutSources(t *testing.T) {
	got := strings.Join(FormatAnswer("質問", "資料に記載がありません。", nil), "\n")
	if strings.Contains(got, "参照") {
		t.Fatalf("出典が無いのに見出しを出している:\n%s", got)
	}
}

// 質問文に @everyone が入っていても、通知を波及させない
func TestFollowUpBlocksMentions(t *testing.T) {
	follow := NewFollowUp("@everyone 危険")
	if follow.AllowedMentions.Parse == nil || len(follow.AllowedMentions.Parse) != 0 {
		t.Fatalf("メンションを許してしまう: %+v", follow.AllowedMentions)
	}
}

func TestFollowUpURL(t *testing.T) {
	got := FollowUpURL("app123", "token456")
	if !strings.Contains(got, "/webhooks/app123/token456/messages/@original") {
		t.Fatalf("書き換え先が違う: %s", got)
	}
}

// Discordは表もMermaidもレンダリングしない。画面向けの回答をそのまま流すと、
// 表は崩れ、図はただの文字列になる。**書き方の指定で避ける。**
func TestMessageLimitIsDiscordLimit(t *testing.T) {
	if MessageLimit != 2000 {
		t.Fatalf("Discordの上限と違う: %d", MessageLimit)
	}
}

// **整数のオプションを string で受けてはいけない。** 期間は `"期間": 7` と
// 数値のまま届くので、string で受けると同じリクエストの他のオプションまで消える
func TestOptionIntAndText(t *testing.T) {
	var interaction Interaction
	if err := json.Unmarshal([]byte(`{
		"type": 2,
		"data": {"name": "要約", "options": [
			{"name": "期間", "type": 4, "value": 30},
			{"name": "範囲", "type": 3, "value": "all"}
		]}
	}`), &interaction); err != nil {
		t.Fatal(err)
	}
	if got := interaction.OptionInt(OptionDays, DefaultDays); got != 30 {
		t.Fatalf("期間を読めない: %d", got)
	}
	if got := interaction.OptionText(OptionScope); got != ScopeAllChannels {
		t.Fatalf("範囲を読めない: %q", got)
	}
	// 未指定なら既定へ落ちる
	if got := interaction.OptionInt("無い名前", DefaultDays); got != DefaultDays {
		t.Fatalf("既定へ落ちない: %d", got)
	}
	if got := interaction.OptionText("無い名前"); got != "" {
		t.Fatalf("空にならない: %q", got)
	}
}

// 整数オプションが混ざっていても、質問は文字列オプションから拾えること
func TestQuestionIgnoresNonString(t *testing.T) {
	var interaction Interaction
	interaction.Data.Options = []Option{
		{Name: "期間", Type: OptionTypeInteger, Value: json.RawMessage(`7`)},
		{Name: "質問", Type: OptionTypeString, Value: json.RawMessage(`"荷重試験は？"`)},
	}
	if got := interaction.Question(); got != "荷重試験は？" {
		t.Fatalf("質問を拾えない: %q", got)
	}
}

// 画面では [1] が押せる出典マークになるが、Discordには押す先が無く、
// ただの記号として残る。実際に使って「見にくすぎる」と指摘が出た（2026-09-13）
func TestFormatAnswerStripsCitations(t *testing.T) {
	got := strings.Join(FormatAnswer("荷重試験は？",
		"新宿で申請します。[1] 期限は前日までです。[2][3]\n- 担当は設計班[1]",
		[]Source{{Title: "荷重試験", URL: "https://wiki.example/load"}}), "\n")

	if strings.Contains(got, "[1]") || strings.Contains(got, "[2][3]") {
		t.Fatalf("資料番号が残っている:\n%s", got)
	}
	if !strings.Contains(got, "新宿で申請します。 期限は前日までです。") {
		t.Fatalf("本文が壊れている:\n%s", got)
	}
	// **出典の一覧は残す。** 確かめる道まで閉じない
	if !strings.Contains(got, "[荷重試験](https://wiki.example/load)") {
		t.Fatalf("出典が消えている:\n%s", got)
	}
}

// `[見出し](URL)` を巻き込まないこと。資料本文のリンクは残す
func TestStripCitationsKeepsMarkdownLinks(t *testing.T) {
	got := stripCitations("詳細は[1](https://example.com/doc)にあります。[2]")
	if !strings.Contains(got, "[1](https://example.com/doc)") {
		t.Fatalf("リンクを壊した: %q", got)
	}
	if strings.Contains(got, "。[2]") {
		t.Fatalf("資料番号が残っている: %q", got)
	}
}

// 番号を抜いた跡に行末の空白を残さない
func TestStripCitationsTrimsTrailingSpace(t *testing.T) {
	if got := stripCitations("申請します。 [1]\n次の行"); got != "申請します。\n次の行" {
		t.Fatalf("行末が汚れている: %q", got)
	}
}

// ⚠️ **Discordはタスクリストを描画しない。** `- [ ]` は箱ではなく
// 「[ ]」という文字がそのまま出る。プロンプトでも禁じているが、
// モデルは慣れた書き方へ戻りやすいのでここでも直す
func TestFormatRecapFixesCheckboxes(t *testing.T) {
	got := strings.Join(FormatRecap(RecapScope("このチャンネル", 7, 10, 2),
		"- [ ] **部員A** / 翼型を決める / 8月まで\n  - [x] 下調べ\n* [ ] **部員B** / 発注"), "\n")

	if strings.Contains(got, "[ ]") || strings.Contains(got, "[x]") {
		t.Fatalf("タスクリスト記法が残っている:\n%s", got)
	}
	for _, want := range []string{"- ☐ **部員A** / 翼型を決める", "  - ☑ 下調べ", "- ☐ **部員B**"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q になっていない:\n%s", want, got)
		}
	}
}

// 補完は25件を超えるとDiscordに弾かれる
func TestAutocompleteCapsChoices(t *testing.T) {
	many := make([]Choice, 40)
	for i := range many {
		many[i] = Choice{Name: "#班", Value: "id"}
	}
	var payload struct {
		Type int `json:"type"`
		Data struct {
			Choices []Choice `json:"choices"`
		} `json:"data"`
	}
	if err := json.Unmarshal(Autocomplete(many), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Type != ResponseAutocomplete {
		t.Fatalf("応答の種類が違う: %d", payload.Type)
	}
	if len(payload.Data.Choices) != AutocompleteLimit {
		t.Fatalf("25件に切っていない: %d件", len(payload.Data.Choices))
	}

	// 候補ゼロでも null ではなく空配列で返す（nullはDiscordに弾かれる）
	if !strings.Contains(string(Autocomplete(nil)), `"choices":[]`) {
		t.Fatalf("空の候補が null になっている: %s", Autocomplete(nil))
	}
}

// 補完のとき、いまどのオプションを打っているかを拾えること
func TestFocused(t *testing.T) {
	var interaction Interaction
	if err := json.Unmarshal([]byte(`{
		"type": 4,
		"data": {"name": "要約", "options": [
			{"name": "期間", "type": 4, "value": 30},
			{"name": "範囲", "type": 3, "value": "きた", "focused": true}
		]}
	}`), &interaction); err != nil {
		t.Fatal(err)
	}
	name, typed := interaction.Focused()
	if name != OptionScope || typed != "きた" {
		t.Fatalf("打っている途中のオプションを拾えない: %q %q", name, typed)
	}
}
