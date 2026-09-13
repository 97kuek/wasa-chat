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
	// Reason は Available が false のときだけ入る。なぜ使えないか。
	//
	// ⚠️ **使えないものを黙って消さない。** 一覧から消すと「無い機能」に見え、
	// 設定すれば使えることが管理者にも伝わらない。
	Reason string `json:"reason,omitempty"`
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
	if s.isAdmin(ctx, user) {
		return true
	}
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
	return slices.Contains(role.Tools, pipeline.ToolDrive)
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
		Description: "Wikiに書かれていない議事録・設計メモも読みます",
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
	switch {
	case s.cfg.DiscordBotToken == "":
		discordTool.Reason = "DISCORD_BOT_TOKEN が未設定です"
	case s.cfg.DiscordGuildID == "":
		discordTool.Reason = "DISCORD_GUILD_ID が未設定です"
	default:
		discordTool.Available = true
	}
	return []Tool{drive, discordTool}
}

// discordSearchTimeout は画面からのDiscord検索に使える時間。
//
// **回答そのものを遅らせない。** ここで粘っても、資料からの回答は作れる。
// 取れなければ資料だけで答える。
const discordSearchTimeout = 20 * time.Second

// searchDiscordFor は質問に関係する会話を拾って、回答の材料にする。
//
// ⚠️ **読む先は公開チャンネルだけ。** 画面の利用者はWikiアカウントであって、
// Discordの権限とは無関係である。ボットが見える範囲をそのまま渡すと、
// **Discordに入っていない人が非公開チャンネルの中身を読める**（docs/09 A-12）。
//
// DISCORD_SEARCH_CHANNELS を設定すると、さらにそのチャンネルだけへ絞れる。
func (s *Server) searchDiscordFor(ctx context.Context, question string) (transcript, note string) {
	if s.cfg.DiscordBotToken == "" || s.cfg.DiscordGuildID == "" {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(ctx, discordSearchTimeout)
	defer cancel()

	allowed := s.searchableChannels(ctx)
	if len(allowed) == 0 {
		return "", ""
	}
	logs, err := discord.Search(ctx, s.cfg.DiscordBotToken, s.cfg.DiscordGuildID, question, allowed)
	if err != nil {
		// **黙って資料だけで答える。** 会話が拾えないことは、質問に答えられない
		// ことを意味しない。ここで質問ごと失敗させるほうが損
		log.Printf("Discordの検索に失敗しました（資料だけで答えます）: %v", err)
		return "", ""
	}
	transcript = discord.Transcript(logs)
	if strings.TrimSpace(transcript) == "" {
		return "", ""
	}
	return transcript, discord.SearchScope(question, len(logs), discord.CountMessages(logs))
}

// searchableChannels は画面から読んでよいチャンネルを返す。
func (s *Server) searchableChannels(ctx context.Context) []discord.Channel {
	open := discord.ScopeChoices(ctx, s.cfg.DiscordBotToken, s.cfg.DiscordGuildID, "")
	allowList := map[string]bool{}
	for _, id := range s.cfg.DiscordSearchChannels {
		allowList[id] = true
	}
	channels := make([]discord.Channel, 0, len(open))
	for _, choice := range open {
		if choice.Value == discord.ScopeAllChannels {
			continue
		}
		if len(allowList) > 0 && !allowList[choice.Value] {
			continue
		}
		channels = append(channels, discord.Channel{ID: choice.Value, Name: strings.TrimPrefix(choice.Name, "#")})
	}
	return channels
}
