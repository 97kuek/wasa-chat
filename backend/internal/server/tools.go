package server

import (
	"net/http"

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
func (s *Server) handleTools(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.tools())
}

// allowedTools は画面から届いた参照先のうち、**サーバー側で使えるものだけ**を返す。
//
// 画面が古い・設定が外れた・知らない名前が来た、のいずれでも黙って落とす。
// 使えないものを通すと、参照したつもりで参照していない回答になる。
func (s *Server) allowedTools(requested []string) []string {
	available := map[string]bool{}
	for _, tool := range s.tools() {
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

func (s *Server) tools() []Tool {
	drive := Tool{
		ID:          pipeline.ToolDrive,
		Name:        "共有ドライブ",
		Description: "Wikiに書かれていない議事録・設計メモも読みます",
		Available:   s.live.Current().DriveTOC != "",
	}
	if !drive.Available {
		drive.Reason = "索引に共有ドライブの資料が入っていません"
	}

	discord := Tool{
		ID:          pipeline.ToolDiscord,
		Name:        "Discord検索",
		Description: "公開チャンネルの会話を読んで答えます",
	}
	if s.cfg.DiscordBotToken == "" {
		discord.Reason = "DISCORD_BOT_TOKEN が未設定です"
	} else {
		discord.Available = true
	}
	return []Tool{drive, discord}
}
