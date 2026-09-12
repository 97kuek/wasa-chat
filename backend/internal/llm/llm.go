// Package llm は回答生成に使うモデルを抽象化する。
//
// 測定はローカル（Ollama）、本番は Claude を想定しており、
// パイプライン側は具象実装に依存しない。
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// WaitInfo はGeminiへの再送や送信間隔調整で、回答開始が遅れる理由を画面へ伝える。
type WaitInfo struct {
	Reason string
	Until  time.Time
}

// APIAttempt は実際に上流へ送ったHTTPリクエスト。再試行も1件ずつ通知する。
// 無料枠のRPDは質問数ではなくこの回数で減るため、管理画面の残量推定に使う。
type APIAttempt struct {
	At     time.Time
	Model  string
	Method string
}

type APIAttemptObserver func(context.Context, APIAttempt)

// APIAttemptGuard は送信の直前に呼ばれ、送ってよければ nil を返す。
// **数えるだけでは枠は守れない**ので、止められる場所をここに用意する。
type APIAttemptGuard func(ctx context.Context, model string) error

// RuntimeStatus は管理画面へ公開してよい上流の状態だけを表す。
// APIキーや応答本文は含めない。
type RuntimeStatus struct {
	State   string    `json:"state"` // available | rate_limited | daily_quota
	RetryAt time.Time `json:"retryAt,omitempty"`
}

type RuntimeStatusProvider interface {
	RuntimeStatus() RuntimeStatus
}

type retryError struct {
	kind  error
	until time.Time
}

func (e *retryError) Error() string { return e.kind.Error() }
func (e *retryError) Unwrap() error { return e.kind }

func withRetryAt(kind error, until time.Time) error {
	return &retryError{kind: kind, until: until}
}

// RetryAt は上流が通知した再開時刻、または安全側に見積もった再開時刻を返す。
func RetryAt(err error) (time.Time, bool) {
	var target *retryError
	if !errors.As(err, &target) || target.until.IsZero() {
		return time.Time{}, false
	}
	return target.until, true
}

var (
	// ErrRateLimited はGemini側の短時間のRPM・TPM上限到達を表す。
	ErrRateLimited = errors.New("LLMの利用上限に到達")
	// ErrDailyQuota はGemini無料枠のRPD上限到達を表す。
	ErrDailyQuota = errors.New("LLMの日次利用上限に到達")
	// ErrQuotaGuard は、こちらが数えた送信回数が上限へ達したことを表す。
	// 上流が429を返す前に、自分で止めた場合に使う
	ErrQuotaGuard = errors.New("本日のLLM利用上限に達しました")
	// ErrUnavailable は一時的な通信障害や上流サービス障害を表す。
	ErrUnavailable = errors.New("LLMへ一時的に接続できない")
)

// Request の Cached / Prompt の分割には理由がある。
//
// Cached にはプロンプトの**先頭に置く固定部分**（目次）を入れる。
// 目次を毎回同じ位置・同じ内容で先頭に置くと、
//   - ローカル（llama.cpp）では KV キャッシュが再利用され、
//     プロンプト処理が 80秒 → 0.8秒 になる（M2a で実測）
//   - Claude では prompt caching が効き、入力費用が約 1/10 になる
//
// つまり「目次を先頭に固定する」ことが性能と費用の両方の要になっており、
// それを型で強制するために分けている。連結して1本の文字列にしてはいけない。
type Request struct {
	Cached string // 先頭固定部分（目次など）。キャッシュ対象
	// System は利用者の入力より強い立場で効かせたい指示。
	//
	// 利用者が作ったアシスタントの追加指示を Prompt に載せる以上、
	// 「出典を書くな」「資料に無くても答えろ」を無効化する規則は、
	// **同じユーザーメッセージの後ろに置くだけでは不十分**である。
	// 提供元が system / systemInstruction を持つなら必ずそちらへ回す。
	// 汎用とアシスタントで規則は2種類あるが、Cachedの固定プレフィックスより
	// 後ろに置くため、目次キャッシュ自体は分裂させない。
	System    string
	Prompt    string          // 質問ごとに変わる部分
	Schema    json.RawMessage // 構造化出力のJSONスキーマ。nil なら自由形式
	MaxTokens int
	// Profile はモデル名を利用者へ公開せず、処理に必要な能力だけを伝える。
	// 実際のモデルIDはサーバー側の許可済み設定から選ぶため、クライアントが
	// 任意の高額モデルを指定することはできない。
	Profile Profile
	OnWait  func(WaitInfo) // nilなら待機状況を通知しない
	// Images は利用者が添えた画像。**回答段だけに渡す。**
	//
	// ページ選択は目次からタイトルを選ぶ仕事で、画像を見せてもほぼ働かない。
	// 全段へ渡すと呼び出し3回ぶんの入力費用が乗るだけになる（docs/04）。
	Images []Image
}

// Image は利用者が添えた画像。原寸は受け取らない。
//
// ブラウザ側で長辺768pxまで縮めてから送る前提で、サーバーは受け取った
// バイト列の形式と大きさを必ず自分で検証する（申告を信じない）。
type Image struct {
	MediaType string // image/jpeg | image/png | image/webp
	Data      []byte
}

// ErrImagesUnsupported は、画像を受け取れないモデルへ画像付きで来たときに返す。
//
// 黙って画像を捨てると、**利用者には「画像を見て答えたつもりの嘘」**が返る。
// 手元のOllama（qwen3:30b-a3b）が該当するため、必ず失敗させる。
var ErrImagesUnsupported = errors.New("このモデルは画像を受け取れない")

// Profile は呼び出しごとの速度・推論量の段階。プロバイダが対応しない場合は
// 既定モデルへ安全にフォールバックする。
type Profile string

const (
	ProfileFast     Profile = "fast"
	ProfileStandard Profile = "standard"
	ProfileDeep     Profile = "deep"
)

// Delta はストリーミング中に届く差分。
type Delta func(text string)

type Client interface {
	// Complete は完了まで待って全文を返す。ページ選択など短い構造化出力に使う。
	Complete(ctx context.Context, req Request) (string, error)
	// Stream は差分を onDelta に流しつつ、最終的な全文を返す。回答生成に使う。
	Stream(ctx context.Context, req Request, onDelta Delta) (string, error)
	Name() string
}
