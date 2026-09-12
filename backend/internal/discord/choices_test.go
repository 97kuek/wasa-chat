package discord

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func denyEveryone(guildID string) []struct {
	ID   string `json:"id"`
	Type int    `json:"type"`
	Deny string `json:"deny"`
} {
	return []struct {
		ID   string `json:"id"`
		Type int    `json:"type"`
		Deny string `json:"deny"`
	}{{ID: guildID, Deny: fmt.Sprint(permViewChannel)}}
}

func choicesStub(t *testing.T) *discordStub {
	t.Helper()
	// 候補は1分間覚えるので、テストごとに捨てる
	channelCacheMu.Lock()
	channelCache = map[string]cachedChannels{}
	channelCacheMu.Unlock()

	stub := &discordStub{channels: []Channel{
		{ID: "c1", Type: channelTypeText, Name: "機体班", Position: 0},
		{ID: "c2", Type: channelTypeText, Name: "電装班", Position: 1},
		{ID: "c3", Type: channelTypeAnnouncement, Name: "お知らせ", Position: 2},
		{ID: "secret", Type: channelTypeText, Name: "幹部会", Position: 3,
			PermissionOverwrit: denyEveryone("g1")},
	}}
	stub.serve(t)
	return stub
}

// 先頭は必ず「公開チャンネル全部」。その後ろに公開チャンネルを画面順で並べる
func TestScopeChoicesListsPublicChannels(t *testing.T) {
	choicesStub(t)
	got := ScopeChoices(t.Context(), "token", "g1", "")

	if len(got) != 4 {
		t.Fatalf("候補の数が違う: %+v", got)
	}
	if got[0].Value != ScopeAllChannels {
		t.Fatalf("先頭が「全部」でない: %+v", got[0])
	}
	for _, choice := range got {
		if strings.Contains(choice.Name, "幹部会") || choice.Value == "secret" {
			t.Fatalf("非公開チャンネルを候補に出している: %+v", choice)
		}
	}
	if got[1].Name != "#機体班" || got[2].Name != "#電装班" {
		t.Fatalf("画面順に並んでいない: %+v", got)
	}
}

// **打つと候補が絞られる**のが要求（2026-09-13）
func TestScopeChoicesFiltersByTyped(t *testing.T) {
	choicesStub(t)

	got := ScopeChoices(t.Context(), "token", "g1", "電装")
	if len(got) != 1 || got[0].Name != "#電装班" {
		t.Fatalf("絞り込めていない: %+v", got)
	}

	// 「全部」も打った文字で絞られる
	if got := ScopeChoices(t.Context(), "token", "g1", "公開"); len(got) == 0 || got[0].Value != ScopeAllChannels {
		t.Fatalf("「全部」を絞り込めていない: %+v", got)
	}
	// 一致しなければ空。Discordは空の候補を受け取れる
	if got := ScopeChoices(t.Context(), "token", "g1", "存在しない班"); len(got) != 0 {
		t.Fatalf("一致しないのに候補が出ている: %+v", got)
	}
}

// DMには公開チャンネルの概念が無い。「全部」だけ返す
func TestScopeChoicesWithoutGuild(t *testing.T) {
	choicesStub(t)
	got := ScopeChoices(t.Context(), "token", "", "")
	if len(got) != 1 || got[0].Value != ScopeAllChannels {
		t.Fatalf("DMでチャンネルを出している: %+v", got)
	}
}

// ⚠️ **補完で出したものだけが選ばれるとは限らない。** 非公開チャンネルのIDを
// 手で打たれても読まないこと
func TestPublicChannelRejectsPrivate(t *testing.T) {
	choicesStub(t)

	if channel, ok := PublicChannel(t.Context(), "token", "g1", "c1"); !ok || channel.Name != "機体班" {
		t.Fatalf("公開チャンネルを拒んだ: %+v %v", channel, ok)
	}
	if _, ok := PublicChannel(t.Context(), "token", "g1", "secret"); ok {
		t.Fatal("非公開チャンネルを通した")
	}
	if _, ok := PublicChannel(t.Context(), "token", "g1", "そんなIDは無い"); ok {
		t.Fatal("存在しないIDを通した")
	}
}

// ⚠️ **補完は3秒以内に同期で返す。** 打つたびにDiscordへ問い合わせると
// 間に合わないので短く覚える
func TestScopeChoicesCaches(t *testing.T) {
	stub := choicesStub(t)

	for i := 0; i < 5; i++ {
		ScopeChoices(t.Context(), "token", "g1", "機")
	}
	if stub.requests != 1 {
		t.Fatalf("毎回問い合わせている: %d回", stub.requests)
	}

	// 覚えている時間を過ぎたら取り直す（新しいチャンネルが候補に出るように）
	channelCacheMu.Lock()
	channelCache["g1"] = cachedChannels{channels: channelCache["g1"].channels, at: time.Now().Add(-2 * channelCacheTTL)}
	channelCacheMu.Unlock()
	ScopeChoices(t.Context(), "token", "g1", "機")
	if stub.requests != 2 {
		t.Fatalf("古いまま返している: %d回", stub.requests)
	}
}

// 候補が作れなくてもコマンド自体は打てる。ここで失敗を見せない
func TestScopeChoicesSurvivesFailure(t *testing.T) {
	channelCacheMu.Lock()
	channelCache = map[string]cachedChannels{}
	channelCacheMu.Unlock()
	stub := &discordStub{} // チャンネル一覧が空 → ErrNoPublicChannels
	stub.serve(t)

	got := ScopeChoices(t.Context(), "token", "g1", "")
	if len(got) != 1 || got[0].Value != ScopeAllChannels {
		t.Fatalf("失敗が候補に漏れている: %+v", got)
	}
}
