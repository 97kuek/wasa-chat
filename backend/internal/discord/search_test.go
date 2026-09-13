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

	got, err := Search(t.Context(), "token", "g1", "翼型",
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

	got, err := Search(t.Context(), "token", "g1", "翼型", []Channel{{ID: "c1", Name: "機体班"}})
	if err != nil {
		t.Fatal(err)
	}
	if CountMessages(got) < 2 {
		t.Fatalf("前後を取っていない: %d件", CountMessages(got))
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

	got, err := Search(t.Context(), "token", "g1", "極秘", []Channel{{ID: "c1", Name: "機体班"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(Transcript(got), "極秘の話") {
		t.Fatalf("許可外のヒットを通した: %+v", got)
	}
}

func TestSearchNeedsGuildAndChannels(t *testing.T) {
	if _, err := Search(t.Context(), "token", "", "翼型", []Channel{{ID: "c1"}}); err != ErrNotInGuild {
		t.Fatalf("サーバー指定なしを通した: %v", err)
	}
	if _, err := Search(t.Context(), "token", "g1", "翼型", nil); err != ErrNoPublicChannels {
		t.Fatalf("チャンネル指定なしを通した: %v", err)
	}
	if _, err := Search(t.Context(), "", "g1", "翼型", []Channel{{ID: "c1"}}); err != ErrNoBotToken {
		t.Fatalf("トークンなしを通した: %v", err)
	}
}

func TestSearchScope(t *testing.T) {
	got := SearchScope("翼型", 2, 14)
	for _, want := range []string{"公開チャンネル2件", "翼型", "14件"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q が無い: %s", want, got)
		}
	}
}
