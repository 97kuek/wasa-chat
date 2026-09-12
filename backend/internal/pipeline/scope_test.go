package pipeline

import (
	"slices"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/index"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

// 参照範囲の絞り込みはGo側で決定的に効かせる。プロンプトで「公式サイトだけ見て」と
// 頼む方式は書き忘れと無視で破れるが、ここで落とせば構造的に混ざらない。
// 「公式サイトのみ」は部外に出せる情報だけに限る用途で使うため、ここが破れると実害が出る。
func TestInScope(t *testing.T) {
	wiki := &index.Page{Title: "電装班", Source: "wiki", Team: "電装"}
	site := &index.Page{Title: "WASAについて知る", Source: "site"}
	// 旧い index.json には source が無い。既定をwikiに寄せないと、
	// 「公式サイトのみ」の絞り込みに部内資料がすり抜ける
	legacy := &index.Page{Title: "古いページ", Source: "", Team: "翼"}

	cases := []struct {
		name      string
		page      *index.Page
		assistant *state.Assistant
		want      bool
	}{
		{"未選択なら全部通す", wiki, nil, true},
		{"絞り込み無しなら全部通す", wiki, &state.Assistant{}, true},
		{"公式サイトのみ: 公式は通す", site, &state.Assistant{Origin: "site"}, true},
		{"公式サイトのみ: Wikiは落とす", wiki, &state.Assistant{Origin: "site"}, false},
		{"公式サイトのみ: source未設定は落とす", legacy, &state.Assistant{Origin: "site"}, false},
		{"Wikiのみ: 公式は落とす", site, &state.Assistant{Origin: "wiki"}, false},
		{"Wikiのみ: source未設定は通す", legacy, &state.Assistant{Origin: "wiki"}, true},
		{"班: 一致すれば通す", wiki, &state.Assistant{Team: "電装"}, true},
		{"班: 違えば落とす", wiki, &state.Assistant{Team: "翼"}, false},
		{"班: 班なしページは落とす", site, &state.Assistant{Team: "翼"}, false},
		{"出所と班の両方", wiki, &state.Assistant{Origin: "wiki", Team: "電装"}, true},
		{"片方でも外れたら落とす", wiki, &state.Assistant{Origin: "site", Team: "電装"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inScope(c.page, c.assistant); got != c.want {
				t.Errorf("inScope() = %v, want %v", got, c.want)
			}
		})
	}
}

// 保険の経路（fallbackPages）だけ規則が緩んでいた。
//
// ほかの候補は add() を通るので questionAllowsOrigin が効くが、fallbackPages の
// 結果はそのまま使われるため、抜けていると「公式サイトに載っていますか」に
// 引き継ぎWikiのページを返せてしまう（2026-09-12に発見）。
// **救済経路だけ緩む形を残さない。**
func TestFallbackPagesKeepsOriginFilter(t *testing.T) {
	ix := &index.Index{Pages: []index.Page{
		{Title: "作業場情報", Source: "wiki", Chunks: []index.Chunk{{ID: "p1-c1", Breadcrumb: "作業場情報 > 家賃"}}},
		{Title: "作業場のしょうかい", Source: "site", Chunks: []index.Chunk{{ID: "p2-c1", Breadcrumb: "作業場のしょうかい > 家賃"}}},
	}}
	p := New(ix, nil)

	cases := []struct {
		name     string
		question string
		want     []string
	}{
		{"出所を指定しなければ両方出る", "作業場の家賃は？", []string{"作業場情報", "作業場のしょうかい"}},
		{"公式サイトを指定したらWikiは出さない", "公式サイトに作業場の家賃はありますか", []string{"作業場のしょうかい"}},
		{"Wikiを指定したら公式サイトは出さない", "Wikiに作業場の家賃はありますか", []string{"作業場情報"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, pg := range p.fallbackPages(c.question, nil) {
				got = append(got, pg.Title)
			}
			if len(got) != len(c.want) {
				t.Fatalf("%v が返った。想定は %v", got, c.want)
			}
			for _, want := range c.want {
				if !slices.Contains(got, want) {
					t.Fatalf("%v に %q が無い", got, want)
				}
			}
		})
	}
}
