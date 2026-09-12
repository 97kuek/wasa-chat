package server

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/assistant"
)

// handleUpdateProfileIcon はログイン中の本人の画像だけを更新する。
// 利用者キーを本文から受け取らずCookieから決め、他人の画像を書き換える経路を作らない。
func (s *Server) handleUpdateProfileIcon(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Icon string `json:"icon"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProfileBodyBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "画像を読み込めませんでした"})
		return
	}
	if err := assistant.ValidateIcon(body.Icon); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	user, _ := s.currentUser(r)
	key := s.userKey(user)
	// 古いCookieから直接更新APIへ来ても保存先を用意できるよう、先にプロフィールを更新する。
	if err := s.state.SaveUserProfile(r.Context(), key, user, time.Now().UTC()); err != nil {
		log.Printf("利用者プロフィールの保存に失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "画像を保存できませんでした"})
		return
	}
	if err := s.state.SaveUserIcon(r.Context(), key, body.Icon); err != nil {
		log.Printf("利用者画像の保存に失敗: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "画像を保存できませんでした"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"icon": body.Icon})
}
