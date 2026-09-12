package discord

import (
	"crypto/ed25519"
	"encoding/hex"
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
	interaction.Data.Options = []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}{{Name: "質問", Value: "  荷重試験の申請は？  "}}
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
	got := FormatAnswer("荷重試験の申請は？", "新宿で申請します。", sources)

	for _, want := range []string{"> 荷重試験の申請は？", "新宿で申請します。", "**参照**",
		"[荷重試験](https://wiki.example/load)", "・構造設計"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q が入っていない:\n%s", want, got)
		}
	}
}

// 2000字を超えるときは**本文を削り、出典は残す**。
// 出典を落とすと、長い回答ほど根拠が分からなくなるという逆の挙動になる。
func TestFormatAnswerTruncatesBodyNotSources(t *testing.T) {
	sources := []Source{{Title: "長い資料", URL: "https://wiki.example/long"}}
	got := FormatAnswer("質問", strings.Repeat("あ", 5000), sources)

	if length := len([]rune(got)); length > MessageLimit {
		t.Fatalf("上限を超えている: %d文字", length)
	}
	if !strings.Contains(got, "[長い資料](https://wiki.example/long)") {
		t.Fatalf("出典が落ちている:\n%s", got[len(got)-200:])
	}
	if !strings.Contains(got, "省略しました") {
		t.Fatal("省略したことを伝えていない")
	}
}

func TestFormatAnswerWithoutSources(t *testing.T) {
	got := FormatAnswer("質問", "資料に記載がありません。", nil)
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
