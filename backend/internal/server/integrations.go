package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/97kuek/wasa-chat/backend/internal/calendar"
	"github.com/97kuek/wasa-chat/backend/internal/pipeline"
)

// discordInvitePermissions は招待URLで求める権限。
//
// **必要なものだけ。** 管理者権限は求めない。
//
//	1<<10 チャンネルを見る / 1<<11 メッセージを送る / 1<<16 メッセージ履歴を読む
const discordInvitePermissions = 1<<10 | 1<<11 | 1<<16

// Integration は外部サービスとの連携の状態。管理画面へ出す。
//
// **つながっているかどうかを画面で見えるようにする。** 環境変数を読める人しか
// 状態が分からない、という作りだと、代替わりのときに誰も直せない（docs/09 A）。
type Integration struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	// Summary はつながっているときの中身（「2サーバー」「155ファイル」など）
	Summary string `json:"summary,omitempty"`
	// NextStep はつながっていないときに、次にやること
	NextStep string `json:"nextStep,omitempty"`
	// ActionURL は押して進める先（Discordの招待URLなど）
	ActionURL string `json:"actionUrl,omitempty"`
	// ActionLabel は ActionURL の押しボタンの文言
	ActionLabel string `json:"actionLabel,omitempty"`
	// Detail は個別の内訳（Discordのサーバー名など）
	Detail []string `json:"detail,omitempty"`
	// ShareWith は共有相手に追加すべきアドレス（共有ドライブ用）
	ShareWith string `json:"shareWith,omitempty"`
}

func (s *Server) handleIntegrations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []Integration{
		s.discordIntegration(r.Context()),
		s.driveIntegration(),
		s.calendarIntegration(r.Context()),
	})
}

// discordInviteURL はボットをサーバーへ追加するURL。
//
// **画面から押せるようにする。** コマンドを打てる人しか追加できない作りだと、
// 代が替わって新しいサーバーを作ったときに使えなくなる（2026-09-13の指摘）。
func discordInviteURL(applicationID string) string {
	if applicationID == "" {
		return ""
	}
	return fmt.Sprintf(
		"https://discord.com/oauth2/authorize?client_id=%s&scope=bot%%20applications.commands&permissions=%d",
		applicationID, discordInvitePermissions)
}

func (s *Server) discordIntegration(ctx context.Context) Integration {
	found := Integration{ID: pipeline.ToolDiscord, Name: "Discord"}
	if s.cfg.DiscordBotToken == "" || s.cfg.DiscordAppID == "" {
		found.NextStep = "DISCORD_BOT_TOKEN と DISCORD_APP_ID を設定してください（docs/07 §5.5）。"
		return found
	}
	found.ActionURL = discordInviteURL(s.cfg.DiscordAppID)
	found.ActionLabel = "サーバーへ追加"

	guilds := s.searchableGuilds(ctx)
	if len(guilds) == 0 {
		found.NextStep = "WASA Chatのボットがどのサーバーにも入っていません。下のボタンから追加してください。"
		return found
	}
	found.Connected = true
	found.Summary = fmt.Sprintf("%dサーバーに接続しています", len(guilds))
	for _, guild := range guilds {
		found.Detail = append(found.Detail, guild.Name)
	}
	return found
}

func (s *Server) driveIntegration() Integration {
	found := Integration{
		ID:        pipeline.ToolDrive,
		Name:      "共有ドライブ",
		ShareWith: s.cfg.DriveServiceAccount,
	}
	pages := 0
	for _, page := range s.live.Current().Pages {
		if page.Source == pipeline.OriginDrive {
			pages++
		}
	}
	if pages == 0 {
		found.NextStep = "共有フォルダの共有相手に下のアドレスを閲覧者として追加し、" +
			"更新Jobへ DRIVE_FOLDER_IDS を設定してください（docs/07 §5.6）。"
		return found
	}
	found.Connected = true
	found.Summary = fmt.Sprintf("%d件の資料を取り込んでいます", pages)
	return found
}

func (s *Server) calendarIntegration(ctx context.Context) Integration {
	found := Integration{
		ID:        pipeline.ToolCalendar,
		Name:      "カレンダー",
		ShareWith: s.cfg.CalendarServiceAccount,
	}
	if len(s.cfg.CalendarIDs) == 0 {
		found.NextStep = "予定表をこのサービスアカウントへ「予定の表示」権限で共有し、" +
			"CALENDAR_IDS にカレンダーIDを設定してください（docs/07 §5.7）。"
		return found
	}
	// **管理画面を予定表の応答待ちにしない。** 読めなくても状態は出せる
	ctx, cancel := context.WithTimeout(ctx, calendarTimeout)
	defer cancel()

	events, err := calendar.Fetch(ctx, s.cfg.CalendarIDs)
	if err != nil {
		found.NextStep = "設定はありますが予定を読めていません。共有設定とカレンダーIDを確かめてください。"
		return found
	}
	// ⚠️ **「予定が0件」と「読めない」を混ぜない。** 以前は0件も未接続として
	// 扱っており、正しく設定できているのに「共有設定を確かめてください」と出た。
	// 設定した直後は範囲内に予定が無いことが普通にあるので、**直したばかりの
	// 設定を疑わせる**表示になる。読めたなら、つながっている
	found.Connected = true
	found.Detail = calendar.CalendarNames(events)
	if len(events) == 0 {
		found.Summary = fmt.Sprintf("読めていますが、%d日前〜%d日後に予定がありません",
			calendar.PastDays, calendar.FutureDays)
		return found
	}
	found.Summary = fmt.Sprintf("%d件の予定を読めています（%d日前〜%d日後）",
		len(events), calendar.PastDays, calendar.FutureDays)
	return found
}
