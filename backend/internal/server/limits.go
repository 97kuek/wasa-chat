package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTP境界の上限は画面と永続化の前提を守る値であり、ハンドラーへ直書きしない。
const (
	cookieName    = "wasa_session"
	sessionMaxAge = 30 * 24 * time.Hour

	maxSmallRequestBodyBytes = 4 << 10
	maxChatBodyBytes         = 1 << 20
	loginFailureDelay        = 300 * time.Millisecond

	maxChats              = 30
	maxChatIDBytes        = 64
	maxChatTitleRunes     = 80
	maxTurnsPerChat       = 100
	maxQuestionRunes      = 500
	maxAnswerRunes        = 300_000
	maxTurnErrorRunes     = 2_000
	maxSourcesPerTurn     = 100
	maxAssistantIDBytes   = 64
	maxAssistantNameRunes = 40
	maxSourceTitleRunes   = 300
	maxSourceURLBytes     = 4_000
	maxFutureClockSkew    = 5 * time.Minute

	maxFeedbackBodyBytes      = 128 << 10
	maxFeedbackClientIDBytes  = 128
	maxFeedbackReasons        = 5
	maxFeedbackCommentRunes   = 500
	maxFeedbackQuestionRunes  = 500
	maxFeedbackAnswerRunes    = 20_000
	maxFeedbackSources        = 8
	maxFeedbackAssistantID    = 64
	maxFeedbackAssistantRunes = 40
	maxFeedbackChatID         = 64
	maxFeedbackTurnIndex      = 99
	maxFeedbackSourceTitle    = 300
	maxFeedbackSourceURL      = 4_000
	maxStageTimingMS          = int64((30 * time.Minute) / time.Millisecond)

	maxAssistants         = 100
	maxAssistantBodyBytes = 256 << 10
	maxProfileBodyBytes   = 128 << 10

	maxConversationTurns         = 2
	maxConversationQuestionRunes = 500
	maxConversationAnswerRunes   = 2_000
	// 画像1枚（縮小後400KBまで）をbase64で載せる余地を持たせる。
	maxAskBodyBytes = 1 << 20
)

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("JSONの値が複数あります")
		}
		return err
	}
	return nil
}

func invalidJSONStatus(err error) int {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}
