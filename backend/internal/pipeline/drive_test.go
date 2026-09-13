package pipeline

import (
	"strings"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/index"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

// ⚠️ **共有ドライブは既定で読まない。**「+」でオンにした会話だけ。
// プロンプトの書き方ではなくここで落とすので、構造的に混ざらない
func TestDriveIsOffByDefault(t *testing.T) {
	page := &index.Page{Title: "議事録", Source: OriginDrive}

	if inScope(page, Scope{}) {
		t.Fatal("オフなのに共有ドライブを読んでいる")
	}
	if !inScope(page, Scope{Drive: true}) {
		t.Fatal("オンにしても共有ドライブを読まない")
	}
	// ほかの出所は「+」に関係なく読む
	if !inScope(&index.Page{Source: OriginWiki}, Scope{}) {
		t.Fatal("Wikiまで落としている")
	}
}

// **知らない出所は読まない。** 索引に新しい出所が入ったとき、
// どこにも許可を書かないまま回答へ混ざるのを防ぐ
func TestUnknownOriginIsNotRead(t *testing.T) {
	if inScope(&index.Page{Source: "まだ知らない出所"}, Scope{Drive: true}) {
		t.Fatal("知らない出所を読んでいる")
	}
	// 旧い index.json には source が無い。当時はWikiだけだった
	if !inScope(&index.Page{Source: ""}, Scope{}) {
		t.Fatal("古い索引を読めなくしている")
	}
}

// 実効範囲 = アシスタントが許す最大範囲 ∩ この会話で足した参照先。
// **足すより狭めるほうが強い**（docs/09 D-5「参照範囲は緩めない」）
func TestAssistantScopeBeatsToolToggle(t *testing.T) {
	siteOnly := Scope{Assistant: &state.Assistant{Origin: OriginSite}, Drive: true}

	if inScope(&index.Page{Source: OriginDrive}, siteOnly) {
		t.Fatal("「公式サイトのみ」のアシスタントで共有ドライブを読んでいる")
	}
	if !inScope(&index.Page{Source: OriginSite}, siteOnly) {
		t.Fatal("公式サイトまで落としている")
	}
}

// 画面から届いた名前を範囲へ落とす。知らない名前は黙って捨てる
func TestNewScope(t *testing.T) {
	if got := NewScope(nil, []string{ToolDrive}); !got.Drive {
		t.Fatal("drive を拾えていない")
	}
	if got := NewScope(nil, []string{"まだ知らない道具"}); got.Drive {
		t.Fatal("知らない名前で範囲が変わった")
	}
	if got := NewScope(nil, nil); got.Drive {
		t.Fatal("指定なしでオンになっている")
	}
}

// ⚠️ **共有ドライブの目次を固定プレフィックスに入れない。**
// ファイルが1つ増減するたびにキャッシュが外れ、オフの会話へも漏れる
func TestDriveTOCStaysOutOfCachedPrefix(t *testing.T) {
	ix := &index.Index{TOC: "# WASA 資料の目次\n## 引き継ぎWiki（部内限定）\n- 荷重試験\n",
		DriveTOC: "## 共有ドライブ（部内限定）全3ファイル\n- 40代 総会議事録\n"}

	if strings.Contains(ix.TOC, "共有ドライブ") {
		t.Fatal("固定プレフィックスに共有ドライブが混ざっている")
	}
	if got := driveTOCSection(ix, Scope{}); got != "" {
		t.Fatalf("オフなのに目次を足している: %q", got)
	}
	if got := driveTOCSection(ix, Scope{Drive: true}); !strings.Contains(got, "40代 総会議事録") {
		t.Fatalf("オンなのに目次を足していない: %q", got)
	}
	// アシスタントが範囲を絞っているなら足さない
	siteOnly := Scope{Assistant: &state.Assistant{Origin: OriginSite}, Drive: true}
	if got := driveTOCSection(ix, siteOnly); got != "" {
		t.Fatalf("「公式サイトのみ」に共有ドライブの目次を足している: %q", got)
	}
}

// ⚠️ **選んだ資料と、使った資料は違う。** 4件選んで1件しか引用しないことは
// 普通に起きる。全部を「参照」に並べると、回答が「記載がありません」と
// 言っているのに参照だけ並ぶ食い違いが出る（2026-09-13に本番で指摘）
func TestMarkUsedSources(t *testing.T) {
	sources := []Source{
		{Title: "荷重試験", Origin: OriginWiki},
		{Title: "構造設計", Origin: OriginWiki},
		{Title: "作業場移転ログ", Origin: OriginWiki},
	}
	markUsedSources(sources, "新宿で申請します。[1] 期限は前日まで。[3]", Scope{})

	if !sources[0].Used || sources[2].Used {
		// [3] は3件目。番号は1始まり
	}
	for i, want := range []bool{true, false, true} {
		if sources[i].Used != want {
			t.Fatalf("%d件目の印が違う: %+v", i+1, sources[i])
		}
	}
}

// ⚠️ **並び順を変えたり間引いたりしない。** 画面は [3] を sources[2] として
// 解くので、詰めると別の資料を指す
func TestMarkUsedSourcesKeepsOrder(t *testing.T) {
	sources := []Source{{Title: "あ"}, {Title: "い"}, {Title: "う"}}
	markUsedSources(sources, "[2]", Scope{})
	if len(sources) != 3 || sources[1].Title != "い" {
		t.Fatalf("並びが変わった: %+v", sources)
	}
	// 範囲外の番号を書かれても落ちない
	markUsedSources(sources, "[99][0][abc]", Scope{})
}

// Discordの会話は番号を持たない。渡したなら使った扱いにする
func TestMarkUsedSourcesKeepsDiscord(t *testing.T) {
	sources := []Source{{Title: "荷重試験", Origin: OriginWiki}, {Title: "#雑談", Origin: ToolDiscord}}
	markUsedSources(sources, "資料に記載がありません。", Scope{DiscordLog: "部員A: 何か"})
	if sources[0].Used {
		t.Fatal("引用されていない資料に印が付いた")
	}
	if !sources[1].Used {
		t.Fatal("読んだDiscordの会話が参照から消える")
	}

	// 会話を渡していないなら印を付けない
	clean := []Source{{Title: "#雑談", Origin: ToolDiscord}}
	markUsedSources(clean, "", Scope{})
	if clean[0].Used {
		t.Fatal("読んでいないのに印が付いた")
	}
}
