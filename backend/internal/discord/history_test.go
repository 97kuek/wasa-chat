package discord

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func message(name, content string, ago time.Duration) Message {
	var m Message
	m.ID = fmt.Sprint(time.Now().Add(-ago).UnixNano())
	m.Author.ID = name
	m.Author.GlobalName = name
	m.Content = content
	m.Timestamp = time.Now().Add(-ago)
	return m
}

func botMessage(content string) Message {
	m := message("WASA Chat", content, time.Minute)
	m.Author.Bot = true
	return m
}

func logs(messages ...Message) []ChannelLog { return []ChannelLog{{Messages: messages}} }

// Discordは**新しい順**で返す。要約に渡すときは読める向きへ並べ直す。
func TestTranscriptOrdersOldestFirst(t *testing.T) {
	got := Transcript(logs(
		message("部員C", "3番目", 1*time.Hour),
		message("部員B", "2番目", 2*time.Hour),
		message("部員A", "1番目", 3*time.Hour),
	))
	lines := strings.Split(got, "\n")
	for i, want := range []string{"1番目", "2番目", "3番目"} {
		if !strings.HasSuffix(lines[i], want) {
			t.Fatalf("並び順が違う:\n%s", got)
		}
	}
}

// ボットの発言は落とす。WASA自身の回答まで混ぜると、**自分の要約を要約する**
// ことになって内容が薄まる。本文が空の発言（添付だけ）も落とす。
func TestTranscriptDropsBotsAndEmpty(t *testing.T) {
	got := Transcript(logs(
		message("部員A", "  ", time.Minute),
		botMessage("> 質問\n\n回答です"),
		message("部員B", "荷重試験はいつ？", time.Minute),
	))
	if strings.Count(got, "\n") != 0 || !strings.Contains(got, "部員B: 荷重試験はいつ？") {
		t.Fatalf("落としきれていない:\n%s", got)
	}
}

func TestTranscriptFlattensNewlines(t *testing.T) {
	got := Transcript(logs(message("部員A", "決めたいこと\n・日程\n・場所", time.Minute)))
	if strings.Count(got, "\n") != 0 {
		t.Fatalf("改行が残っている: %q", got)
	}
	if !strings.Contains(got, "決めたいこと / ・日程 / ・場所") {
		t.Fatalf("つなぎ方が違う: %q", got)
	}
}

func TestTranscriptTruncatesLongMessage(t *testing.T) {
	got := Transcript(logs(message("部員A", strings.Repeat("あ", MessageLengthLimit+500), time.Minute)))
	if length := len([]rune(got)); length > MessageLengthLimit+40 {
		t.Fatalf("1発言を切っていない: %d文字", length)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("切ったことが分からない: %q", got[len(got)-30:])
	}
}

// **切り詰めはここでしない。** 長いときに古いほうを捨てると、前の代で出た案が
// 黙って消える。全部渡して、分割して読むかどうかは recap 側が決める
func TestTranscriptKeepsEverything(t *testing.T) {
	var messages []Message
	messages = append(messages, message("最新", "いちばん新しい発言", time.Minute))
	for i := 0; i < 300; i++ {
		messages = append(messages, message("古い", strings.Repeat("あ", 300), time.Duration(i+2)*time.Hour))
	}
	messages = append(messages, message("最古", "いちばん古い発言", 400*time.Hour))
	got := Transcript(logs(messages...))

	if !strings.Contains(got, "いちばん新しい発言") {
		t.Fatal("最新の発言が落ちている")
	}
	if !strings.Contains(got, "いちばん古い発言") {
		t.Fatal("古い発言を捨てている。ここで捨ててはいけない")
	}
}

// 横断のときはチャンネル名を見出しにする。1チャンネルなら付けない
func TestTranscriptLabelsChannels(t *testing.T) {
	got := Transcript([]ChannelLog{
		{Channel: "機体班", Messages: []Message{message("部員A", "翼型を決めたい", time.Minute)}},
		{Channel: "電装班", Messages: []Message{message("部員B", "配線を見直す", time.Minute)}},
	})
	if !strings.Contains(got, "## #機体班") || !strings.Contains(got, "## #電装班") {
		t.Fatalf("チャンネル名が無い:\n%s", got)
	}

	single := Transcript(logs(message("部員A", "あ", time.Minute)))
	if strings.Contains(single, "##") {
		t.Fatalf("1チャンネルなのに見出しを付けている:\n%s", single)
	}
}

func TestCountMessagesAndSpeakers(t *testing.T) {
	all := []ChannelLog{
		{Channel: "a", Messages: []Message{
			message("部員A", "あ", time.Minute),
			message("部員A", "い", time.Minute),
			botMessage("う"),
		}},
		{Channel: "b", Messages: []Message{
			message("部員B", "え", time.Minute),
			message("部員C", "", time.Minute),
		}},
	}
	if got := CountMessages(all); got != 3 {
		t.Fatalf("件数が違う: %d", got)
	}
	if got := CountSpeakers(all); got != 2 {
		t.Fatalf("人数が違う: %d", got)
	}
}

// ⚠️ **チャンネル横断の安全弁。** ボットが見えるチャンネルと、コマンドを打った人が
// 見えるチャンネルは同じではない。@everyone から見えないチャンネルは読まない
func TestChannelPublic(t *testing.T) {
	const guild = "999"
	deny := func(id string, bits uint64) Channel {
		c := Channel{ID: "c", Type: channelTypeText}
		c.PermissionOverwrit = append(c.PermissionOverwrit, struct {
			ID   string `json:"id"`
			Type int    `json:"type"`
			Deny string `json:"deny"`
		}{ID: id, Deny: fmt.Sprint(bits)})
		return c
	}

	if !(Channel{Type: channelTypeText}).public(guild) {
		t.Fatal("上書きの無いチャンネルを非公開扱いにした")
	}
	if !(Channel{Type: channelTypeAnnouncement}).public(guild) {
		t.Fatal("お知らせチャンネルを非公開扱いにした")
	}
	if (Channel{Type: 2}).public(guild) {
		t.Fatal("音声チャンネルを読もうとしている")
	}
	if channel := deny(guild, permViewChannel); channel.public(guild) {
		t.Fatal("@everyone から見えないチャンネルを公開扱いにした")
	}
	// 別ロールへの拒否は @everyone の可視性に関係しない
	if channel := deny("111", permViewChannel); !channel.public(guild) {
		t.Fatal("別ロールの拒否で公開チャンネルを外した")
	}
	// 読めない値は**安全側**へ倒す
	if channel := deny(guild, 0); !channel.public(guild) {
		t.Fatal("見える設定を外した")
	}
	broken := Channel{Type: channelTypeText}
	broken.PermissionOverwrit = append(broken.PermissionOverwrit, struct {
		ID   string `json:"id"`
		Type int    `json:"type"`
		Deny string `json:"deny"`
	}{ID: guild, Deny: "数値ではない"})
	if broken.public(guild) {
		t.Fatal("壊れた値を公開扱いにした（安全側へ倒していない）")
	}
}

func TestOptionsDays(t *testing.T) {
	for _, c := range []struct{ in, want int }{{0, DefaultDays}, {-3, DefaultDays}, {5, 5}, {999, MaxDays}} {
		if got := (Options{Days: c.in}).days(); got != c.want {
			t.Fatalf("Days=%d が %d（期待 %d）", c.in, got, c.want)
		}
	}
}

func TestGatherNeedsBotToken(t *testing.T) {
	if _, err := Gather(t.Context(), "", "g", "c", Options{}); err != ErrNoBotToken {
		t.Fatalf("トークン無しを通した: %v", err)
	}
}

// discordStub はDiscordのAPIを最小限に真似る。
type discordStub struct {
	pages    map[string][][]Message // チャンネルID → ページ（新しい順）
	channels []Channel
	requests int
}

func (d *discordStub) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v10/guilds/{id}/channels", func(w http.ResponseWriter, _ *http.Request) {
		d.requests++
		_ = json.NewEncoder(w).Encode(d.channels)
	})
	mux.HandleFunc("/api/v10/channels/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		d.requests++
		pages, ok := d.pages[r.PathValue("id")]
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		index := 0
		if before := r.URL.Query().Get("before"); before != "" {
			for i, page := range pages {
				if len(page) > 0 && page[len(page)-1].ID == before {
					index = i + 1
				}
			}
		}
		if index >= len(pages) {
			_ = json.NewEncoder(w).Encode([]Message{})
			return
		}
		_ = json.NewEncoder(w).Encode(pages[index])
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	apiBase = server.URL + "/api/v10"
	// 連投の間隔はテストでは要らない。60回待つと15秒かかる
	requestInterval = 0
	t.Cleanup(func() { apiBase, requestInterval = defaultAPIBase, 250*time.Millisecond })
	return server
}

func page(prefix string, count int, start time.Duration) []Message {
	out := make([]Message, 0, count)
	for i := 0; i < count; i++ {
		age := start + time.Duration(i)*time.Minute
		m := message("部員A", fmt.Sprintf("%s-%d", prefix, i), age)
		m.ID = fmt.Sprintf("%s-%d", prefix, i)
		out = append(out, m)
	}
	return out
}

// Discordは1回に100件しか返さない。**「直近100件」で固定すると、話が続いた日の
// 午後には午前の話が読めなくなる。** `before` で遡ること
func TestGatherPaginates(t *testing.T) {
	stub := &discordStub{pages: map[string][][]Message{
		"c1": {page("新", pageSize, time.Minute), page("中", pageSize, 3*time.Hour), page("古", 20, 6*time.Hour)},
	}}
	stub.serve(t)

	got, err := Gather(t.Context(), "token", "", "c1", Options{Days: 7})
	if err != nil {
		t.Fatal(err)
	}
	if total := CountMessages(got); total != pageSize*2+20 {
		t.Fatalf("ページを遡れていない: %d件", total)
	}
	transcript := Transcript(got)
	if !strings.Contains(transcript, "古-19") || !strings.Contains(transcript, "新-0") {
		t.Fatal("最初のページしか読んでいない")
	}
}

// 期間より古い発言は読まない。**そこで打ち切る**（次のページも取りに行かない）
func TestGatherStopsAtCutoff(t *testing.T) {
	stub := &discordStub{pages: map[string][][]Message{
		"c1": {page("新", 3, time.Minute), page("ずっと古い", 50, 40*24*time.Hour)},
	}}
	stub.serve(t)

	got, err := Gather(t.Context(), "token", "", "c1", Options{Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total := CountMessages(got); total != 3 {
		t.Fatalf("期間外まで読んでいる: %d件", total)
	}
	if strings.Contains(Transcript(got), "ずっと古い") {
		t.Fatal("期間外の発言が混ざっている")
	}
}

// 横断は**公開チャンネルだけ**。非公開は読みに行きもしない
func TestGatherAllChannelsSkipsPrivate(t *testing.T) {
	private := Channel{ID: "secret", Type: channelTypeText, Name: "幹部会", Position: 1}
	private.PermissionOverwrit = append(private.PermissionOverwrit, struct {
		ID   string `json:"id"`
		Type int    `json:"type"`
		Deny string `json:"deny"`
	}{ID: "g1", Deny: fmt.Sprint(permViewChannel)})

	stub := &discordStub{
		channels: []Channel{
			{ID: "c2", Type: channelTypeText, Name: "電装班", Position: 2},
			private,
			{ID: "c1", Type: channelTypeText, Name: "機体班", Position: 0},
		},
		pages: map[string][][]Message{
			"c1":     {page("機体", 2, time.Minute)},
			"c2":     {page("電装", 2, time.Minute)},
			"secret": {page("極秘", 2, time.Minute)},
		},
	}
	stub.serve(t)

	got, err := Gather(t.Context(), "token", "g1", "c1", Options{Days: 7, AllChannels: true})
	if err != nil {
		t.Fatal(err)
	}
	transcript := Transcript(got)
	if strings.Contains(transcript, "極秘") || strings.Contains(transcript, "幹部会") {
		t.Fatalf("非公開チャンネルを読んでいる:\n%s", transcript)
	}
	// 画面に並ぶ順（Position）で読む
	if len(got) != 2 || got[0].Channel != "機体班" || got[1].Channel != "電装班" {
		t.Fatalf("並び順が違う: %+v", got)
	}
}

// 横断のときに1つの権限不足で全部が失敗しては使い物にならない
func TestGatherAllChannelsSkipsUnreadable(t *testing.T) {
	stub := &discordStub{
		channels: []Channel{
			{ID: "c1", Type: channelTypeText, Name: "機体班", Position: 0},
			{ID: "入れていない", Type: channelTypeText, Name: "他班", Position: 1},
		},
		pages: map[string][][]Message{"c1": {page("機体", 2, time.Minute)}},
	}
	stub.serve(t)

	got, err := Gather(t.Context(), "token", "g1", "c1", Options{Days: 7, AllChannels: true})
	if err != nil {
		t.Fatalf("1つ読めないだけで全部失敗した: %v", err)
	}
	if len(got) != 1 || got[0].Channel != "機体班" {
		t.Fatalf("読めるチャンネルまで落ちている: %+v", got)
	}
}

func TestGatherAllChannelsNeedsGuild(t *testing.T) {
	if _, err := Gather(t.Context(), "token", "", "c1", Options{AllChannels: true}); err != ErrNotInGuild {
		t.Fatalf("DMで横断を通した: %v", err)
	}
}

// 1チャンネル指定で読めなければ、そのまま伝える（横断と違って黙って空にしない）
func TestGatherReportsNoAccess(t *testing.T) {
	stub := &discordStub{pages: map[string][][]Message{}}
	stub.serve(t)

	if _, err := Gather(t.Context(), "token", "g1", "c1", Options{Days: 7}); err != ErrNoAccess {
		t.Fatalf("権限不足を伝えていない: %v", err)
	}
}

// **上限は要る。** 日数を大きく指定されても、リクエストと送信量が青天井にならない
func TestGatherStopsAtRequestLimit(t *testing.T) {
	pages := make([][]Message, 200)
	for i := range pages {
		pages[i] = page(fmt.Sprintf("p%d", i), pageSize, time.Duration(i)*time.Minute)
	}
	stub := &discordStub{pages: map[string][][]Message{"c1": pages}}
	stub.serve(t)

	got, err := Gather(t.Context(), "token", "g1", "c1", Options{Days: MaxDays})
	if err != nil {
		t.Fatal(err)
	}
	if total := CountMessages(got); total > MaxMessages {
		t.Fatalf("発言数の上限を超えた: %d件", total)
	}
	if stub.requests > MaxRequests {
		t.Fatalf("リクエスト数の上限を超えた: %d回", stub.requests)
	}
}

// **読んだ範囲を先頭に書く。** 会話ログを上流へ送る機能なので、
// 何が送られたかがチャンネルに残る文言そのもので分かるようにする
func TestFormatRecapStatesScope(t *testing.T) {
	got := FormatRecap(RecapScope(7, 42, 3, 1), "決まったこと\n- 日程は8月")

	for _, want := range []string{"このチャンネル", "過去7日", "42件", "3人", "引き継ぎ資料は参照していません"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q が無い:\n%s", want, got)
		}
	}
	if strings.Contains(got, "参照\n・") {
		t.Fatalf("出典欄を作っている:\n%s", got)
	}
}

func TestRecapScopeNamesCrossChannel(t *testing.T) {
	got := RecapScope(30, 800, 12, 5)
	if !strings.Contains(got, "公開チャンネル5件") {
		t.Fatalf("横断であることが分からない: %s", got)
	}
}

func TestFormatRecapFitsMessageLimit(t *testing.T) {
	got := FormatRecap(RecapScope(7, 100, 9, 1), strings.Repeat("あ", 5000))
	if length := len([]rune(got)); length > MessageLimit {
		t.Fatalf("上限を超えている: %d文字", length)
	}
	if !strings.Contains(got, "省略しました") {
		t.Fatal("省略したことを伝えていない")
	}
}
