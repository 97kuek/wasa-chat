package server

import (
	"context"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/calendar"
	"github.com/97kuek/wasa-chat/backend/internal/discord"
	"github.com/97kuek/wasa-chat/backend/internal/pipeline"
)

// Tool は設定画面の「外部サービス連携」から足せる参照先。
//
// **引き継ぎ資料（Wiki・公式サイト・フライトシミュレータ）はここに出ない。**
// それらは常に読むので、ここは「それ以外の置き場所」だけを並べる。
//
// 画面へ出す形（利用者ごとのオン・オフや連携中のサーバー）は settings.go で
// 組み立てる。ここは**サーバー側で使えるかどうか**だけを持つ。
type Tool struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Available   bool   `json:"available"`
	// Servers は Discord のときだけ入る。**代ごとにサーバーが変わる**ので、
	// 設定で1つに固定せず、ボットが入っている先から選んでもらう
	Servers []ToolServer `json:"servers,omitempty"`
	// Reason は Available が false のときだけ入る。なぜ使えないか。
	//
	// ⚠️ **使えないものを黙って消さない。** 一覧から消すと「無い機能」に見え、
	// 設定すれば使えることが管理者にも伝わらない。
	Reason string `json:"reason,omitempty"`
}

// ToolServer は選べるDiscordのサーバー。
//
// ⚠️ **名前だけを並べない。** 「WASA 41代」「WASA 42代」と4つ並んでも、
// 初めて設定する人には**どれが自分のいるサーバーか分からない**（2026-09-14の指摘）。
// 見分けが付く材料と、選んだときに何が読まれるかを一緒に返す。
type ToolServer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Icon はDiscordのサーバーアイコン（data URI）。無ければ画面が頭文字で描く
	Icon string `json:"icon,omitempty"`
	// Members はおおよその参加人数。0なら取れなかった（出さない）
	Members int `json:"members,omitempty"`
	// Channels は**実際に検索する**公開チャンネルの数。
	// DISCORD_SEARCH_CHANNELS で絞っていれば、その数になる
	Channels int `json:"channels,omitempty"`
}

// mayUseDrive は共有ドライブを読んでよい利用者かを返す。
//
// ⚠️ **共有ドライブは部内資料より緩い場所である。** Wikiに書かない人が置いた
// 資料が入るため、誰が読めるかを個別に決める（2026-09-13にPMが判断）。
// 管理者は共有フォルダの中身を決める側なので、指定しなくても読める。
//
// Discordは公開チャンネルだけを読むので、ここでは絞らない。
func (s *Server) mayUseDrive(ctx context.Context, user string) bool {
	if user == "" {
		return false
	}
	if s.isOwner(user) {
		return true // 主管理者は設定に書いてある。保存先を見るまでもない
	}
	// **読み取りは1回にまとめる。** 共同管理者かどうかも参照先の許可も同じ文書に
	// 入っているので、isAdmin を経由すると同じ文書を2回読むことになる
	role, ok, err := s.state.GetAdminRole(ctx, s.userKey(user))
	if err != nil {
		// **読めないときは許可しない。** 失敗を「許可」に倒すと、
		// 一時的な障害で部内資料が広く読まれる
		log.Printf("参照先の許可を読み込めません: %v", err)
		return false
	}
	if !ok || role.Username != user {
		return false
	}
	// 管理者は共有フォルダの中身を決める側なので、指定しなくても読める
	return role.Role == "co_admin" || slices.Contains(role.Tools, pipeline.ToolDrive)
}

// allowedTools は画面から届いた参照先のうち、**サーバー側で使えるものだけ**を返す。
//
// 画面が古い・設定が外れた・知らない名前が来た、のいずれでも黙って落とす。
// 使えないものを通すと、参照したつもりで参照していない回答になる。
func (s *Server) allowedTools(ctx context.Context, user string, requested []string) []string {
	available := map[string]bool{}
	for _, tool := range s.tools(ctx, user) {
		available[tool.ID] = tool.Available
	}
	allowed := make([]string, 0, len(requested))
	for _, tool := range requested {
		if available[tool] {
			allowed = append(allowed, tool)
		}
	}
	return allowed
}

func (s *Server) tools(ctx context.Context, user string) []Tool {
	drive := Tool{
		ID:   pipeline.ToolDrive,
		Name: "共有ドライブ",
		// **短くする。** 説明が3行に折り返すと、その項目だけ背が高くなって
		// 一覧が上へ伸びる（2026-09-13の指摘）。詳細はサポートページにある
		Description: "wasa.birdman@gmail.com のドライブの引き継ぎ資料",
	}
	switch {
	case s.live.Current().DriveTOC == "":
		drive.Reason = "索引に共有ドライブの資料が入っていません"
	case !s.mayUseDrive(ctx, user):
		drive.Reason = "管理者の許可が要ります"
	default:
		drive.Available = true
	}

	discordTool := Tool{
		ID:          pipeline.ToolDiscord,
		Name:        "Discord検索",
		Description: "公開チャンネルの会話を読んで答えます",
	}
	guilds := s.searchableGuilds(ctx)
	switch {
	case s.cfg.DiscordBotToken == "":
		discordTool.Reason = "DISCORD_BOT_TOKEN が未設定です"
	case len(guilds) == 0:
		discordTool.Reason = "WASA ChatのボットがどのDiscordサーバーにも入っていません"
	default:
		discordTool.Available = true
		for _, guild := range guilds {
			discordTool.Servers = append(discordTool.Servers, ToolServer{ID: guild.ID, Name: guild.Name})
		}
	}
	calendarTool := Tool{
		ID:          pipeline.ToolCalendar,
		Name:        "カレンダー",
		Description: "部の予定表から、これからの予定と終わった予定を読みます",
	}
	if len(s.cfg.CalendarIDs) == 0 {
		calendarTool.Reason = "CALENDAR_IDS が未設定です"
	} else {
		calendarTool.Available = true
	}
	return []Tool{drive, discordTool, calendarTool}
}

// discordSearchTimeout は画面からのDiscord検索に使える時間。
//
// **回答そのものを遅らせない。** ここで粘っても、資料からの回答は作れる。
// 取れなければ資料だけで答える。
const discordSearchTimeout = 20 * time.Second

// MaxSearchGuilds は1回の質問で検索するサーバーの数。
// 代が進むほど増えるので、「すべて」を選ばれたときの歯止め。
const MaxSearchGuilds = discord.MaxSearchGuilds

// searchDiscordFor は質問に関係する会話を拾って、回答の材料にする。
//
// ⚠️ **読む先は公開チャンネルだけ。** 画面の利用者はWikiアカウントであって、
// Discordの権限とは無関係である。ボットが見える範囲をそのまま渡すと、
// **Discordに入っていない人が非公開チャンネルの中身を読める**（docs/09 A-12）。
//
// DISCORD_SEARCH_CHANNELS を設定すると、さらにそのチャンネルだけへ絞れる。
//
// guildIDs は設定画面で**利用者が連携したサーバー**。空なら何も読まない
// （searchTargets 参照）。
func (s *Server) searchDiscordFor(ctx context.Context, question, previous string, guildIDs []string) (transcript, note string, sources []pipeline.Source) {
	if s.cfg.DiscordBotToken == "" {
		return "", "", nil
	}
	// **前の質問も見る。**「最近のは？」だけでは何を探すか決まらない。
	// 語は複数返る（1つのクエリにまとめるとAND条件で当たらなくなる）
	terms := discord.SearchTermsFor(question, previous)
	if len(terms) == 0 {
		return "", "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, discordSearchTimeout)
	defer cancel()

	targets := s.searchTargets(ctx, guildIDs)
	if len(targets) == 0 {
		return "", "", nil
	}

	var logs []discord.ChannelLog
	var servers []string
	for _, guild := range targets {
		allowed := s.searchableChannels(ctx, guild.ID)
		if len(allowed) == 0 {
			continue
		}
		found, err := discord.Search(ctx, s.cfg.DiscordBotToken, guild.ID, terms, allowed)
		if err != nil {
			// **黙って続ける。** 1つのサーバーが読めなくても、ほかは読める。
			// 会話が拾えないことは、質問に答えられないことを意味しない
			log.Printf("Discord（%s）の検索に失敗しました: %v", guild.Name, err)
			continue
		}
		if len(found) == 0 {
			continue
		}
		// **どのサーバーの話かを見出しに残す。** 代が違えば別の話である
		for i := range found {
			found[i].Channel = guild.Name + " / " + found[i].Channel
			// ⚠️ **出典を作る。** 会話は索引のページではないが、リンクは作れる。
			// 作らないと「Discordの会話によれば」と答えながら、どの発言が
			// 根拠なのか後から追えない（2026-09-13に本番で発覚）
			sources = append(sources, discordSource(guild.ID, found[i]))
		}
		logs = append(logs, found...)
		servers = append(servers, guild.Name)
	}

	transcript = discord.Transcript(logs)
	if strings.TrimSpace(transcript) == "" {
		return "", "", nil
	}
	note = discord.SearchScopeAcross(terms, servers, len(logs), discord.CountMessages(logs))
	return transcript, note, sources
}

// discordSource は読んだチャンネルを出典に直す。
//
// 一致した発言があればその発言へ、無ければチャンネルの先頭へ飛ばす。
// 最終更新には、そのチャンネルで読んだ**いちばん新しい発言の日付**を入れる
// （資料の「最終更新」と同じ意味で使えるように）。
func discordSource(guildID string, log discord.ChannelLog) pipeline.Source {
	url := discord.ChannelURL(guildID, log.ID)
	if hit, ok := log.FirstHit(); ok {
		url = discord.MessageURL(guildID, log.ID, hit.ID)
	}
	newest := ""
	for _, message := range log.Messages {
		if at := message.Timestamp.In(japanTime).Format("2006-01-02"); at > newest {
			newest = at
		}
	}
	return pipeline.Source{
		Title:      "#" + log.Channel,
		URL:        url,
		LastEdited: newest,
		Origin:     pipeline.ToolDiscord,
	}
}

// searchTargets は検索するサーバーを決める。
//
// ⚠️ **指定が無ければ何も読まない。** 以前は指定が無いと名前順で先頭3つを
// 横断していたが、どの代の会話を読んだのかが回答からしか分からなかった。
// いまは設定画面で**連携したサーバーだけ**を読む（2026-09-14に変更）。
//
// 連携時にも上限を掛けているが（connectableGuilds）、ここでも掛ける。
// 古い設定や保存先の書き換えで、上限を超えた一覧が入っていることがある。
func (s *Server) searchTargets(ctx context.Context, guildIDs []string) []discord.Guild {
	if len(guildIDs) == 0 {
		return nil
	}
	guilds := s.searchableGuilds(ctx)
	targets := make([]discord.Guild, 0, len(guildIDs))
	for _, guild := range guilds {
		// ⚠️ **連携した一覧に無いものは読まない。** ボットが外された、
		// DISCORD_GUILD_IDS で絞られた、のどちらでもここで落ちる
		if !slices.Contains(guildIDs, guild.ID) {
			continue
		}
		if len(targets) == MaxSearchGuilds {
			break
		}
		targets = append(targets, guild)
	}
	return targets
}

// searchableGuilds は画面から検索してよいサーバーを返す。
//
// DISCORD_GUILD_IDS を設定すると、そのサーバーだけへ絞れる。未設定なら
// ボットが入っている先すべて（招待するのは管理者なので、既定はこれでよい）。
func (s *Server) searchableGuilds(ctx context.Context) []discord.Guild {
	all := discord.ListGuilds(ctx, s.cfg.DiscordBotToken)
	if len(s.cfg.DiscordGuildIDs) == 0 {
		return all
	}
	allowed := make([]discord.Guild, 0, len(all))
	for _, guild := range all {
		if slices.Contains(s.cfg.DiscordGuildIDs, guild.ID) {
			allowed = append(allowed, guild)
		}
	}
	return allowed
}

// searchableChannels は画面から読んでよいチャンネルを返す。
func (s *Server) searchableChannels(ctx context.Context, guildID string) []discord.Channel {
	open := discord.PublicChannels(ctx, s.cfg.DiscordBotToken, guildID)
	if len(s.cfg.DiscordSearchChannels) == 0 {
		return open
	}
	channels := make([]discord.Channel, 0, len(open))
	for _, channel := range open {
		if slices.Contains(s.cfg.DiscordSearchChannels, channel.ID) {
			channels = append(channels, channel)
		}
	}
	return channels
}

// calendarTimeout は予定を取りに行く上限。**回答そのものを遅らせない。**
// 取れなければ資料だけで答える。
const calendarTimeout = 10 * time.Second

// readCalendar は部の予定を読む。
//
// ⚠️ **索引には入れない。** 予定は時間で意味が変わる（「次のTFはいつ」の答えは
// 今日が何日かで変わる）。質問のたびに取りに行く（internal/calendar を参照）。
func (s *Server) readCalendar(ctx context.Context) (transcript, note string, sources []pipeline.Source) {
	if len(s.cfg.CalendarIDs) == 0 {
		return "", "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, calendarTimeout)
	defer cancel()

	events, err := calendar.Fetch(ctx, s.cfg.CalendarIDs)
	if err != nil {
		// **黙って資料だけで答える。** 予定が読めないことは、質問に答えられない
		// ことを意味しない
		log.Printf("カレンダーを読めません（資料だけで答えます）: %v", err)
		return "", "", nil
	}
	transcript = calendar.Transcript(events, time.Now())
	if strings.TrimSpace(transcript) == "" {
		return "", "", nil
	}
	names := calendar.CalendarNames(events)
	for _, name := range names {
		sources = append(sources, pipeline.Source{
			Title:  name,
			URL:    "https://calendar.google.com/",
			Origin: pipeline.ToolCalendar,
		})
	}
	return transcript, calendar.ScopeNote(len(events), names), sources
}
