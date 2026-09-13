package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func geminiHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestGeminiRetriesTemporaryFailure(t *testing.T) {
	var calls atomic.Int32
	var reserved atomic.Int32
	g := NewGeminiProfiles("test-key", ModelProfiles{Default: "test-model"}, 0, 1)
	// **再試行も1回として数える。** 無料枠のRPDは質問数ではなく送信回数で減る。
	// 判定と加算は同じ場所（この判定）で行うので、ここを数えれば消費と一致する
	g.SetAttemptGuard(func(_ context.Context, model string) error {
		if model != "test-model" {
			t.Errorf("判定へ渡るモデル名が不正: %q", model)
		}
		reserved.Add(1)
		return nil
	})
	g.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			resp := geminiHTTPResponse(http.StatusServiceUnavailable, `{}`)
			resp.Header.Set("Retry-After", "0")
			return resp, nil
		}
		return geminiHTTPResponse(http.StatusOK,
			`{"candidates":[{"content":{"parts":[{"text":"再試行成功"}]}}]}`), nil
	})}

	got, err := g.Complete(context.Background(), Request{MaxTokens: 10})
	if err != nil || got != "再試行成功" || calls.Load() != 2 {
		t.Fatalf("503後に1回だけ再試行できていない: got=%q calls=%d err=%v", got, calls.Load(), err)
	}
	if reserved.Load() != 2 {
		t.Fatalf("再試行を含む実送信回数を数えていない: %d", reserved.Load())
	}
}

func TestGeminiProfileSelectsServerConfiguredModelAndThinking(t *testing.T) {
	g := NewGeminiProfiles("test-key", ModelProfiles{
		Default: "gemini-3.5-flash-lite", Fast: "gemini-3.5-flash-lite",
		Standard: "gemini-3.6-flash", Deep: "gemini-3.6-flash",
	}, 0, 0)

	if got := g.modelFor(ProfileStandard); got != "gemini-3.6-flash" {
		t.Fatalf("標準モデルを選べていない: %q", got)
	}
	payload := g.payload(Request{MaxTokens: 300, Profile: ProfileDeep}, "gemini-3.6-flash")
	config := payload["generationConfig"].(map[string]any)
	thinking := config["thinkingConfig"].(map[string]any)
	if thinking["thinkingLevel"] != "high" {
		t.Fatalf("じっくりの推論量が不正: %+v", thinking)
	}
	if _, exists := config["temperature"]; exists {
		t.Fatal("Gemini 3系へ非推奨のtemperatureを送っている")
	}
	if config["maxOutputTokens"] != 2300 {
		t.Fatalf("思考用の余白がない: %+v", config)
	}
}

func TestGeminiUsesFixedDefaultModel(t *testing.T) {
	g := NewGeminiProfiles("test-key", ModelProfiles{}, 0, 0)
	if g.model != "gemini-3.5-flash-lite" {
		t.Fatalf("未指定時に固定モデルIDを使っていない: %q", g.model)
	}
	for _, profile := range []Profile{ProfileFast, ProfileStandard, ProfileDeep} {
		if got := g.modelFor(profile); got != g.model {
			t.Fatalf("未指定の段階が既定モデルへ戻っていない: profile=%s model=%q", profile, got)
		}
	}
}

func TestGeminiLegacyModelOmitsThinkingLevel(t *testing.T) {
	g := NewGeminiProfiles("test-key", ModelProfiles{Default: "gemini-2.5-flash"}, 0, 0)
	config := g.payload(Request{MaxTokens: 300, Profile: ProfileDeep}, "gemini-2.5-flash")["generationConfig"].(map[string]any)
	if _, exists := config["thinkingConfig"]; exists {
		t.Fatal("thinkingLevel非対応モデルへ設定を送っている")
	}
	if config["temperature"] != 0 {
		t.Fatalf("旧モデルの決定性設定が消えた: %+v", config)
	}
}

func TestGeminiStopsRequestsDuringRateLimitCooldown(t *testing.T) {
	var calls atomic.Int32
	g := NewGeminiProfiles("test-key", ModelProfiles{Default: "test-model"}, 0, 0)
	g.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return geminiHTTPResponse(http.StatusTooManyRequests, `{}`), nil
	})}

	if _, err := g.Complete(context.Background(), Request{MaxTokens: 10}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("429をレート制限として返していない: %v", err)
	}
	_, err := g.Complete(context.Background(), Request{MaxTokens: 10})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("待機中の呼び出しを止めていない: %v", err)
	}
	if retryAt, ok := RetryAt(err); !ok || time.Until(retryAt) < 30*time.Second {
		t.Fatalf("レート制限の再開目安がない: retryAt=%v ok=%v", retryAt, ok)
	}
	if calls.Load() != 1 {
		t.Fatalf("待機中にもGeminiへ送信している: calls=%d", calls.Load())
	}
}

func TestGeminiReportsIntervalWait(t *testing.T) {
	g := NewGeminiProfiles("test-key", ModelProfiles{Default: "test-model"}, 20*time.Millisecond, 0)
	g.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return geminiHTTPResponse(http.StatusOK,
			`{"candidates":[{"content":{"parts":[{"text":"完了"}]}}]}`), nil
	})}
	if _, err := g.Complete(context.Background(), Request{MaxTokens: 10}); err != nil {
		t.Fatal(err)
	}
	waited := make(chan WaitInfo, 1)
	if _, err := g.Complete(context.Background(), Request{MaxTokens: 10, OnWait: func(info WaitInfo) {
		waited <- info
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case info := <-waited:
		if info.Reason != "pace" || info.Until.IsZero() {
			t.Fatalf("送信間隔の待機理由が不正: %+v", info)
		}
	default:
		t.Fatal("送信間隔の待機を通知していない")
	}
}

func TestRetryDelayUsesResponseMetadata(t *testing.T) {
	resp := geminiHTTPResponse(http.StatusTooManyRequests, "")
	resp.Header.Set("Retry-After", "3")
	if got := retryDelay(resp, nil, time.Second); got != 3*time.Second {
		t.Fatalf("Retry-Afterを使っていない: %s", got)
	}

	resp.Header.Del("Retry-After")
	if got := retryDelay(resp, []byte(`{"retryDelay":"2s"}`), time.Second); got != 2*time.Second {
		t.Fatalf("RetryInfoを使っていない: %s", got)
	}
}

func TestDailyQuotaIsDistinguishedFromShortRateLimit(t *testing.T) {
	if !isDailyQuota([]byte(`{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}`)) {
		t.Fatal("無料枠の日次上限を短時間のレート制限と区別できていない")
	}
	if isDailyQuota([]byte(`{"quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier"}`)) {
		t.Fatal("分間上限を日次上限と誤認している")
	}
}

// 2026-08-10に実際に返ってきた本文をそのまま固定する。
//
// 注目すべきは **日次上限なのに retryDelay が 39s で返ってくる** ところである。
// message 側には「per day」も「RPD」も出てこないので、details の quotaId を
// 見ないと短時間のレート制限と区別できない。素直に retryDelay へ従うと、
// 回復しないものを待ち直し続けることになる（測定側で実際に空費した）。
func TestDailyQuotaMatchesRealResponseBody(t *testing.T) {
	body := []byte(`{"error":{"code":429,"message":"You exceeded your current quota, ` +
		`please check your plan and billing details. \n* Quota exceeded for metric: ` +
		`generativelanguage.googleapis.com/generate_content_free_tier_requests, ` +
		`limit: 500, model: gemini-3.5-flash-lite\nPlease retry in 39.131021433s.",` +
		`"status":"RESOURCE_EXHAUSTED","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{` +
		`"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests",` +
		`"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier",` +
		`"quotaValue":"500"}]},` +
		`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"39s"}]}}`)
	if !isDailyQuota(body) {
		t.Fatal("実際の日次上限の応答を検出できていない")
	}
}

func TestGeminiDoesNotRetryDailyQuota(t *testing.T) {
	var calls atomic.Int32
	g := NewGeminiProfiles("test-key", ModelProfiles{Default: "test-model"}, 0, 2)
	g.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return geminiHTTPResponse(http.StatusTooManyRequests,
			`{"error":{"details":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]}}`), nil
	})}

	if _, err := g.Complete(context.Background(), Request{MaxTokens: 10}); !errors.Is(err, ErrDailyQuota) {
		t.Fatalf("日次上限として返していない: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("回復しない日次上限を再試行した: calls=%d", calls.Load())
	}
	status := g.RuntimeStatus()
	if status.State != "daily_quota" || status.RetryAt.IsZero() {
		t.Fatalf("管理画面向けの日次上限状態が不正: %+v", status)
	}
}
