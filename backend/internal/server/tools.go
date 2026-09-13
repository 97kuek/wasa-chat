package server

import (
	"context"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/discord"
	"github.com/97kuek/wasa-chat/backend/internal/pipeline"
)

// Tool は入力欄の「+」から足せる参照先。
//
// **引き継ぎ資料（Wiki・公式サイト・フライトシミュレータ）はここに出ない。**
// それらは常に読むので、ここは「それ以外の置き場所」だけを並べる。
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
type ToolServer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// handleTools は参照先の一覧を返す。
//
// **画面に固定で並べない。** 未設定のものが押せてしまうと「押したのに効かない」
// になるため、使えるかどうかはサーバーが決めて返す。
// 使えるかどうかは**利用者ごとに違う**（共有ドライブは許可制）。
func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	user, _ := s.currentUser(r)
	writeJSON(w, http.StatusOK, s.tools(r.Context(), user))
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
		ID:          pipeline.ToolDrive,
		Name:        "共有ドライブ",
		Description: "wasa.birdman@gmail.com のドライブにある引き継ぎ資料。Wikiに書かれていない議事録・設計を読みます",
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
	return []Tool{drive, discordTool}
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
// searchDiscordFor は質問に関係する会話を拾う。guildID が空なら、新しい代から
// MaxSearchGuilds 件まで横断する。
func (s *Server) searchDiscordFor(ctx context.Context, question, guildID string) (transcript, note string) {
	if s.cfg.DiscordBotToken == "" {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(ctx, discordSearchTimeout)
	defer cancel()

	targets := s.searchTargets(ctx, guildID)
	if len(targets) == 0 {
		return "", ""
	}

	var logs []discord.ChannelLog
	var servers []string
	for _, guild := range targets {
		allowed := s.searchableChannels(ctx, guild.ID)
		if len(allowed) == 0 {
			continue
		}
		found, err := discord.Search(ctx, s.cfg.DiscordBotToken, guild.ID, question, allowed)
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
		}
		logs = append(logs, found...)
		servers = append(servers, guild.Name)
	}

	transcript = discord.Transcript(logs)
	if strings.TrimSpace(transcript) == "" {
		return "", ""
	}
	return transcript, discord.SearchScopeAcross(question, servers, len(logs), discord.CountMessages(logs))
}

// searchTargets は検索するサーバーを決める。
//
// 指定が無ければ横断するが、**代が進むほどサーバーが増える**ので上限を設ける。
// 一覧は名前順なので、新しい代が上に来る名前付けをしてもらう前提にしない。
// 指定があればそれだけ（許可した一覧に無ければ何もしない）。
func (s *Server) searchTargets(ctx context.Context, guildID string) []discord.Guild {
	guilds := s.searchableGuilds(ctx)
	if guildID == "" {
		if len(guilds) > MaxSearchGuilds {
			return guilds[:MaxSearchGuilds]
		}
		return guilds
	}
	for _, guild := range guilds {
		if guild.ID == guildID {
			return []discord.Guild{guild}
		}
	}
	return nil
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
