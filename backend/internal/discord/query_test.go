package discord

import "testing"

// ⚠️ **質問文をそのまま渡すと必ず0件になる。** Discordの content は語の一致で
// 探すため。さらに**語を複数渡すとAND条件**になり、急に当たらなくなる。
// 同じサーバーでの実測（2026-09-13、docs/08 M64）:
//
//	申請 16件 / 申請方法 1件 / 荷重試験 21件 / 「荷重試験 申請」1件
//
// そこで「いちばん長い塊を1つ」だけ使う。
func TestSearchQueryPicksOneSpecificTerm(t *testing.T) {
	cases := []struct{ question, want string }{
		{"Discordにはどんな内容が書かれている？チャンネル名とか色々教えて", "チャンネル"},
		{"翼型の設計はどうやって決めた？", "翼型"},
		{"荷重試験の申請方法を教えてください", "荷重試験"},
		{"TR797の諸元は？", "TR797"},
		{"テストフライトの配車はどうする", "テストフライト"},
		// 数字だけの塊は単独では当たりすぎる。「40」ではなく「代表」を選ぶ
		{"40代の代表は誰ですか", "代表"},
	}
	for _, c := range cases {
		t.Run(c.question, func(t *testing.T) {
			got := SearchQuery(c.question)
			if got != c.want {
				t.Fatalf("%q → %q（期待 %q）", c.question, got, c.want)
			}
			// **1語だけ。** 空白で区切ると AND になって当たらなくなる
			for _, r := range got {
				if r == ' ' {
					t.Fatalf("語を複数返している: %q", got)
				}
			}
		})
	}
}

// 1文字は当たりすぎるので普通は使わないが、「桁」のような部材名もある。
// 他に候補が無ければ使う
func TestSearchQueryFallsBackToSingleRune(t *testing.T) {
	if got := SearchQuery("桁は？"); got != "桁" {
		t.Fatalf("1文字の部材名を落とした: %q", got)
	}
	// ほかに長い候補があればそちらを使う
	if got := SearchQuery("桁の設計は？"); got != "設計" {
		t.Fatalf("長いほうを選んでいない: %q", got)
	}
}

// 語が取れない質問は、投げても0件なので検索しない
func TestSearchQueryEmptyWhenNoTerm(t *testing.T) {
	for _, question := range []string{"どんな内容が書かれていますか", "教えて", "ください", "？？？"} {
		if got := SearchQuery(question); got != "" {
			t.Fatalf("%q から %q を取り出した", question, got)
		}
	}
}

// ひらがなだけの塊は助詞・語尾なので落とす
func TestSplitQuestionDropsHiragana(t *testing.T) {
	got := splitQuestion("翼型の設計はどこ")
	if len(got) != 2 || got[0] != "翼型" || got[1] != "設計" {
		t.Fatalf("区切り方が違う: %v", got)
	}
}
