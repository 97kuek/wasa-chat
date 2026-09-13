package calendar

import (
	"strings"
	"testing"
	"time"
)

func at(value string) when {
	parsed, _ := time.ParseInLocation("2006-01-02 15:04", value, japanTime)
	return when{DateTime: parsed}
}

func allDay(value string) when { return when{Date: value} }

// ⚠️ **今日を境に分けてから渡す。** 日付の比較をモデルに任せると、
// 終わった予定を「これからの予定です」と書く
func TestTranscriptSplitsAtToday(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, japanTime)
	events := []Event{
		{Summary: "1st TF", Start: allDay("2026-04-03"), End: allDay("2026-04-04")},
		{Summary: "全体ミーティング", Start: at("2026-09-20 19:00"), End: at("2026-09-20 21:00")},
	}
	got := Transcript(events, now)

	past := strings.Index(got, "## 終わった予定")
	future := strings.Index(got, "## これからの予定")
	if past < 0 || future < 0 || past > future {
		t.Fatalf("分けていない、または順番が違う:\n%s", got)
	}
	if strings.Index(got, "1st TF") > future {
		t.Fatalf("終わった予定がこれからに入っている:\n%s", got)
	}
	if strings.Index(got, "全体ミーティング") < future {
		t.Fatalf("これからの予定が終わったほうに入っている:\n%s", got)
	}
}

// ⚠️ **今日の予定を「終わった」と書かない。** 開始時刻で分けていたころは、
// 今日の終日の予定が（開始が0時なので）昼には終わった扱いになり、
// 進行中の予定も「終わった予定」に並んでいた
func TestTranscriptKeepsTodayInFuture(t *testing.T) {
	now := time.Date(2026, 9, 13, 15, 0, 0, 0, japanTime)
	events := []Event{
		// 終日の予定。Googleの end.date は翌日（終わりを含まない）
		{Summary: "鳥人間コンテスト", Start: allDay("2026-09-13"), End: allDay("2026-09-14")},
		// いま進行中
		{Summary: "荷重試験", Start: at("2026-09-13 14:00"), End: at("2026-09-13 17:00")},
		// 本当に終わっている
		{Summary: "朝の打ち合わせ", Start: at("2026-09-13 09:00"), End: at("2026-09-13 10:00")},
	}
	got := Transcript(events, now)

	future := strings.Index(got, "## これからの予定")
	if future < 0 {
		t.Fatalf("これからの予定が1件も無い:\n%s", got)
	}
	for _, want := range []string{"鳥人間コンテスト", "荷重試験"} {
		if strings.Index(got, want) < future {
			t.Fatalf("%s を終わった予定に入れている:\n%s", want, got)
		}
	}
	if at := strings.Index(got, "朝の打ち合わせ"); at < 0 || at > future {
		t.Fatalf("終わった予定をこれからに入れている:\n%s", got)
	}
}

// ⚠️ **枠が足りないときに捨てるのは古いほう。** 開始の早い順に先頭から
// 残していたころは、予定が多い時期ほど「これからの予定」が丸ごと消えた。
// 「次のTFはいつ？」は、部が忙しいときにこそ聞かれる
func TestTrimKeepsFutureEvents(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, japanTime)
	var events []Event
	for i := 0; i < MaxEvents; i++ {
		events = append(events, Event{
			Summary: "過去の定例",
			Start:   at("2026-08-01 10:00"), End: at("2026-08-01 11:00"),
		})
	}
	events = append(events, Event{
		Summary: "次のテストフライト",
		Start:   at("2026-10-01 05:00"), End: at("2026-10-01 12:00"),
	})

	got := trim(events, now)
	if len(got) != MaxEvents {
		t.Fatalf("上限に収めていない: %d件", len(got))
	}
	if !strings.Contains(Transcript(got, now), "次のテストフライト") {
		t.Fatal("これからの予定を捨てている")
	}
}

// 終日の予定に時刻を出さない。時刻つきは時刻まで出す
func TestFormatShowsTimeOnlyWhenSet(t *testing.T) {
	whole := format(Event{Summary: "合宿", Start: allDay("2026-08-01"), End: allDay("2026-08-03")})
	if strings.Contains(whole, ":") {
		t.Fatalf("終日の予定に時刻が出ている: %s", whole)
	}
	if !strings.Contains(whole, "2026-08-01") {
		t.Fatalf("日付が出ていない: %s", whole)
	}

	timed := format(Event{Summary: "定例", Start: at("2026-09-20 19:00"), End: at("2026-09-20 21:00")})
	if !strings.Contains(timed, "19:00-21:00") {
		t.Fatalf("時刻が出ていない: %s", timed)
	}
}

func TestFormatIncludesPlaceAndNote(t *testing.T) {
	got := format(Event{
		Summary: "荷重試験", Location: "西早稲田", Description: "9時集合\n持ち物は各自",
		Start: allDay("2026-09-20"),
	})
	if !strings.Contains(got, "場所: 西早稲田") {
		t.Fatalf("場所が出ていない: %s", got)
	}
	// 改行は1行に均す。予定の一覧として読めなくなる
	if strings.Contains(got, "\n") {
		t.Fatalf("改行が残っている: %q", got)
	}
	if !strings.Contains(got, "9時集合 持ち物は各自") {
		t.Fatalf("説明が出ていない: %s", got)
	}
}

// 予定表の説明に議事録を貼る人がいるので、上限は要る
func TestFormatTruncatesLongDescription(t *testing.T) {
	got := format(Event{Summary: "定例", Description: strings.Repeat("あ", 500), Start: allDay("2026-09-20")})
	if len([]rune(got)) > descriptionLimit+80 {
		t.Fatalf("説明を切っていない: %d文字", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("切ったことが分からない: %s", got[len(got)-30:])
	}
}

// 名前も日付も無い予定は出さない（空行が並ぶだけ）
func TestFormatSkipsEmptyEvents(t *testing.T) {
	if got := format(Event{Start: allDay("2026-09-20")}); got != "" {
		t.Fatalf("名前の無い予定を出している: %q", got)
	}
	if got := format(Event{Summary: "名前だけ"}); got != "" {
		t.Fatalf("日付の無い予定を出している: %q", got)
	}
}

func TestFetchNeedsConfiguration(t *testing.T) {
	if _, err := Fetch(t.Context(), nil); err != ErrNotConfigured {
		t.Fatalf("設定なしで読もうとしている: %v", err)
	}
}

func TestCalendarNamesAreUniqueAndSorted(t *testing.T) {
	got := CalendarNames([]Event{
		{Calendar: "WASA予定表"}, {Calendar: "WASA予定表"}, {Calendar: "TF日程"}, {Calendar: ""},
	})
	if len(got) != 2 || got[0] != "TF日程" || got[1] != "WASA予定表" {
		t.Fatalf("予定表の名前が違う: %v", got)
	}
}

func TestScopeNoteStatesRange(t *testing.T) {
	got := ScopeNote(12, []string{"WASA予定表"})
	for _, want := range []string{"WASA予定表", "12件", "60日前", "90日後"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q が無い: %s", want, got)
		}
	}
}
