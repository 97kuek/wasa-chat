package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/index"
	"github.com/97kuek/wasa-chat/backend/internal/llm"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

// stubLLM はページ選択に固定の答えを返し、回答生成のプロンプトを記録する。
// 本物のモデルを呼ばずに「何を渡しているか」だけを検証したい。
type stubLLM struct {
	titles        []string
	chunkIDs      []string // 節の絞り込みで返させるID。空なら選ばせない
	lastAnswer    string   // 回答生成に渡った Prompt
	lastCached    string   // 同 Cached（目次はこちらに載る）
	lastSelect    string   // ページ選択に渡った Prompt
	lastSystem    string   // 回答生成に渡った System
	selectProfile llm.Profile
	chunkProfile  llm.Profile
	answerProfile llm.Profile
}

func (s *stubLLM) Complete(_ context.Context, req llm.Request) (string, error) {
	if strings.Contains(req.Prompt, "節の一覧") {
		s.chunkProfile = req.Profile
		out, _ := json.Marshal(map[string]any{"ids": s.chunkIDs})
		return string(out), nil
	}
	s.lastSelect = req.Prompt
	s.selectProfile = req.Profile
	out, _ := json.Marshal(map[string]any{"titles": s.titles, "answerable": true})
	return string(out), nil
}

func (s *stubLLM) Stream(_ context.Context, req llm.Request, onDelta llm.Delta) (string, error) {
	s.lastAnswer = req.Prompt
	s.lastCached = req.Cached
	s.lastSystem = req.System
	s.answerProfile = req.Profile
	onDelta("（ダミー回答）")
	return "（ダミー回答）", nil
}

func (s *stubLLM) Name() string { return "stub" }

// テスト用の小さな索引を書き出して読み込む。
func testIndex(t *testing.T) *index.Index {
	t.Helper()
	return testIndexWithTOC(t, "# 目次\n")
}

// testIndexWithTOC は目次だけ差し替えた索引を作る。出所ごとの分割を試すため。
func testIndexWithTOC(t *testing.T, toc string) *index.Index {
	t.Helper()
	dir := t.TempDir()
	payload := map[string]any{"pages": []map[string]any{
		{
			"id": "1", "source": "wiki", "title": "電装班", "url": "https://wiki.example/dens",
			"last_edited": "2026-04-01", "team": "電装", "chars": 100,
			"chunks": []map[string]any{
				{"id": "p1-c0", "breadcrumb": "電装班", "text": "計測系統のはなし", "chars": 8,
					"era": map[string]any{"years": []int{2024}, "gens": []int{40}}},
			},
		},
		{
			"id": "s1", "source": "site", "title": "WASAについて知る", "url": "https://wasa-birdman.com/about",
			"last_edited": "2026-04-01", "team": "", "chars": 100,
			"chunks": []map[string]any{
				{"id": "ps1-c0", "breadcrumb": "WASAについて知る", "text": "WASAは1965年に設立された。", "chars": 14,
					"era": map[string]any{"years": []int{1965}, "gens": []int{}}},
			},
		},
	}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "toc.md"), []byte(toc), 0o600); err != nil {
		t.Fatal(err)
	}
	ix, err := index.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// 型番検索のテスト用。質問はハイフン無し、本文はハイフン有りにして、
// 目次選択だけでは再現できなかったTR797の表記揺れを固定する。
func testIdentifierIndex(t *testing.T) *index.Index {
	t.Helper()
	dir := t.TempDir()
	payload := map[string]any{"pages": []map[string]any{
		{
			"id": "1", "source": "wiki", "title": "人物ページ", "url": "https://wiki.example/person",
			"last_edited": "2026-04-01", "team": "代まとめ・人物", "chars": 100,
			"chunks": []map[string]any{
				{"id": "p1-c0", "breadcrumb": "人物ページ", "text": "空力設計について相談できます。", "chars": 15},
			},
		},
		{
			"id": "2", "source": "wiki", "title": "空力設計", "url": "https://wiki.example/aero",
			"last_edited": "2026-04-01", "team": "空力", "chars": 13001,
			"chunks": []map[string]any{
				{"id": "p2-c0", "breadcrumb": "空力設計 > 循環分布", "text": "TR-797型分布は翼根曲げモーメントを制約した最適循環分布です。", "chars": 13001},
			},
		},
	}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "toc.md"), []byte("# 目次\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ix, err := index.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// タイトル一致の世代変換と優先順位を、LLMの出力と切り離して試す。
func testDirectTitleIndex(t *testing.T) *index.Index {
	t.Helper()
	dir := t.TempDir()
	type pageSpec struct {
		title string
		team  string
	}
	specs := []pageSpec{
		{title: "空力設計", team: "空力"},
		{title: "40代", team: "代まとめ・人物"},
		{title: "空力設計(40th)", team: "空力"},
		{title: "空力設計(41st)", team: "空力"},
		{title: "HPA交流会", team: "全体"},
		{title: "PM", team: "全体"},
		{title: "人物ページ", team: "代まとめ・人物"},
	}
	pages := make([]map[string]any, 0, len(specs))
	for i, spec := range specs {
		id := fmt.Sprintf("p%d-c0", i+1)
		pages = append(pages, map[string]any{
			"id": fmt.Sprintf("%d", i+1), "source": "wiki", "title": spec.title,
			"url": "https://wiki.example/" + id, "last_edited": "2026-04-01",
			"team": spec.team, "chars": 20,
			"chunks": []map[string]any{{
				"id": id, "breadcrumb": spec.title, "text": spec.title + "の本文", "chars": 20,
			}},
		})
	}
	raw, err := json.Marshal(map[string]any{"pages": pages})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "toc.md"), []byte("# 目次\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ix, err := index.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func run(t *testing.T, assistant *state.Assistant) (*stubLLM, []Source, string) {
	t.Helper()
	// モデルは毎回「両方のページを使いたい」と答える。絞り込みが効いていなければ
	// 両方が文脈に入るので、範囲外が確実に落ちているかを見分けられる
	client := &stubLLM{titles: []string{"電装班", "WASAについて知る"}}
	pipe := New(index.NewLive(testIndex(t), "test"), client)

	var sources []Source
	var answer strings.Builder
	err := pipe.Run(context.Background(), "WASAについて教えて", nil, assistant, func(e Event) {
		switch e.Type {
		case "pages":
			sources = e.Pages
		case "delta":
			answer.WriteString(e.Text)
		}
	})
	if err != nil {
		t.Fatalf("Run が失敗: %v", err)
	}
	return client, sources, answer.String()
}

// 「公式サイトのみ」は部外に出せる情報だけに限る用途で使う。
// ここが破れると、部内資料が対外向けの回答に混ざる。
func TestRunScopeExcludesOtherOrigin(t *testing.T) {
	client, sources, _ := run(t, &state.Assistant{
		Name: "対外説明", Instruction: "ですます調で書く", Origin: "site",
	})

	if len(sources) != 1 || sources[0].Origin != "site" {
		t.Fatalf("出典に範囲外が残った: %+v", sources)
	}
	// 出典パネルだけでなく、モデルに渡す本文からも消えていること
	if strings.Contains(client.lastAnswer, "計測系統のはなし") {
		t.Error("範囲外のWiki本文がプロンプトへ入った")
	}
	if !strings.Contains(client.lastAnswer, "WASAは1965年に設立された。") {
		t.Error("範囲内の本文がプロンプトへ入っていない")
	}
}

func TestRunScopeByTeam(t *testing.T) {
	_, sources, _ := run(t, &state.Assistant{Name: "電装班", Instruction: "簡潔に", Team: "電装"})
	if len(sources) != 1 || sources[0].Title != "電装班" {
		t.Fatalf("班の絞り込みが効いていない: %+v", sources)
	}
}

func TestRunWithoutAssistantKeepsEverything(t *testing.T) {
	_, sources, _ := run(t, nil)
	if len(sources) != 2 {
		t.Fatalf("未選択なのに資料が減った: %+v", sources)
	}
}

func TestRunUsesRecentConversationWithoutTreatingItAsEvidence(t *testing.T) {
	client := &stubLLM{titles: []string{"電装班"}}
	pipe := New(index.NewLive(testIndex(t), "test"), client)
	history := []ConversationTurn{{
		Question: "ESP32について教えて", Answer: "以前の回答には誤りがあるかもしれません。",
	}}

	if err := pipe.Run(context.Background(), "それのピン配置は？", history, nil, func(Event) {}); err != nil {
		t.Fatalf("Run が失敗: %v", err)
	}
	if !strings.Contains(client.lastSelect, "ESP32について教えて") {
		t.Fatal("ページ選択に直近の会話が渡っていない")
	}
	if !strings.Contains(client.lastAnswer, "以前の回答: 以前の回答には誤り") ||
		!strings.Contains(client.lastAnswer, "事実の根拠にせず") ||
		!strings.Contains(client.lastAnswer, "対象が一つに定まるなら、その対象を直接解説") {
		t.Fatal("回答生成に会話の位置づけが明示されていない")
	}
}

func TestRunResolvesTailDesignFollowUp(t *testing.T) {
	client := &stubLLM{titles: []string{"電装班"}}
	pipe := New(index.NewLive(testIndex(t), "test"), client)
	history := []ConversationTurn{
		{Question: "空力設計の手順を説明して", Answer: "主翼の設計手順を説明しました。"},
		{Question: "尾翼設計について書かれていないようですが", Answer: "尾翼設計では飛行力学の式を理解します。"},
	}
	if err := pipe.Run(context.Background(), "それについて解説して", history, nil, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.lastSelect, "尾翼設計について書かれていない") ||
		!strings.Contains(client.lastAnswer, "現在の質問") {
		t.Fatal("直前の尾翼設計を検索と回答へ引き継いでいない")
	}
}

func TestResponseModeUsesDifferentProfilesByStage(t *testing.T) {
	client := &stubLLM{titles: []string{"電装班"}}
	pipe := New(index.NewLive(testIndex(t), "test"), client)
	var resolved string
	if err := pipe.RunWithMode(context.Background(), "電装班について教えて", nil, nil, ModeStandard, func(event Event) {
		if event.Type == "mode" {
			resolved = event.Mode
		}
	}); err != nil {
		t.Fatalf("RunWithMode が失敗: %v", err)
	}
	if resolved != string(ModeStandard) {
		t.Fatalf("解決したモードが画面へ流れていない: %q", resolved)
	}
	if client.selectProfile != llm.ProfileStandard || client.answerProfile != llm.ProfileFast {
		t.Fatalf("標準モードの段階別設定が不正: select=%q answer=%q", client.selectProfile, client.answerProfile)
	}
}

func TestRunEmitsStageTimings(t *testing.T) {
	client := &stubLLM{titles: []string{"電装班"}}
	pipe := New(index.NewLive(testIndex(t), "test"), client)
	var stages []string
	timings := map[string]int64{}

	if err := pipe.Run(context.Background(), "電装班について教えて", nil, nil, func(event Event) {
		if event.Type == "timing" {
			stages = append(stages, event.Stage)
			timings[event.Stage] = event.Millis
		}
	}); err != nil {
		t.Fatalf("Run が失敗: %v", err)
	}
	want := []string{"pages", "chunks", "answer", "total"}
	if strings.Join(stages, ",") != strings.Join(want, ",") {
		t.Fatalf("計測段階または順序が不正: got=%v want=%v", stages, want)
	}
	for _, stage := range want {
		if timings[stage] < 1 {
			t.Errorf("%sの所要時間が記録されていない: %d", stage, timings[stage])
		}
	}
	if timings["total"] < timings["pages"] || timings["total"] < timings["answer"] {
		t.Fatalf("合計時間が各段階より短い: %+v", timings)
	}
}

func TestAutoResponseModeRaisesEffortOnlyForComplexQuestions(t *testing.T) {
	cases := []struct {
		question string
		want     ResponseMode
	}{
		{"TR797とは何ですか？", ModeFast},
		{"空力設計は38代から41代でどう変化しましたか？", ModeDeep},
		{"フライトシミュレーターについて教えて", ModeStandard},
	}
	for _, tc := range cases {
		if got := resolveResponseMode(ModeAuto, tc.question); got != tc.want {
			t.Errorf("resolveResponseMode(%q) = %q, want %q", tc.question, got, tc.want)
		}
	}
}

func TestGenericCanAnswerSeparatedGeneralKnowledgeWithoutPages(t *testing.T) {
	client := &stubLLM{}
	pipe := New(index.NewLive(testIndex(t), "test"), client)
	var answer strings.Builder

	if err := pipe.Run(context.Background(), "量子色力学とは？", nil, nil, func(event Event) {
		if event.Type == "delta" {
			answer.WriteString(event.Text)
		}
	}); err != nil {
		t.Fatalf("Run が失敗: %v", err)
	}
	if answer.String() != "（ダミー回答）" {
		t.Fatalf("資料が無い時点で汎用回答が止まった: %q", answer.String())
	}
	if !strings.Contains(client.lastSystem, "一般知識（WASA資料外）") {
		t.Fatal("汎用チャットに資料外知識の分離規則が渡っていない")
	}
}

func TestSelectPagesAddsNormalizedIdentifierMatch(t *testing.T) {
	client := &stubLLM{titles: []string{"人物ページ"}}
	pipe := New(index.NewLive(testIdentifierIndex(t), "test"), client)

	pages, err := pipe.selectPages(context.Background(), pipe.Index(), "TR797とは何ですか？", Scope{}, llm.ProfileFast, nil)
	if err != nil {
		t.Fatalf("ページ選択が失敗: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("型番候補とLLM候補が合流していない: %+v", pages)
	}
	if pages[0].Title != "空力設計" || pages[1].Title != "人物ページ" {
		t.Fatalf("型番の完全一致候補が優先されていない: %q, %q", pages[0].Title, pages[1].Title)
	}
}

func TestDirectTitlePagesPrioritizesGenerationSpecificPages(t *testing.T) {
	pipe := New(index.NewLive(testDirectTitleIndex(t), "test"), &stubLLM{})
	pages := directTitlePages(pipe.Index(), "41stの空力設計と40代の空力設計は何が違いますか？", Scope{})
	if len(pages) != 2 || pages[0].Title != "空力設計(40th)" || pages[1].Title != "空力設計(41st)" {
		t.Fatalf("世代別ページが汎用ページより優先されていない: %+v", pages)
	}
}

func TestSelectPagesKeepsDirectTitleMatchAlongsideModelChoice(t *testing.T) {
	client := &stubLLM{titles: []string{"人物ページ"}}
	pipe := New(index.NewLive(testDirectTitleIndex(t), "test"), client)
	pages, err := pipe.selectPages(context.Background(), pipe.Index(), "HPA交流会の準備を教えてください", Scope{}, llm.ProfileStandard, nil)
	if err != nil {
		t.Fatalf("ページ選択が失敗: %v", err)
	}
	if len(pages) != 2 || pages[0].Title != "HPA交流会" || pages[1].Title != "人物ページ" {
		t.Fatalf("タイトル一致とLLM候補が合流していない: %+v", pages)
	}
}

func TestLinkQuestionFindsWikiMainPageAndExcludesOfficialSite(t *testing.T) {
	dir := t.TempDir()
	payload := map[string]any{"pages": []map[string]any{
		{"id": "1", "source": "wiki", "title": "メインページ", "url": "https://wiki.example/main", "chars": 100,
			"chunks": []map[string]any{{"id": "p1-c0", "breadcrumb": "メインページ > 活動には直接関係しないもの", "chars": 100,
				"text": "[情報理工・情報通信学科 過去問・レポート](https://drive.example/past)"}}},
		{"id": "s1", "source": "site", "title": "公式サイト", "url": "https://site.example", "chars": 100,
			"chunks": []map[string]any{{"id": "ps1-c0", "breadcrumb": "公式サイト", "chars": 100, "text": "情報通信の紹介"}}},
	}}
	raw, _ := json.Marshal(payload)
	if err := os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "toc.md"), []byte("# 目次"), 0o600); err != nil {
		t.Fatal(err)
	}
	ix, err := index.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	client := &stubLLM{titles: []string{"公式サイト"}}
	pipe := New(index.NewLive(ix, "test"), client)
	pages, err := pipe.selectPages(context.Background(), pipe.Index(), "WASA Wikiには情報通信学科の過去問がありますか？リンクを教えてください", Scope{}, llm.ProfileStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Title != "メインページ" || pages[0].Source != "wiki" {
		t.Fatalf("本文中のリンクを持つWikiページだけを選べていない: %+v", pages)
	}
}

// 「リンクを教えて」だけでなく「どこにありますか」でもリンク検索が要る。
// 実索引で、前者は メインページ を選べるのに後者は選べなかった（2026-08-09）。
// あわせて、実在タイトルの一致を本文スコアより先に採ることも固定する。
// 「どこ」を足しただけの版では、q26「合宿で39代はどこに行きましたか」で
// リンク検索が 合宿 ページを押し出し、決定的候補の的中が19→18へ下がった。
func TestWhereQuestionFindsLinkPageWithoutPushingOutTitleMatch(t *testing.T) {
	dir := t.TempDir()
	payload := map[string]any{"pages": []map[string]any{
		{"id": "1", "source": "wiki", "title": "メインページ", "url": "https://wiki.example/main", "chars": 100,
			"chunks": []map[string]any{{"id": "p1-c0", "breadcrumb": "メインページ", "chars": 100,
				"text": "[情報通信学科　過去問](https://drive.example/past)"}}},
		{"id": "2", "source": "wiki", "title": "合宿", "url": "https://wiki.example/camp", "chars": 100,
			"chunks": []map[string]any{{"id": "p2-c0", "breadcrumb": "合宿", "chars": 100,
				"text": "39代の合宿は中止になった。行き先の検討経緯はここに残す。"}}},
		{"id": "3", "source": "wiki", "title": "大学に出す書類", "url": "https://wiki.example/doc", "chars": 100,
			"chunks": []map[string]any{{"id": "p3-c0", "breadcrumb": "大学に出す書類", "chars": 100,
				"text": "合宿の届出はどこに出すか。39代の様式を残す。"}}},
	}}
	raw, _ := json.Marshal(payload)
	if err := os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "toc.md"), []byte("# 目次"), 0o600); err != nil {
		t.Fatal(err)
	}
	ix, err := index.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	pipe := New(index.NewLive(ix, "test"), &stubLLM{})

	// 1. 「どこにありますか」でもリンクを含むページを保持する
	got := deterministicPages(pipe.Index(), "情報通信学科の過去問はどこにありますか？", Scope{})
	if len(got) == 0 || got[0].Title != "メインページ" {
		t.Fatalf("「どこ」の質問でリンク先ページを保持できていない: %+v", titlesOf(got))
	}

	// 2. タイトルが一致するページを、本文スコアで押し出さない
	got = deterministicPages(pipe.Index(), "合宿で39代はどこに行きましたか？", Scope{})
	if len(got) == 0 || got[0].Title != "合宿" {
		t.Fatalf("実在タイトルの一致が本文スコアに負けている: %+v", titlesOf(got))
	}
}

func titlesOf(pages []*index.Page) []string {
	var out []string
	for _, p := range pages {
		out = append(out, p.Title)
	}
	return out
}

// モデルに書かせてよいのは**資料の番号だけ**である。タイトルとURLを書かせると
// 「引き継ぎWiki」のような実在しない出典名を作れてしまう（docs/02）。
func TestAnswerPromptLeavesSourceListToServer(t *testing.T) {
	client := &stubLLM{titles: []string{"電装班"}}
	pipe := New(index.NewLive(testIndex(t), "test"), client)
	if err := pipe.Run(context.Background(), "電装班について", nil, nil, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.lastAnswer, "回答内に出典一覧を作らない") ||
		strings.Contains(client.lastAnswer, "[ページ名](URL)") {
		t.Fatal("モデルに出典一覧を作らせる古い規則が残っている")
	}
	// 番号で引かせる以上、番号の作り方まで縛らないと同じ穴が開く
	if !strings.Contains(client.lastAnswer, "書かれていない番号を作らない") {
		t.Fatal("存在しない資料番号を禁じる規則が無い")
	}
	if !strings.Contains(client.lastAnswer, "資料番号: 1") {
		t.Fatal("資料に番号が振られていない。モデルが [n] を書けない")
	}
}

// 回答末尾の「参照」に出す節は、索引のパンくずをそのまま渡す。
func TestPagesEventCarriesReadSections(t *testing.T) {
	client := &stubLLM{titles: []string{"電装班"}}
	pipe := New(index.NewLive(testIndex(t), "test"), client)
	var last []Source
	err := pipe.Run(context.Background(), "電装班について", nil, nil, func(e Event) {
		if e.Type == "pages" {
			last = e.Pages
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(last) == 0 {
		t.Fatal("pagesイベントが流れていない")
	}
	if len(last[0].Sections) == 0 {
		t.Fatalf("読んだ節が入っていない: %+v", last[0])
	}
	for _, section := range last[0].Sections {
		if !strings.Contains(section, last[0].Title) {
			t.Fatalf("パンくずがページ名から始まっていない: %q", section)
		}
	}
}

func TestDirectTitlePagesRespectsScopeAndASCIIWordBoundary(t *testing.T) {
	pipe := New(index.NewLive(testDirectTitleIndex(t), "test"), &stubLLM{})
	if pages := directTitlePages(pipe.Index(), "RPMの計測方法", Scope{}); len(pages) != 0 {
		t.Fatalf("短いPMがRPMの部分一致で拾われた: %+v", pages)
	}
	if pages := directTitlePages(pipe.Index(), "HPA交流会について", Scope{Assistant: &state.Assistant{Team: "空力"}}); len(pages) != 0 {
		t.Fatalf("アシスタントの参照範囲外が残った: %+v", pages)
	}
}

func TestRunKeepsIdentifierChunkAfterNarrowing(t *testing.T) {
	client := &stubLLM{
		titles:   []string{"人物ページ"},
		chunkIDs: []string{"p1-c0"}, // LLMは型番のない節だけを選ぶ
	}
	pipe := New(index.NewLive(testIdentifierIndex(t), "test"), client)

	if err := pipe.Run(context.Background(), "TR797とは何ですか？", nil, nil, func(Event) {}); err != nil {
		t.Fatalf("Run が失敗: %v", err)
	}
	if !strings.Contains(client.lastAnswer, "TR-797型分布") {
		t.Fatal("ページ選択後の節絞り込みで、型番の完全一致チャンクが落ちた")
	}
}

func TestRunResolvesIdentifierFromRecentConversation(t *testing.T) {
	client := &stubLLM{
		titles:   []string{"人物ページ"},
		chunkIDs: []string{"p1-c0"},
	}
	pipe := New(index.NewLive(testIdentifierIndex(t), "test"), client)
	history := []ConversationTurn{{
		Question: "循環分布には何がありますか？",
		Answer:   "完全楕円循環分布とTR-797型分布があります。",
	}}

	if err := pipe.Run(context.Background(), "後者について詳しく", history, nil, func(Event) {}); err != nil {
		t.Fatalf("Run が失敗: %v", err)
	}
	if !strings.Contains(client.lastAnswer, "TR-797型分布は翼根曲げモーメント") {
		t.Fatal("直近の会話にある型番から正解チャンクを引けていない")
	}
}

// 絞り込みで全滅したときに、無言で「見つかりません」と言うと壊れて見える。
func TestRunReportsEmptyScope(t *testing.T) {
	_, sources, answer := run(t, &state.Assistant{
		Name: "パイロット班", Instruction: "簡潔に", Team: "パイロット",
	})
	if len(sources) != 0 {
		t.Fatalf("範囲外が残った: %+v", sources)
	}
	if !strings.Contains(answer, "参照範囲") {
		t.Errorf("絞り込みが原因だと分かる文面になっていない: %q", answer)
	}
}

// 区分の絞り込みは公開範囲の境界ではないため、目次を差し替えない。
// 区分ごとに分けるとキャッシュが分裂し、使う人の少ないものほど単価が上がる。
func TestRunKeepsTOCCacheable(t *testing.T) {
	ix := testIndex(t)
	var cached []string
	pipe := New(index.NewLive(ix, "test"), &cacheProbe{inner: &stubLLM{titles: []string{"電装班"}}, seen: &cached})
	for _, a := range []*state.Assistant{nil, {Name: "電装", Instruction: "簡潔に", Team: "電装"}} {
		if err := pipe.Run(context.Background(), "質問", nil, a, func(Event) {}); err != nil {
			t.Fatalf("Run が失敗: %v", err)
		}
	}
	for _, got := range cached {
		if got != ix.TOC {
			t.Fatal("アシスタントによって目次が変わっている（キャッシュが分裂する）")
		}
	}
}

type cacheProbe struct {
	inner *stubLLM
	seen  *[]string
}

func (c *cacheProbe) Complete(ctx context.Context, req llm.Request) (string, error) {
	if req.Cached != "" {
		*c.seen = append(*c.seen, req.Cached)
	}
	return c.inner.Complete(ctx, req)
}

func (c *cacheProbe) Stream(ctx context.Context, req llm.Request, onDelta llm.Delta) (string, error) {
	if req.Cached != "" {
		*c.seen = append(*c.seen, req.Cached)
	}
	return c.inner.Stream(ctx, req, onDelta)
}

func (c *cacheProbe) Name() string { return "probe" }

// 「公式サイトのみ」は部外に出せる情報だけに限る用途で使う。
// 選択ページと出典を絞っても、**目次を渡したままだと回答本文が部内Wikiの
// ページ名やリード文を拾える**（回答プロンプトが目次を根拠にしてよいと
// 明記しているため）。目次側も出所で絞れていることを固定する。
func TestRunScopedTOCHidesWikiSection(t *testing.T) {
	ix := testIndexWithTOC(t, "# 目次\n\n## 引き継ぎWiki（部内限定）全1ページ\n\n- **電装班** 部内限定の秘密\n\n## 公式サイト（一般公開 wasa-birdman.com）全1ページ\n\n- **WASAについて知る** 団体紹介\n")
	client := &stubLLM{titles: []string{"WASAについて知る"}}
	pipe := New(index.NewLive(ix, "test"), &cacheProbe{inner: client, seen: new([]string)})

	if err := pipe.Run(context.Background(), "WASAとは", nil, &state.Assistant{
		Name: "対外説明", Instruction: "ですます調", Origin: "site",
	}, func(Event) {}); err != nil {
		t.Fatalf("Run が失敗: %v", err)
	}
	// 目次は Cached に載る。Prompt だけ見ても素通りするので両方を確かめる
	whole := client.lastCached + client.lastAnswer
	if strings.Contains(whole, "部内限定の秘密") {
		t.Error("目次経由で部内Wikiの内容が回答プロンプトへ入った")
	}
	if !strings.Contains(whole, "団体紹介") {
		t.Error("公式サイト側の目次まで落ちている")
	}
}

// 目次の見出しが変わって分割できないときは、全体を渡すのではなく目次なしにする。
func TestSiteTOCFailsClosed(t *testing.T) {
	ix := testIndexWithTOC(t, "# 目次\n\n見出しの形式が変わった\n")
	if got := scopedTOC(ix, Scope{Assistant: &state.Assistant{Origin: "site"}}); got != "" {
		t.Errorf("分割できないのに目次を渡した: %q", got)
	}
}

// モデルが範囲外の節IDを返しても採用しない。IDは p{ページ}-c{連番} で予測できる。
func TestSelectChunksRejectsOutOfScopeIDs(t *testing.T) {
	// 節の絞り込みは総量が directContextLimit を超えたときだけ走る。
	// 超えるだけの本文を持つ索引を用意して、LLMの返答を通す経路にする
	ix := testLargeIndex(t)
	site, ok := ix.Resolve("WASAについて知る")
	if !ok {
		t.Fatal("下準備のページが無い")
	}
	pipe := New(index.NewLive(ix, "test"), &stubLLM{chunkIDs: []string{"p1-c0"}}) // 選択外のWikiの節
	got, err := pipe.selectChunks(context.Background(), ix, "質問", []*index.Page{site}, llm.ProfileStandard, nil)
	if err != nil {
		t.Fatalf("selectChunks が失敗: %v", err)
	}
	for _, id := range got {
		if id == "p1-c0" {
			t.Fatal("選択ページ外の節IDが採用された")
		}
	}
}

// testLargeIndex は節の絞り込みが走る大きさの索引を作る。
func testLargeIndex(t *testing.T) *index.Index {
	t.Helper()
	dir := t.TempDir()
	long := strings.Repeat("あ", 7000)
	payload := map[string]any{"pages": []map[string]any{
		{
			"id": "1", "source": "wiki", "title": "電装班", "url": "https://wiki.example/dens",
			"last_edited": "2026-04-01", "team": "電装", "chars": 7000,
			"chunks": []map[string]any{
				{"id": "p1-c0", "breadcrumb": "電装班", "text": long, "chars": 7000},
			},
		},
		{
			"id": "s1", "source": "site", "title": "WASAについて知る", "url": "https://wasa-birdman.com/about",
			"last_edited": "2026-04-01", "chars": 14000,
			"chunks": []map[string]any{
				{"id": "ps1-c0", "breadcrumb": "WASAについて知る > 前半", "text": long, "chars": 7000},
				{"id": "ps1-c1", "breadcrumb": "WASAについて知る > 後半", "text": long, "chars": 7000},
			},
		},
	}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "toc.md"), []byte("# 目次\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ix, err := index.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// 「設計」は空力と構造をまとめた区分。区分の突き合わせを == で書くと必ず漏れる。
func TestDesignScopeCoversAerodynamicsAndStructure(t *testing.T) {
	pages := []struct {
		title string
		team  string
		want  bool
	}{
		{"空力設計(41st)", "空力", true},
		{"構造設計 38th", "構造", true},
		{"駆動設計", "駆動・フレーム", false},
		{"電装班", "電装", false},
		{"区分なしのページ", "", false},
	}
	a := &state.Assistant{Name: "設計（空力・構造）", Team: "設計"}
	for _, c := range pages {
		pg := &index.Page{Title: c.title, Source: "wiki", Team: c.team}
		if got := inScope(pg, Scope{Assistant: a}); got != c.want {
			t.Errorf("%s（区分=%s）: inScope=%v, 期待=%v", c.title, c.team, got, c.want)
		}
	}
	// 単独区分の指定は従来どおり
	solo := &state.Assistant{Name: "電装班", Team: "電装"}
	if !inScope(&index.Page{Title: "電装班", Source: "wiki", Team: "電装"}, Scope{Assistant: solo}) {
		t.Error("単独区分の指定が壊れている")
	}
	if inScope(&index.Page{Title: "空力設計(41st)", Source: "wiki", Team: "空力"}, Scope{Assistant: solo}) {
		t.Error("単独区分が別の区分まで通している")
	}
}

// 「公式サイトのみ」の目次に、後ろの節が付いてこないこと。
//
// 以前は公式サイトの見出しから**ファイル末尾まで**返していたため、出所が3つに
// なったとき（M27）にフライトシミュレータのガイド全32ページが「公式サイトのみ」の
// アシスタントへ渡っていた（2026-09-12に発見）。参照範囲そのものは inScope で
// 効いているので資料は混ざらないが、回答プロンプトは「目次はページの有無や
// 分野ごとの情報量の根拠にしてよい」と明記している。
// **出所を足すたびに同じ壊れ方をしないこと。**
func TestSiteTOCStopsAtNextOrigin(t *testing.T) {
	toc := "# 基本情報\n\nWASAの説明\n" +
		"\n## 引き継ぎWiki（部内限定）全114ページ\n- **電装班** 部内の話\n" +
		"\n## 公式サイト（一般公開 wasa-birdman.com）全500ページ\n- **WASAについて知る** 団体紹介\n" +
		"\n## フライトシミュレータのガイド（FlightEnvironmentEmulator）全32ページ\n- **FEEの使い方** ソフトの話\n"
	ix := testIndexWithTOC(t, toc)
	got := scopedTOC(ix, Scope{Assistant: &state.Assistant{Origin: "site"}})

	if !strings.Contains(got, "WASAについて知る") {
		t.Fatalf("公式サイトの節が落ちている:\n%s", got)
	}
	if !strings.Contains(got, "WASAの説明") {
		t.Fatalf("先頭の基本情報が落ちている:\n%s", got)
	}
	for _, leaked := range []string{"電装班", "フライトシミュレータ", "FEEの使い方"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("範囲外の %q が目次へ入っている:\n%s", leaked, got)
		}
	}
}
