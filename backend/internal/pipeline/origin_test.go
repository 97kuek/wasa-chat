package pipeline

import (
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/index"
)

// ⚠️ **知らない出所を「Wiki」に丸めない。** 丸めると、部外に出せない資料が
// 「Wiki」の顔をして出典に並ぶ（2026-09-13にCodexが指摘）
func TestOriginLabelDoesNotFallBackToWiki(t *testing.T) {
	for source, want := range map[string]string{
		OriginWiki: "Wiki", OriginSite: "公式サイト",
		OriginFEE: "フライトシミュレータ", OriginDrive: "共有ドライブ",
		// 旧い index.json には source が無い。当時はWikiだけだった
		"": "Wiki",
	} {
		if got := OriginLabel(source); got != want {
			t.Fatalf("%q が %q（期待 %q）", source, got, want)
		}
	}
	if got := OriginLabel("まだ知らない出所"); got == "Wiki" {
		t.Fatal("知らない出所をWikiに丸めている")
	}
	if !KnownOrigin(OriginDrive) || KnownOrigin("まだ知らない出所") {
		t.Fatal("KnownOrigin の判定が違う")
	}
}

// ⚠️ **「Wikiにある？」で Wiki 以外を通さない。** 以前は `!= "site"` と
// 書いており、出所が増えた瞬間にDriveの資料が混ざる状態だった
func TestQuestionAllowsOriginNamesOneSource(t *testing.T) {
	page := func(source string) *index.Page { return &index.Page{Source: source} }

	cases := []struct {
		question string
		allowed  []string
		blocked  []string
	}{
		{"WASA Wikiに荷重試験の記載はある？", []string{OriginWiki, ""}, []string{OriginSite, OriginFEE, OriginDrive}},
		{"公式サイトに載っている？", []string{OriginSite}, []string{OriginWiki, OriginDrive, OriginFEE}},
		{"シミュレータの操作方法は？", []string{OriginFEE}, []string{OriginWiki, OriginSite, OriginDrive}},
		{"共有ドライブに設計資料ある？", []string{OriginDrive}, []string{OriginWiki, OriginSite, OriginFEE}},
		// 出所を名指ししていない質問は絞らない（FEEだけは紛れ込ませない）
		{"荷重試験の申請方法は？", []string{OriginWiki, OriginSite, OriginDrive}, []string{OriginFEE}},
	}
	for _, c := range cases {
		t.Run(c.question, func(t *testing.T) {
			for _, source := range c.allowed {
				if !questionAllowsOrigin(c.question, page(source)) {
					t.Fatalf("%q を落とした", source)
				}
			}
			for _, source := range c.blocked {
				if questionAllowsOrigin(c.question, page(source)) {
					t.Fatalf("%q を通した", source)
				}
			}
		})
	}
}

// 両方を明記した比較質問は絞らない
func TestQuestionAllowsOriginKeepsComparisons(t *testing.T) {
	question := "WASA Wikiと公式サイトで書いてあることは違う？"
	for _, source := range []string{OriginWiki, OriginSite} {
		if !questionAllowsOrigin(question, &index.Page{Source: source}) {
			t.Fatalf("比較質問で %q を落とした", source)
		}
	}
}
