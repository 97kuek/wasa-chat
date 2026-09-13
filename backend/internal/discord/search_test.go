package discord

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// ⚠️ **読む先は allowed で渡されたチャンネルだけ。** 検索APIは「ボットが
// 見える範囲」で認可されるので、そのまま使うと画面の利用者（Wikiアカウント）が
// 非公開チャンネルの中身を読めてしまう（2026-09-13のCodex指摘）
func TestSearchNamesChannelsExplicitly(t *testing.T) {
	var query string
	stub := &discordStub{pages: map[string][][]Message{"c1": {page("前後", 3, time.Minute)}}}
	stub.searchHandler = func(r *http.Request) any {
		query = r.URL.RawQuery
		hit := message("部員A", "翼型はNACA4412にした", time.Minute)
		hit.ID, hit.ChannelID, hit.Hit = "hit1", "c1", true
		return map[string]any{"messages": [][]Message{{hit}}}
	}
	stub.serve(t)

	got, err := Search(t.Context(), "token", "g1", []string{"翼型"},
		[]Channel{{ID: "c1", Name: "機体班"}, {ID: "c2", Name: "電装班"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "channel_id=c1") || !strings.Contains(query, "channel_id=c2") {
		t.Fatalf("チャンネルを明示していない: %s", query)
	}
	if strings.Contains(query, "channel_id=secret") {
		t.Fatalf("許可していないチャンネルを渡している: %s", query)
	}
	if len(got) != 1 || got[0].Channel != "機体班" {
		t.Fatalf("チャンネル名を付けていない: %+v", got)
	}
}

// **検索結果だけでは会話にならない。** 1行では何の話の途中か分からないので、
// 前後を取って初めて要約の材料になる
func TestSearchFetchesContextAroundHit(t *testing.T) {
	stub := &discordStub{pages: map[string][][]Message{"c1": {page("前後", 5, time.Minute)}}}
	stub.searchHandler = func(_ *http.Request) any {
		hit := message("部員A", "翼型の話", time.Minute)
		hit.ID, hit.ChannelID, hit.Hit = "hit1", "c1", true
		return map[string]any{"messages": [][]Message{{hit}}}
	}
	stub.serve(t)

	got, err := Search(t.Context(), "token", "g1", []string{"翼型"}, []Channel{{ID: "c1", Name: "機体班"}})
	if err != nil {
		t.Fatal(err)
	}
	if CountMessages(got) < 2 {
		t.Fatalf("前後を取っていない: %d件", CountMessages(got))
	}
}

// ⚠️ **1語目の結果で枠を使い切らない。** 語を分けて投げるのはANDで潰さない
// ためだが（docs/08 M64）、結果を語ごとに前から並べると、1語目が上限まで
// 返した時点で MaxSearchHits が埋まり、**2語目以降が1件も読まれない**。
// 分けて投げた意味が結果側で消えていた
func TestSearchGivesEveryTermAShare(t *testing.T) {
	stub := &discordStub{pages: map[string][][]Message{}}
	stub.searchHandler = func(r *http.Request) any {
		term := r.URL.Query().Get("content")
		// 1語目だけが上限いっぱい返す状況を作る
		count := 1
		if term == "荷重試験" {
			count = SearchLimit
		}
		groups := make([][]Message, 0, count)
		for i := 0; i < count; i++ {
			hit := message("部員A", term+"の話", time.Minute)
			hit.ID = term + "-" + strings.Repeat("x", i+1)
			hit.ChannelID, hit.Hit = "c1", true
			groups = append(groups, []Message{hit})
		}
		return map[string]any{"messages": groups}
	}
	stub.serve(t)

	got, err := Search(t.Context(), "token", "g1", []string{"荷重試験", "申請"}, []Channel{{ID: "c1", Name: "機体班"}})
	if err != nil {
		t.Fatal(err)
	}
	transcript := Transcript(got)
	if !strings.Contains(transcript, "申請の話") {
		t.Fatalf("2語目が1件も読まれていない:\n%s", transcript)
	}
}

// 許可していないチャンネルのヒットが返ってきても捨てる（二重の防御）
func TestSearchDropsHitsOutsideAllowed(t *testing.T) {
	stub := &discordStub{pages: map[string][][]Message{}}
	stub.searchHandler = func(_ *http.Request) any {
		hit := message("部員A", "極秘の話", time.Minute)
		hit.ID, hit.ChannelID, hit.Hit = "hit1", "secret", true
		return map[string]any{"messages": [][]Message{{hit}}}
	}
	stub.serve(t)

	got, err := Search(t.Context(), "token", "g1", []string{"極秘"}, []Channel{{ID: "c1", Name: "機体班"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(Transcript(got), "極秘の話") {
		t.Fatalf("許可外のヒットを通した: %+v", got)
	}
}

func TestSearchNeedsGuildAndChannels(t *testing.T) {
	if _, err := Search(t.Context(), "token", "", []string{"翼型"}, []Channel{{ID: "c1"}}); err != ErrNotInGuild {
		t.Fatalf("サーバー指定なしを通した: %v", err)
	}
	if _, err := Search(t.Context(), "token", "g1", []string{"翼型"}, nil); err != ErrNoPublicChannels {
		t.Fatalf("チャンネル指定なしを通した: %v", err)
	}
	if _, err := Search(t.Context(), "", "g1", []string{"翼型"}, []Channel{{ID: "c1"}}); err != ErrNoBotToken {
		t.Fatalf("トークンなしを通した: %v", err)
	}
}

func TestSearchScope(t *testing.T) {
	got := SearchScope([]string{"翼型"}, 2, 14)
	for _, want := range []string{"公開チャンネル2件", "翼型", "14件"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q が無い: %s", want, got)
		}
	}
}

// Discordは「ヒットとその前後」を組で返す。組の中で hit が立っているものが本体
func TestPickHit(t *testing.T) {
	before := message("部員A", "前", time.Minute)
	hit := message("部員B", "本体", time.Minute)
	hit.Hit = true
	after := message("部員C", "後", time.Minute)

	if got, ok := pickHit([]Message{before, hit, after}); !ok || got.Content != "本体" {
		t.Fatalf("ヒットを選べない: %+v", got)
	}
	// hit が立っていなければ先頭を使う（仕様が変わっても空にしない）
	if got, ok := pickHit([]Message{before, after}); !ok || got.Content != "前" {
		t.Fatalf("先頭へ落ちていない: %+v", got)
	}
	if _, ok := pickHit(nil); ok {
		t.Fatal("空の組から取り出している")
	}
}

// 見出しの順を毎回同じにする。走るたびに並びが変わると差分が読めない
func TestSearchOrdersChannelsStably(t *testing.T) {
	stub := &discordStub{pages: map[string][][]Message{
		"c1": {page("あ", 1, time.Minute)}, "c2": {page("い", 1, time.Minute)},
	}}
	stub.searchHandler = func(_ *http.Request) any {
		groups := [][]Message{}
		for id, name := range map[string]string{"c2": "電装", "c1": "機体"} {
			hit := message("部員A", name, time.Minute)
			hit.ID, hit.ChannelID, hit.Hit = "hit-"+id, id, true
			groups = append(groups, []Message{hit})
		}
		return map[string]any{"messages": groups}
	}
	stub.serve(t)

	allowed := []Channel{{ID: "c1", Name: "機体班"}, {ID: "c2", Name: "電装班"}}
	for i := 0; i < 3; i++ {
		got, err := Search(t.Context(), "token", "g1", []string{"翼型"}, allowed)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Channel != "機体班" || got[1].Channel != "電装班" {
			t.Fatalf("%d回目の並びが違う: %+v", i+1, got)
		}
	}
}

// 候補の表示名から復元しない。表示を変えた瞬間に読む先が壊れる
func TestPublicChannelsReturnsStructs(t *testing.T) {
	stub := &discordStub{channels: []Channel{
		{ID: "c1", Type: channelTypeText, Name: "機体班", Position: 0},
	}}
	stub.serve(t)

	got := PublicChannels(t.Context(), "token", "g1")
	if len(got) != 1 || got[0].ID != "c1" || got[0].Name != "機体班" {
		t.Fatalf("チャンネルを返せていない: %+v", got)
	}
	if PublicChannels(t.Context(), "", "g1") != nil || PublicChannels(t.Context(), "token", "") != nil {
		t.Fatal("設定が足りないのに読もうとしている")
	}
}
