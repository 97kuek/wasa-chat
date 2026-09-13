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

// 短い質問（「最近のは？」）は指示語だけで、そのままでは当たらない語になる。
// 前の質問のほうが具体的ならそちらを使う（2026-09-13に本番で、
// 「荷重試験の計画書ってある？」→「最近のは？」が別の話になった）
func TestSearchQueryForFollowUp(t *testing.T) {
	if got := SearchQueryFor("最近のは？", "荷重試験の過去の計画書ってある？"); got != "荷重試験" {
		t.Fatalf("前の質問を見ていない: %q", got)
	}
	if got := SearchQueryFor("他には？", "翼型の設計はどうやって決めた？"); got != "翼型" {
		t.Fatalf("前の質問を見ていない: %q", got)
	}
	// **長い質問では前の質問を見ない。** 話題が変わったときに引きずると、
	// 関係のない会話を根拠として渡すことになる
	if got := SearchQueryFor("プロペラの製作手順を詳しく教えてください", "荷重試験の計画書は？"); got != "プロペラ" {
		t.Fatalf("話題の変更を引きずっている: %q", got)
	}
	// 前の質問が無ければ、いままでどおり
	if got := SearchQueryFor("翼型の設計は？", ""); got != "翼型" {
		t.Fatalf("単独の質問が壊れた: %q", got)
	}
	// 短い質問でも、自分の語のほうが具体的ならそちらを使う
	if got := SearchQueryFor("プロペラは？", "最近どう？"); got != "プロペラ" {
		t.Fatalf("自分の語を捨てている: %q", got)
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
