package discord

import (
	"slices"
	"testing"
)

// ⚠️ **質問文をそのまま渡すと必ず0件になる。** Discordの content は語の一致で
// 探すため。さらに**1つのクエリに語を並べるとAND条件**になり、急に当たらなくなる。
// 同じサーバーでの実測（2026-09-13、docs/08 M64）:
//
//	申請 16件       申請方法 1件
//	荷重試験 21件    「荷重試験 申請」1件
//	チャンネル 7件   「チャンネル 名」0件
//
// そこで**塊に割って、1語ずつ別のクエリで投げる**。先頭は特徴的な語（長い順）。
func TestSearchTermsLeadWithTheSpecificWord(t *testing.T) {
	cases := []struct{ question, want string }{
		{"Discordにはどんな内容が書かれている？チャンネル名とか色々教えて", "チャンネル"},
		{"翼型の設計はどうやって決めた？", "翼型"},
		{"荷重試験の申請方法を教えてください", "荷重試験"},
		{"TR797の諸元は？", "TR797"},
		{"テストフライトの配車はどうする", "テストフライト"},
		// 数字だけの塊は単独では当たりすぎる。「40」ではなく「代表」を先に使う
		{"40代の代表は誰ですか", "代表"},
	}
	for _, c := range cases {
		t.Run(c.question, func(t *testing.T) {
			got := SearchTermsFor(c.question, "")
			if len(got) == 0 || got[0] != c.want {
				t.Fatalf("%q → %v（先頭は %q のはず）", c.question, got, c.want)
			}
			// **1クエリ1語。** 空白で区切ると AND になって当たらなくなる
			for _, term := range got {
				for _, r := range term {
					if r == ' ' {
						t.Fatalf("1つのクエリに語を並べている: %q", term)
					}
				}
			}
		})
	}
}

// 1文字は当たりすぎるので普通は後回しだが、「桁」のような部材名もある。
// 他に候補が無ければ使う
func TestSearchTermsFallBackToSingleRune(t *testing.T) {
	if got := SearchTermsFor("桁は？", ""); len(got) != 1 || got[0] != "桁" {
		t.Fatalf("1文字の部材名を落とした: %v", got)
	}
	// ほかに長い候補があればそちらが先。1文字は後ろへ回す
	if got := SearchTermsFor("桁の設計は？", ""); len(got) == 0 || got[0] != "設計" {
		t.Fatalf("長いほうを先にしていない: %v", got)
	}
}

// 語が取れない質問は、投げても0件なので検索しない
func TestSearchTermsEmptyWhenNoTerm(t *testing.T) {
	for _, question := range []string{"どんな内容が書かれていますか", "教えて", "ください", "？？？"} {
		if got := SearchTermsFor(question, ""); len(got) != 0 {
			t.Fatalf("%q から %v を取り出した", question, got)
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

// 短い質問（「最近のは？」）は指示語だけで、そのままでは当たらない語になる。
// 前の質問の語を足す（2026-09-13に本番で、「荷重試験の計画書ってある？」→
// 「最近のは？」が別の話になった）
func TestSearchTermsForFollowUp(t *testing.T) {
	for _, c := range []struct{ question, previous, want string }{
		{"最近のは？", "荷重試験の過去の計画書ってある？", "荷重試験"},
		{"他には？", "翼型の設計はどうやって決めた？", "翼型"},
		// 前の質問が無ければ、いままでどおり
		{"翼型の設計は？", "", "翼型"},
	} {
		if got := SearchTermsFor(c.question, c.previous); len(got) == 0 || got[0] != c.want {
			t.Fatalf("%q（前: %q）→ %v（先頭は %q のはず）", c.question, c.previous, got, c.want)
		}
	}
	// 短い質問では前の語を足すが、**自分の語も捨てない。** 語ごとに別の
	// クエリを投げるので、両方探せば済む（1語を選ぶ必要はもう無い）
	got := SearchTermsFor("プロペラは？", "最近どう？")
	if !slices.Contains(got, "プロペラ") {
		t.Fatalf("自分の語を捨てている: %v", got)
	}
}

// ⚠️ **1つのクエリに語を並べない（AND条件で当たらなくなる）。**
// 代わりにクエリを分けて束ねる。1語だけだと拾える範囲が狭く、
// 「Discordを見ている感じがしない」という指摘が出た（2026-09-13）
func TestSearchTermsForReturnsSeveral(t *testing.T) {
	got := SearchTermsFor("荷重試験の申請方法を教えてください", "")
	if len(got) < 2 {
		t.Fatalf("語が1つしか返っていない: %v", got)
	}
	// 特徴的な順（長い順）
	if got[0] != "荷重試験" {
		t.Fatalf("いちばん特徴的な語が先頭でない: %v", got)
	}
	if len(got) > MaxQueries {
		t.Fatalf("クエリが多すぎる: %v", got)
	}
}

// 短い追加質問では、前の質問の語を**先頭に**置く（そちらが本題）
func TestSearchTermsForFollowUpLeadsWithPrevious(t *testing.T) {
	got := SearchTermsFor("最近のは？", "荷重試験の過去の計画書ってある？")
	if len(got) == 0 || got[0] != "荷重試験" {
		t.Fatalf("前の質問の語が先頭でない: %v", got)
	}
	// 長い質問では前の質問を見ない（話題の変更を引きずらない）
	fresh := SearchTermsFor("プロペラの製作手順を詳しく教えてください", "荷重試験の計画書は？")
	for _, term := range fresh {
		if term == "荷重試験" {
			t.Fatalf("話題の変更を引きずっている: %v", fresh)
		}
	}
}

// 同じ語を2回投げない（リクエストの無駄）
func TestSearchTermsForDeduplicates(t *testing.T) {
	got := SearchTermsFor("翼型は？", "翼型の設計について")
	seen := map[string]bool{}
	for _, term := range got {
		if seen[term] {
			t.Fatalf("同じ語が2回入っている: %v", got)
		}
		seen[term] = true
	}
}

func TestSearchTermsForEmpty(t *testing.T) {
	if got := SearchTermsFor("教えて", ""); len(got) != 0 {
		t.Fatalf("語が無いのに返している: %v", got)
	}
}
