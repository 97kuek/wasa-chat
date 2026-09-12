import type { FeedbackReason, Turn } from "./api";

export const FEEDBACK_COMMENT_MAX = 500;
export const FEEDBACK_ANSWER_MAX = 20_000;
export const FEEDBACK_SOURCE_MAX = 8;

export const FEEDBACK_LABELS: Record<FeedbackReason, string> = {
  helpful: "知りたいことが分かった",
  clear: "説明が分かりやすい",
  good_sources: "出典が役立った",
  incorrect: "内容が違う",
  missing: "必要な情報がない",
  unclear: "説明が分かりにくい",
  wrong_sources: "出典が違う",
  outdated: "情報が古い",
  slow: "回答が遅い",
  bug: "表示・動作がおかしい",
  usability: "使いにくい",
  feature: "機能の提案",
  content: "回答・資料について",
  other: "その他",
};

export const GOOD_REASONS: FeedbackReason[] = ["helpful", "clear", "good_sources"];
export const BAD_REASONS: FeedbackReason[] = ["incorrect", "missing", "unclear", "wrong_sources", "outdated", "slow"];
export const GENERAL_REASONS: FeedbackReason[] = ["bug", "usability", "feature", "content", "other"];

export function feedbackNotificationMessage(noun = "フィードバック"): string {
  return `${noun}を保存しました`;
}

type ReasonButtonsProps = {
  reasons: FeedbackReason[];
  selected: FeedbackReason[];
  onToggle: (reason: FeedbackReason) => void;
  className: "feedback-choice-list" | "feedback-chips";
};

export function FeedbackReasonButtons({ reasons, selected, onToggle, className }: ReasonButtonsProps) {
  return (
    <div className={className} role="group" aria-label="フィードバックの種類">
      {reasons.map((reason) => (
        <button
          type="button"
          key={reason}
          className={selected.includes(reason) ? "selected" : ""}
          aria-pressed={selected.includes(reason)}
          onClick={() => onToggle(reason)}
        >
          {FEEDBACK_LABELS[reason]}
        </button>
      ))}
    </div>
  );
}

type FeedbackPopoverProps = {
  reason: FeedbackReason | null;
  comment: string;
  submitting: boolean;
  onReason: (reason: FeedbackReason) => void;
  onComment: (comment: string) => void;
  onSubmit: () => void;
  onClose: () => void;
};

export function FeedbackPopover(props: FeedbackPopoverProps) {
  return (
    <section className="header-popover feedback-popover" id="feedback-popover" aria-label="フィードバック">
      <div className="feedback-popover-head">
        <div><h2>気づいたことを送る</h2><p>改善のため、開発者が確認します</p></div>
        <button type="button" className="popover-close" onClick={props.onClose} aria-label="閉じる" title="閉じる">
          <svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
            <path d="m6 6 12 12M18 6 6 18" />
          </svg>
        </button>
      </div>
      <form className="feedback-comment-form" onSubmit={(event) => { event.preventDefault(); props.onSubmit(); }}>
        <FeedbackReasonButtons
          reasons={GENERAL_REASONS}
          selected={props.reason ? [props.reason] : []}
          onToggle={props.onReason}
          className="feedback-choice-list"
        />
        <textarea
          value={props.comment}
          onChange={(event) => props.onComment(event.target.value)}
          maxLength={FEEDBACK_COMMENT_MAX}
          rows={3}
          aria-label="補足"
          placeholder="どこで、何が起きたかなど"
        />
        <button type="submit" disabled={!props.reason || props.submitting}>{props.submitting ? "送信中…" : "送信する"}</button>
      </form>
    </section>
  );
}

type AnswerFeedbackProps = {
  turn: Turn;
  open: boolean;
  comment: string;
  regenerating: boolean;
  onRating: (rating: "good" | "bad") => void;
  onReason: (reason: FeedbackReason) => void;
  onComment: (comment: string) => void;
  onSubmitComment: () => void;
  onClose: () => void;
  onCopy: () => void;
  onRegenerate: () => void;
};

function CopyIcon() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true">
      <rect x="9" y="9" width="11" height="11" rx="2" />
      <path d="M5 15V6a2 2 0 0 1 2-2h9" />
    </svg>
  );
}

function RegenerateIcon() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true">
      <path d="M20 12a8 8 0 1 1-2.3-5.6M20 4v5h-5" />
    </svg>
  );
}

export function AnswerFeedback(props: AnswerFeedbackProps) {
  const { turn } = props;
  return (
    <section className="answer-feedback" aria-label="回答の操作と評価">
      <div className="answer-feedback-row">
        <span>{turn.feedbackRating ? "評価済み" : "この回答は役に立ちましたか？"}{!turn.feedbackRating && <small>質問・回答も送信</small>}</span>
        {/* コピーと再生成は評価より前に置く。押す頻度が高く、
            評価と違って押しても取り消せる */}
        <button type="button" className="answer-action" aria-label="回答をコピー" title="回答をコピー" onClick={props.onCopy}>
          <CopyIcon />
        </button>
        <button
          type="button"
          className="answer-action"
          aria-label="同じ質問を入力欄へ入れる"
          title="同じ質問を入力欄へ入れる"
          disabled={props.regenerating}
          onClick={props.onRegenerate}
        >
          <RegenerateIcon />
        </button>
        <button type="button" className={turn.feedbackRating === "good" ? "selected" : ""} aria-label="役に立った" aria-pressed={turn.feedbackRating === "good"} onClick={() => props.onRating("good")}>👍</button>
        <button type="button" className={turn.feedbackRating === "bad" ? "selected" : ""} aria-label="改善が必要" aria-pressed={turn.feedbackRating === "bad"} onClick={() => props.onRating("bad")}>👎</button>
      </div>
      {props.open && turn.feedbackRating && (
        <div className="answer-feedback-detail">
          <button type="button" className="answer-feedback-close" onClick={props.onClose} aria-label="閉じる" title="閉じる">
            <svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
              <path d="m6 6 12 12M18 6 6 18" />
            </svg>
          </button>
          <FeedbackReasonButtons reasons={turn.feedbackRating === "good" ? GOOD_REASONS : BAD_REASONS} selected={turn.feedbackReasons ?? []} onToggle={props.onReason} className="feedback-chips" />
          {/* 「補足」というラベルは置かない。書く欄が1つしかなく、
              プレースホルダで足りる。閉じるはアイコンで右上へ寄せる */}
          <div className="answer-feedback-comment">
            <textarea value={props.comment} onChange={(event) => props.onComment(event.target.value)} maxLength={FEEDBACK_COMMENT_MAX} rows={2} aria-label="補足" placeholder="補足があれば入力（任意）" />
            <button type="button" onClick={props.onSubmitComment}>送る</button>
          </div>
          <p>評価時は、この質問・回答・出典も改善確認のため保存します。</p>
        </div>
      )}
    </section>
  );
}
