// Package state は、端末をまたいで共有する利用回数とチャット履歴を管理する。
package state

import (
	"context"
	"errors"
	"time"
)

type Source struct {
	Title      string `json:"title" firestore:"title"`
	URL        string `json:"url" firestore:"url"`
	LastEdited string `json:"last_edited" firestore:"last_edited"`
	Origin     string `json:"origin,omitempty" firestore:"origin,omitempty"`
}

// StageTimingsは利用者が待った時間を処理段階ごとに保持する。
// モデル内部のトークン数ではなく壁時計時間なので、API待機や再試行も含む。
type StageTimings struct {
	PagesMS  int64 `json:"pagesMs,omitempty" firestore:"pages_ms,omitempty"`
	ChunksMS int64 `json:"chunksMs,omitempty" firestore:"chunks_ms,omitempty"`
	AnswerMS int64 `json:"answerMs,omitempty" firestore:"answer_ms,omitempty"`
	TotalMS  int64 `json:"totalMs,omitempty" firestore:"total_ms,omitempty"`
}

type Turn struct {
	Question  string   `json:"question" firestore:"question"`
	Answer    string   `json:"answer" firestore:"answer"`
	Sources   []Source `json:"sources" firestore:"sources"`
	Status    string   `json:"status" firestore:"status"`
	RetryAt   string   `json:"retryAt,omitempty" firestore:"retry_at,omitempty"`
	Error     string   `json:"error,omitempty" firestore:"error,omitempty"`
	ErrorCode string   `json:"errorCode,omitempty" firestore:"error_code,omitempty"`
	Streaming bool     `json:"streaming" firestore:"streaming"`
	// どのアシスタントで答えたか。過去の回答を見たときに復元できないと、
	// 口調が違う理由が分からない。画像は持たない（履歴が肥大するため、
	// アイコンは現在の一覧から引き、消えていれば名前の頭文字で描く）
	AssistantID   string `json:"assistantId,omitempty" firestore:"assistant_id,omitempty"`
	AssistantName string `json:"assistantName,omitempty" firestore:"assistant_name,omitempty"`
	// 回答時の指定と、自動判定後に実際に使ったモード。モデル名は履歴へ
	// 保存しないため、将来モデルを更新しても画面表示が陳腐化しない。
	ResponseMode string        `json:"responseMode,omitempty" firestore:"response_mode,omitempty"`
	ResolvedMode string        `json:"resolvedMode,omitempty" firestore:"resolved_mode,omitempty"`
	Timings      *StageTimings `json:"timings,omitempty" firestore:"timings,omitempty"`
	// HasAttachment は画像を添えて聞いたかどうか。**画像そのものは保存しない。**
	// Firestoreは1ドキュメント1MBで履歴は30件なので、入れると破綻する。
	// 履歴を開き直したとき「画像つきで聞いた」とだけ分かればよい。
	HasAttachment bool `json:"hasAttachment,omitempty" firestore:"has_attachment,omitempty"`
	// 回答評価は履歴にも残し、再表示後に同じ回答へ何度も評価させない。
	// 集計用の正本はトップレベルfeedbackコレクションに別途保存する。
	FeedbackRating  string   `json:"feedbackRating,omitempty" firestore:"feedback_rating,omitempty"`
	FeedbackReasons []string `json:"feedbackReasons,omitempty" firestore:"feedback_reasons,omitempty"`
	FeedbackComment string   `json:"feedbackComment,omitempty" firestore:"feedback_comment,omitempty"`
}

type Chat struct {
	ID        string `json:"id" firestore:"id"`
	Title     string `json:"title" firestore:"title"`
	CreatedAt string `json:"createdAt" firestore:"created_at"`
	UpdatedAt string `json:"updatedAt" firestore:"updated_at"`
	Turns     []Turn `json:"turns" firestore:"turns"`
	Pinned    bool   `json:"pinned,omitempty" firestore:"pinned"`
}

// NormalizeChat は、中身の無いスライスを空スライスへ均す。
//
// Goは nil のスライスをJSONへ `[]` ではなく `null` として書く。画面側は
// `chat.turns` と `turn.sources` を配列として扱うため、`null` が届くと
// `.length` の読み出しで例外になり、画面が真っ白になっていた
// （2026-08-09に本番で確認）。保存時と読み出し時の両方でここを通す。
//
// 型で防げないのは、`omitempty` を付けても外せないためである。付けると
// 「0件」と「フィールドが無い」が区別できなくなり、問題を先送りするだけになる。
func NormalizeChat(chat Chat) Chat {
	if chat.Turns == nil {
		chat.Turns = []Turn{}
	}
	turns := make([]Turn, len(chat.Turns))
	for i, turn := range chat.Turns {
		if turn.Sources == nil {
			turn.Sources = []Source{}
		}
		turns[i] = turn
	}
	chat.Turns = turns
	return chat
}

// Assistant は利用者が作り、全員で共有するアシスタント。
//
// チャット履歴と違い、**利用者名を平文で持つ**（Author）。履歴の保存先は
// 利用者名をHMAC化して隠しているが、アシスタントは「誰が作ったか」を
// 画面に出すことが安全装置そのものなので、隠しては意味がない。
// 匿名で共有できないことが、内容の管理を人手に頼らずに済む理由である。
type Assistant struct {
	ID          string `json:"id" firestore:"id"`
	Name        string `json:"name" firestore:"name"`
	Description string `json:"description" firestore:"description"`
	// 口調・語尾・文体・出力形式のための追加指示。事実の扱いの規則は上書きできない
	// （組み立ては internal/assistant を参照）。
	Instruction string `json:"instruction" firestore:"instruction"`
	// 参照範囲の絞り込み。空なら絞らない。**広げる方向の指定は存在しない**
	Team   string `json:"team,omitempty" firestore:"team,omitempty"`
	Origin string `json:"origin,omitempty" firestore:"origin,omitempty"` // "" | "wiki" | "site" | "fee"

	// Icon は data URI の画像。空なら画面側が名前の頭文字で描く。
	// 外部URLを許さないのは、部内Wikiの利用状況が外部ホストへ漏れるのと、
	// CSP（img-src 'self' data:）で表示できないため。
	Icon string `json:"icon,omitempty" firestore:"icon,omitempty"`

	Author    string `json:"author" firestore:"author"` // Wikiの利用者名。画面に必ず出す
	CreatedAt string `json:"createdAt" firestore:"created_at"`
	UpdatedAt string `json:"updatedAt" firestore:"updated_at"`
}

// Feedback は回答評価と画面全体への改善報告を同じ形で保存する。
// ReporterKeyは利用者名のHMAC値であり、管理画面のJSONには出さない。
type Feedback struct {
	ID            string        `json:"id" firestore:"id"`
	ReporterKey   string        `json:"-" firestore:"reporter_key"`
	Kind          string        `json:"kind" firestore:"kind"`                         // answer | general
	Rating        string        `json:"rating,omitempty" firestore:"rating,omitempty"` // good | bad
	Reasons       []string      `json:"reasons,omitempty" firestore:"reasons,omitempty"`
	Comment       string        `json:"comment,omitempty" firestore:"comment,omitempty"`
	Question      string        `json:"question,omitempty" firestore:"question,omitempty"`
	Answer        string        `json:"answer,omitempty" firestore:"answer,omitempty"`
	Sources       []Source      `json:"sources,omitempty" firestore:"sources,omitempty"`
	AssistantID   string        `json:"assistantId,omitempty" firestore:"assistant_id,omitempty"`
	AssistantName string        `json:"assistantName,omitempty" firestore:"assistant_name,omitempty"`
	ResponseMode  string        `json:"responseMode,omitempty" firestore:"response_mode,omitempty"`
	ResolvedMode  string        `json:"resolvedMode,omitempty" firestore:"resolved_mode,omitempty"`
	Timings       *StageTimings `json:"timings,omitempty" firestore:"timings,omitempty"`
	ChatID        string        `json:"chatId,omitempty" firestore:"chat_id,omitempty"`
	TurnIndex     int           `json:"turnIndex,omitempty" firestore:"turn_index,omitempty"`
	Page          string        `json:"page,omitempty" firestore:"page,omitempty"`
	SubmittedAt   string        `json:"submittedAt" firestore:"submitted_at"`
}

// UserProfile は管理画面で利用回数を実名と結び付け、本人の任意画像を保存する。
// 質問・回答は持たず、保存先のIDは従来どおり利用者名のHMAC値にする。
type UserProfile struct {
	Key       string    `json:"-" firestore:"-"`
	Username  string    `json:"username" firestore:"username"`
	Icon      string    `json:"icon,omitempty" firestore:"icon,omitempty"`
	FirstSeen time.Time `json:"firstSeen" firestore:"first_seen"`
	LastSeen  time.Time `json:"lastSeen" firestore:"last_seen"`
}

// DailyUsage は既存の日次上限判定用ドキュメントを管理画面でも読むための形。
type DailyUsage struct {
	Day       string    `json:"day" firestore:"-"`
	Used      int       `json:"used" firestore:"used"`
	UpdatedAt time.Time `json:"updatedAt" firestore:"updated_at"`
}

// UsageEvent は質問本文を含まない利用監査ログ。回答の中身ではなく、
// 成否・待ち時間・利用した機能を調べるために90日だけ保持する。
type UsageEvent struct {
	ID            string    `json:"id" firestore:"id"`
	UserKey       string    `json:"-" firestore:"user_key"`
	OccurredAt    time.Time `json:"occurredAt" firestore:"occurred_at"`
	Outcome       string    `json:"outcome" firestore:"outcome"`
	ResponseMode  string    `json:"responseMode,omitempty" firestore:"response_mode,omitempty"`
	ResolvedMode  string    `json:"resolvedMode,omitempty" firestore:"resolved_mode,omitempty"`
	AssistantID   string    `json:"assistantId,omitempty" firestore:"assistant_id,omitempty"`
	HasAttachment bool      `json:"hasAttachment,omitempty" firestore:"has_attachment,omitempty"`
	DurationMS    int64     `json:"durationMs" firestore:"duration_ms"`
}

// AdminAudit は実名一覧など、通常利用者には見えない情報へ管理者が
// アクセスした事実を残す。質問本文や秘密値は記録しない。
type AdminAudit struct {
	ID         string    `json:"id" firestore:"id"`
	Actor      string    `json:"actor" firestore:"actor"`
	Action     string    `json:"action" firestore:"action"`
	Target     string    `json:"target,omitempty" firestore:"target,omitempty"`
	OccurredAt time.Time `json:"occurredAt" firestore:"occurred_at"`
}

// AdminRole は主管理者が画面から付与した共同管理者権限。
// 主管理者は環境変数ADMIN_USERSを復旧口として扱うため、ここには保存しない。
type AdminRole struct {
	Key       string    `json:"-" firestore:"-"`
	Username  string    `json:"username" firestore:"username"`
	Role      string    `json:"role" firestore:"role"`
	GrantedBy string    `json:"grantedBy" firestore:"granted_by"`
	GrantedAt time.Time `json:"grantedAt" firestore:"granted_at"`
}

// SourceDelta は、現在の索引と公開元のメタデータを比べた結果。
// Added等には本文を入れず、管理者が確認先を判断できるページ名だけを残す。
type SourceDelta struct {
	Source  string   `json:"source" firestore:"source"`
	Added   []string `json:"added" firestore:"added"`
	Updated []string `json:"updated" firestore:"updated"`
	Removed []string `json:"removed" firestore:"removed"`
}

type SourceCheck struct {
	CheckedAt time.Time     `json:"checkedAt" firestore:"checked_at"`
	CheckedBy string        `json:"checkedBy" firestore:"checked_by"`
	Changed   bool          `json:"changed" firestore:"changed"`
	Deltas    []SourceDelta `json:"deltas" firestore:"deltas"`
}

// APIUsage はGeminiの日次RPDを推定するための、実HTTP送信回数。
// 質問1回は通常3送信で、再試行も枠を消費するため質問数とは分けて数える。
type APIUsage struct {
	Day       string    `json:"day" firestore:"day"`
	Model     string    `json:"model" firestore:"model"`
	Requests  int       `json:"requests" firestore:"requests"`
	UpdatedAt time.Time `json:"updatedAt" firestore:"updated_at"`
}

// Storeの利用者キーには、利用者名そのものではなくサーバー側でHMAC化した値を渡す。
// ただしアシスタントは全員で共有するため、利用者ごとの区別を持たない。
type Store interface {
	Remaining(context.Context, string, string, int) (int, error)
	Take(context.Context, string, string, int) (bool, error)
	Refund(context.Context, string, string) error
	SaveUserProfile(context.Context, string, string, time.Time) error
	GetUserProfile(context.Context, string) (UserProfile, bool, error)
	SaveUserIcon(context.Context, string, string) error
	ListUserProfiles(context.Context) ([]UserProfile, error)
	ListDailyUsage(context.Context, string, string) ([]DailyUsage, error)
	PurgeDailyUsage(context.Context, string, string) (int, error)
	PurgeUserProfiles(context.Context, time.Time) (int, error)
	SaveUsageEvent(context.Context, UsageEvent) error
	ListUsageEvents(context.Context, int) ([]UsageEvent, error)
	PurgeUsageEvents(context.Context, time.Time) (int, error)
	SaveAdminAudit(context.Context, AdminAudit) error
	ListAdminAudits(context.Context, int) ([]AdminAudit, error)
	PurgeAdminAudits(context.Context, time.Time) (int, error)
	GetAdminRole(context.Context, string) (AdminRole, bool, error)
	ListAdminRoles(context.Context) ([]AdminRole, error)
	SaveAdminRole(context.Context, string, AdminRole) error
	DeleteAdminRole(context.Context, string) error
	LatestSourceCheck(context.Context) (SourceCheck, bool, error)
	SaveSourceCheck(context.Context, SourceCheck) error
	RecordAPIRequest(context.Context, string, string, time.Time) error
	ListAPIUsage(context.Context, string) ([]APIUsage, error)
	ListChats(context.Context, string, int) ([]Chat, error)
	SaveChat(context.Context, string, Chat, int) error
	DeleteChat(context.Context, string, string) error
	SaveFeedback(context.Context, Feedback) error
	// ListFeedback は新しい順で返す。limitが0以下なら全件を返す。
	// HTTP画面ではなく、開発者用の書き出しCLIから使う。
	ListFeedback(context.Context, int) ([]Feedback, error)
	// PurgeFeedback は submitted_at が before より古い報告を消し、消した件数を返す。
	// プライバシーポリシーで保存期間を1年と示している以上、管理者の手作業に頼らず
	// コード側で期限切れを消す必要がある。beforeはRFC3339のUTC文字列。
	PurgeFeedback(ctx context.Context, before string) (int, error)

	ListAssistants(context.Context) ([]Assistant, error)
	// CreateAssistant は同じIDが無いときだけ書き込む。既にあれば ErrAssistantExists。
	//
	// 一覧で重複を調べてから書く方式だと、同時に作成された2件が両方とも
	// 検査を通り、後勝ちで他人のアシスタントを上書きできてしまう。
	// 「他人のものは編集できない」を守るには、書き込み自体を条件付きにするしかない。
	CreateAssistant(context.Context, Assistant) error
	// UpdateAssistant は既存を書き換える。IDは変えられない。
	UpdateAssistant(context.Context, Assistant) error
	DeleteAssistant(context.Context, string) error
}

// ErrAssistantExists は、既に使われているIDで作成しようとしたことを表す。
var ErrAssistantExists = errors.New("そのIDは既に使われています")
