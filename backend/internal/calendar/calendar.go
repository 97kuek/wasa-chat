// Package calendar は部の予定を読む。
//
// # なぜ索引へ入れないのか
//
// 資料（Wiki・公式サイト・共有ドライブ）は索引に入れて検索するが、**予定は
// 入れない**。予定は時間で意味が変わるからである。
//
//   - 「次のテストフライトはいつ？」の答えは、今日が何日かで変わる
//   - 索引は変更を検知して作り直す仕組みで、早くても1時間は古い
//   - 終わった予定と、これからの予定は、同じ文字列でも意味が違う
//
// そこで Discord の会話と同じく、**質問のたびに取りに行く**。件数が少ないので
// 1リクエストで済み、目次も要らない。
//
// # 誰の権限で読むのか
//
// Cloud Run のサービスIDで読む（Application Default Credentials）。
// ⚠️ **鍵（JSON）は作らない。** 対象のカレンダーを、そのサービスアカウントへ
// 「予定の表示」権限で共有してもらう。共有していないカレンダーは読めない。
//
// ⚠️ **カレンダーIDは設定で明示する。** `primary` を避けて部の予定表だけを
// 指定すれば、同じアカウントの私的な予定は読まない（docs/09 A-13）。
package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2/google"
)

// Scope は読み取り専用。書き込みは求めない。
const Scope = "https://www.googleapis.com/auth/calendar.readonly"

// 取りに行く範囲。
//
// **過ぎた予定も少し見る。** 「この前のTFはいつだった？」に答えられないと、
// 引き継ぎの道具として片手落ちになる。先は3か月あれば、代の中の計画は入る。
const (
	PastDays   = 60
	FutureDays = 90
	// MaxEvents は1回で読む予定の数。多すぎるとプロンプトが埋まる
	MaxEvents = 120
)

// cacheTTL は予定を覚えておく時間。
//
// 予定は分単位では変わらない。**質問のたびに取りに行くと、連続した質問で
// 同じ内容を何度も取ることになる。** 5分なら、直したことにその場で気づける。
const cacheTTL = 5 * time.Minute

var apiBase = "https://www.googleapis.com/calendar/v3"

// Event は1件の予定。必要な項目だけ拾う。
type Event struct {
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Location    string `json:"location"`
	HTMLLink    string `json:"htmlLink"`
	Start       when   `json:"start"`
	End         when   `json:"end"`
	// Calendar は読み込んだあとに入れる。どの予定表の予定かを出典に使う
	Calendar string `json:"-"`
}

type when struct {
	// Date は終日の予定（2026-08-01）、DateTime は時刻つき
	Date     string    `json:"date"`
	DateTime time.Time `json:"dateTime"`
}

func (w when) at() (time.Time, bool) {
	if !w.DateTime.IsZero() {
		return w.DateTime, true
	}
	if w.Date == "" {
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation("2006-01-02", w.Date, japanTime)
	return parsed, err == nil
}

// allDay は終日の予定かを返す。時刻を出すかどうかが変わる。
func (w when) allDay() bool { return w.DateTime.IsZero() && w.Date != "" }

var japanTime = time.FixedZone("JST", 9*60*60)

type cached struct {
	events []Event
	at     time.Time
}

var (
	cacheMu sync.Mutex
	cache   = map[string]cached{}
)

// Fetch は指定したカレンダーの予定を、今日を挟んだ範囲で取る。
//
// 読めないカレンダーは飛ばす。1つ読めないだけで予定が全部出ないほうが困る。
func Fetch(ctx context.Context, calendarIDs []string) ([]Event, error) {
	if len(calendarIDs) == 0 {
		return nil, ErrNotConfigured
	}
	key := strings.Join(calendarIDs, ",")
	cacheMu.Lock()
	if hit, ok := cache[key]; ok && time.Since(hit.at) < cacheTTL {
		cacheMu.Unlock()
		return hit.events, nil
	}
	cacheMu.Unlock()

	client, err := google.DefaultClient(ctx, Scope)
	if err != nil {
		return nil, fmt.Errorf("カレンダーの資格情報を取れません: %w", err)
	}
	now := time.Now()
	var events []Event
	for _, id := range calendarIDs {
		found, err := list(ctx, client, id, now)
		if err != nil {
			// 読めないカレンダーは飛ばす。ほかは読める
			continue
		}
		events = append(events, found...)
	}
	sort.Slice(events, func(a, b int) bool {
		left, _ := events[a].Start.at()
		right, _ := events[b].Start.at()
		return left.Before(right)
	})
	if len(events) > MaxEvents {
		events = events[:MaxEvents]
	}

	cacheMu.Lock()
	cache[key] = cached{events: events, at: time.Now()}
	cacheMu.Unlock()
	return events, nil
}

func list(ctx context.Context, client *http.Client, calendarID string, now time.Time) ([]Event, error) {
	values := url.Values{}
	values.Set("timeMin", now.AddDate(0, 0, -PastDays).Format(time.RFC3339))
	values.Set("timeMax", now.AddDate(0, 0, FutureDays).Format(time.RFC3339))
	// **繰り返しの予定を展開する。** 展開しないと「毎週の定例」が1件に見え、
	// 次がいつかを答えられない
	values.Set("singleEvents", "true")
	values.Set("orderBy", "startTime")
	values.Set("maxResults", fmt.Sprint(MaxEvents))

	target := fmt.Sprintf("%s/calendars/%s/events?%s", apiBase, url.PathEscape(calendarID), values.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 256))
		return nil, fmt.Errorf("カレンダー %s を読めません（%d）: %s", calendarID, res.StatusCode, detail)
	}
	var payload struct {
		Summary string  `json:"summary"`
		Items   []Event `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, err
	}
	name := payload.Summary
	if name == "" {
		name = calendarID
	}
	for i := range payload.Items {
		payload.Items[i].Calendar = name
	}
	return payload.Items, nil
}

// Transcript は予定を、回答の材料として渡せる文字列にする。
//
// **今日を境に「これまで」と「これから」で分ける。** 並べただけだと、
// モデルが終わった予定を「予定です」と書く。日付の比較をモデルに任せない。
func Transcript(events []Event, now time.Time) string {
	var past, future []string
	today := now.In(japanTime)
	for _, event := range events {
		line := format(event)
		if line == "" {
			continue
		}
		start, ok := event.Start.at()
		if ok && start.Before(today) {
			past = append(past, line)
		} else {
			future = append(future, line)
		}
	}
	var out strings.Builder
	if len(past) > 0 {
		out.WriteString("## 終わった予定（古い順）\n")
		out.WriteString(strings.Join(past, "\n"))
		out.WriteString("\n")
	}
	if len(future) > 0 {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString("## これからの予定（近い順）\n")
		out.WriteString(strings.Join(future, "\n"))
		out.WriteString("\n")
	}
	return out.String()
}

func format(event Event) string {
	start, ok := event.Start.at()
	if !ok || strings.TrimSpace(event.Summary) == "" {
		return ""
	}
	stamp := start.In(japanTime).Format("2006-01-02(Mon)")
	if !event.Start.allDay() {
		stamp += " " + start.In(japanTime).Format("15:04")
		if end, ok := event.End.at(); ok {
			stamp += "-" + end.In(japanTime).Format("15:04")
		}
	}
	line := fmt.Sprintf("- %s %s", stamp, strings.TrimSpace(event.Summary))
	if place := strings.TrimSpace(event.Location); place != "" {
		line += "（場所: " + place + "）"
	}
	// 説明は長いことがある。予定の一覧として読める長さで切る
	if note := flatten(event.Description); note != "" {
		line += " / " + note
	}
	return line
}

// descriptionLimit は予定の説明を何文字まで載せるか。
// 予定表の説明に議事録を貼る人がいるので、上限は要る。
const descriptionLimit = 120

func flatten(text string) string {
	text = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(text))
	if runes := []rune(text); len(runes) > descriptionLimit {
		return string(runes[:descriptionLimit]) + "…"
	}
	return text
}

// Scope はこの範囲を読んだ、という説明。画面と出典に出す。
func ScopeNote(events int, calendars []string) string {
	return fmt.Sprintf("%s の予定（%d日前〜%d日後）から%d件を読みました",
		strings.Join(calendars, "・"), PastDays, FutureDays, events)
}

// CalendarNames は読み込んだ予定表の名前を重複なく返す。
func CalendarNames(events []Event) []string {
	seen := map[string]bool{}
	var names []string
	for _, event := range events {
		if event.Calendar == "" || seen[event.Calendar] {
			continue
		}
		seen[event.Calendar] = true
		names = append(names, event.Calendar)
	}
	sort.Strings(names)
	return names
}

// ErrNotConfigured はカレンダーが設定されていないことを表す。
var ErrNotConfigured = fmt.Errorf("カレンダーが設定されていません")

// ResetCacheForTest はテスト用。本番からは呼ばない。
func ResetCacheForTest(base string) {
	cacheMu.Lock()
	cache = map[string]cached{}
	cacheMu.Unlock()
	if base != "" {
		apiBase = base
	}
}
