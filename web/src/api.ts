// 通常画面とバックエンドの通信。管理画面は遅延読込を保つため admin/api.ts へ分離する。

import { API_ORIGIN } from "./config";

/**
 * 資料の出所。サーバー側の `assistant.origins` と同じ集合にすること。
 *
 * ここが欠けていると、画面からその出所を選べないまま
 * サーバーだけが受け付ける状態になる（実際 `fee` がその状態だった）。
 */
export type AssistantOrigin = "wiki" | "site" | "fee";

export type Source = {
  title: string;
  url: string;
  last_edited: string;
  /** 資料の出所。**部外に出せる情報かどうかの判断に要る**ので、出典に必ず出す。
   *  値はサーバー側（assistant.origins）と同じにすること。 */
  origin?: AssistantOrigin;
  /** この資料から実際に読んだ節のパンくず（「ページ名 &gt; 見出し」）。
   *  回答末尾の「参照」に出し、どこを開けば確かめられるかまで示す。
   *  節を選び終えるまで確定しないので、`pages`イベントは2回流れる。 */
  sections?: string[];
};

export type ResponseMode = "auto" | "fast" | "standard" | "deep";
export type ResolvedResponseMode = Exclude<ResponseMode, "auto">;
export type StageTimingName = "pages" | "chunks" | "answer" | "total";
export type StageTimings = {
  pagesMs?: number;
  chunksMs?: number;
  answerMs?: number;
  totalMs?: number;
};

export type Turn = {
  question: string;
  answer: string;
  sources: Source[];
  status: string;
  retryAt?: string;
  error?: string;
  errorCode?: "daily_quota" | "rate_limit" | "user_daily_limit" | "unavailable";
  streaming: boolean;
  /** どのアシスタントで答えたか。過去の回答でも口調の理由が分かるように残す。
   *  アイコンは持たない（履歴が肥大するため、現在の一覧から引く）。 */
  assistantId?: string;
  assistantName?: string;
  /** 利用者が選んだモードと、自動判定後に実際に使われたモード。 */
  responseMode?: ResponseMode;
  resolvedMode?: ResolvedResponseMode;
  timings?: StageTimings;
  /** 画像を添えて聞いたか。**画像そのものは保存しない**（Firestoreの
   *  1ドキュメント1MB上限に対し履歴は30件で、入れると破綻する）。
   *  履歴を開き直したとき「画像つきで聞いた」とだけ分かるようにする。 */
  hasAttachment?: boolean;
  feedbackRating?: "good" | "bad";
  feedbackReasons?: FeedbackReason[];
  feedbackComment?: string;
};

export type Chat = {
  id: string;
  title: string;
  createdAt: string;
  updatedAt: string;
  turns: Turn[];
  pinned?: boolean;
};

/** 指示語を解決するため質問APIへ送る直近の会話。出典や状態は再送しない。 */
export type ConversationContextTurn = Pick<Turn, "question" | "answer">;

/** サーバーから流れてくる進捗イベント。pipeline.Event と対応する。 */
export type Event =
  | { type: "mode"; mode: ResolvedResponseMode }
  | { type: "status"; message: string; retry_at?: string }
  | { type: "timing"; stage: StageTimingName; milliseconds: number }
  | { type: "pages"; pages: Source[] }
  | { type: "delta"; text: string }
  | { type: "done" }
  | { type: "error"; message: string; code?: "daily_quota" | "rate_limit" | "user_daily_limit" | "unavailable"; retry_at?: string };

export type Session = { authenticated: boolean; username: string; icon?: string; remaining: number; admin: boolean };

/**
 * 入力欄の「+」から足せる参照先。
 *
 * **引き継ぎ資料（Wiki・公式サイト・フライトシミュレータ）はここに出ない。**
 * それらは常に読むので、ここは「それ以外の置き場所」だけを並べる。
 *
 * available が false のものも返る。**黙って消すと「無い機能」に見える**ので、
 * 設定が要るだけなら reason にそう書いて出す。
 */
export type Tool = {
  id: string;
  name: string;
  description: string;
  available: boolean;
  /** available が false のときだけ入る。なぜ使えないか */
  reason?: string;
  /** Discord のときだけ入る。**代ごとにサーバーが変わる**ので選べるようにする */
  servers?: { id: string; name: string }[];
};

export async function tools(): Promise<Tool[]> {
  const res = await fetch(`${API_ORIGIN}/api/tools`, { credentials: "include" });
  if (!res.ok) return [];
  return res.json();
}

/**
 * Wikiのアカウントでログインする。
 *
 * パスワードはサーバーがWikiに中継して検証するだけで、保存もログ出力もしない。
 * 成功すると利用者名を載せた署名付きCookieが返る。
 * 失敗理由はサーバーの文言をそのまま出す（Wiki接続失敗と認証失敗を区別するため）。
 */
export async function login(username: string, password: string): Promise<string | null> {
  const res = await fetch(`${API_ORIGIN}/api/login`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (res.ok) return null;
  const body = await res.json().catch(() => ({}));
  return body.error ?? "ログインできませんでした";
}

export async function logout(): Promise<void> {
  await fetch(`${API_ORIGIN}/api/logout`, { method: "POST", credentials: "include" });
}

export async function session(): Promise<Session> {
  const res = await fetch(`${API_ORIGIN}/api/session`, { credentials: "include" });
  if (!res.ok) return { authenticated: false, username: "", remaining: 0, admin: false };
  return res.json();
}

export async function updateProfileIcon(icon: string): Promise<string> {
  const res = await fetch(`${API_ORIGIN}/api/profile/icon`, {
    method: "PUT",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ icon }),
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error ?? "利用者画像を保存できませんでした");
  return typeof body.icon === "string" ? body.icon : "";
}

export type FeedbackReason =
  | "helpful" | "clear" | "good_sources"
  | "incorrect" | "missing" | "unclear" | "wrong_sources" | "outdated" | "slow"
  | "bug" | "usability" | "feature" | "content" | "other";

export type FeedbackPayload = {
  clientId: string;
  kind: "answer" | "general";
  rating?: "good" | "bad";
  reasons?: FeedbackReason[];
  comment?: string;
  question?: string;
  answer?: string;
  sources?: Source[];
  assistantId?: string;
  assistantName?: string;
  responseMode?: ResponseMode;
  resolvedMode?: ResolvedResponseMode;
  timings?: StageTimings;
  chatId?: string;
  turnIndex?: number;
  page: "chat" | "assistants";
};

export async function submitFeedback(payload: FeedbackPayload): Promise<void> {
  const res = await fetch(`${API_ORIGIN}/api/feedback`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error ?? "フィードバックを送信できませんでした");
  }
}

/**
 * 部内の言い方と、資料での言い方の対応。
 *
 * **語の言い換えを置く場所であって、事実を置く場所ではない。** ここに書いたものには
 * 出典が付かないため、事実として回答に出ると確かめようがなくなる。規則はサーバー側の
 * system（assistant.Guard）にも入れてある。
 */
export type GlossaryEntry = { term: string; meaning: string };

/** 全員で共有するアシスタント。作成者名は隠さない（誰に聞けばよいか分かるため）。 */
export type Assistant = {
  id: string;
  name: string;
  description: string;
  instruction: string;
  team?: string;
  origin?: AssistantOrigin;
  /** data URI の画像。未設定なら画面側が名前の頭文字で描く。 */
  icon?: string;
  glossary?: GlossaryEntry[];
  author: string;
  createdAt: string;
  updatedAt: string;
  /** 参照範囲の説明。サーバー側で組み立てた文言をそのまま出す。 */
  scope: string;
  /** 作成者本人か管理者のときだけ true。編集と削除で同じ権限。 */
  canEdit: boolean;
};

/** 参照範囲に使える区分。呼び方はサーバー側が持つ（「空力班」のような
 *  存在しない呼び方を画面で組み立てないため）。 */
export type Team = { value: string; label: string };

/**
 * 出所と区分の組み合わせごとに、参照できるページ数。キーは `出所/区分`。
 * 参照範囲を選んだ結果が質問するまで分からなかったので、サーバーが索引を数えて返す。
 */
export type ScopeCounts = Record<string, number>;

export async function listAssistants(): Promise<{
  assistants: Assistant[];
  teams: Team[];
  scopeCounts: ScopeCounts;
}> {
  const res = await fetch(`${API_ORIGIN}/api/assistants`, { credentials: "include" });
  if (!res.ok) throw new Error("アシスタントを読み込めませんでした");
  const body = await res.json() as { assistants?: Assistant[]; teams?: Team[]; scopeCounts?: ScopeCounts };
  return { assistants: body.assistants ?? [], teams: body.teams ?? [], scopeCounts: body.scopeCounts ?? {} };
}

export type AssistantDraft = {
  id: string;
  name: string;
  description: string;
  instruction: string;
  team?: string;
  origin?: AssistantOrigin;
  icon?: string;
  glossary?: GlossaryEntry[];
};

export async function createAssistant(draft: AssistantDraft): Promise<Assistant> {
  const res = await fetch(`${API_ORIGIN}/api/assistants`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(draft),
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error ?? "アシスタントを作成できませんでした");
  return body as Assistant;
}

export async function updateAssistant(id: string, draft: AssistantDraft): Promise<Assistant> {
  const res = await fetch(`${API_ORIGIN}/api/assistants/${encodeURIComponent(id)}`, {
    method: "PUT",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(draft),
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error ?? "アシスタントを保存できませんでした");
  return body as Assistant;
}

export async function deleteAssistant(id: string): Promise<void> {
  const res = await fetch(`${API_ORIGIN}/api/assistants/${encodeURIComponent(id)}`, {
    method: "DELETE",
    credentials: "include",
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error ?? "アシスタントを削除できませんでした");
  }
}

/**
 * サーバーから来た履歴を、画面が前提にしている形へ整える。
 *
 * Goは中身の無いスライスを `[]` ではなく `null` としてJSONに書く。そのため
 * `turns` や `sources` が `null` で届くことがあり、`turn.sources.length` が
 * 「Cannot read properties of null」で落ちて画面が真っ白になっていた
 * （2026-08-09に本番で確認）。サーバー側も直したが、**すでに保存済みの
 * 履歴には `null` が残っている**ため、受け取る側でも必ず配列に均す。
 */
function normalizeChat(chat: Chat): Chat {
  const turns = Array.isArray(chat.turns) ? chat.turns : [];
  return {
    ...chat,
    turns: turns.map((turn) => ({
      ...turn,
      sources: Array.isArray(turn.sources) ? turn.sources : [],
    })),
  };
}

export async function listChats(): Promise<Chat[]> {
  const res = await fetch(`${API_ORIGIN}/api/chats`, { credentials: "include" });
  if (!res.ok) throw new Error("チャット履歴を読み込めませんでした");
  const body = await res.json() as { chats?: Chat[] };
  return Array.isArray(body.chats) ? body.chats.map(normalizeChat) : [];
}

export async function saveChat(chat: Chat): Promise<void> {
  const res = await fetch(`${API_ORIGIN}/api/chats/${encodeURIComponent(chat.id)}`, {
    method: "PUT",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(chat),
  });
  if (!res.ok) throw new Error("チャット履歴を保存できませんでした");
}

export async function deleteChat(chatId: string): Promise<void> {
  const res = await fetch(`${API_ORIGIN}/api/chats/${encodeURIComponent(chatId)}`, {
    method: "DELETE",
    credentials: "include",
  });
  if (!res.ok) throw new Error("チャット履歴を削除できませんでした");
}

/**
 * 質問を送り、イベントを逐次 onEvent に渡す。
 *
 * EventSource ではなく fetch + ReadableStream を使っているのは、
 * EventSource がエラー時に自動再接続してしまい、LLMの呼び出しが
 * 二重に走る（＝API費用が二重にかかる）ため。
 */
export async function ask(
  question: string,
  onEvent: (event: Event) => void,
  signal?: AbortSignal,
  assistantId?: string,
  enabledTools?: string[],
  discordServer?: string,
  context: ConversationContextTurn[] = [],
  responseMode: ResponseMode = "auto",
  attachments: string[] = [],
): Promise<void> {
  // 非公開Wikiに関する質問をURLへ載せるとアクセスログに残るため、本文で送る。
  const res = await fetch(`${API_ORIGIN}/api/ask`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      question, assistantId: assistantId ?? "", context, responseMode, attachments,
      tools: enabledTools ?? [],
      discordServer: discordServer ?? "",
    }),
    signal,
  });
  if (!res.ok || !res.body) {
    const body = await res.json().catch(() => ({ error: "通信に失敗しました", code: undefined }));
    onEvent({
      type: "error",
      message: body.error ?? "通信に失敗しました",
      code: body.code,
      retry_at: body.retry_at,
    });
    return;
  }

  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += value;

    // SSEのフレームは空行区切り
    let boundary: number;
    while ((boundary = buffer.indexOf("\n\n")) !== -1) {
      const frame = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const payload = frame.replace(/^data: /, "").trim();
      if (!payload) continue;
      try {
        onEvent(JSON.parse(payload) as Event);
      } catch {
        // 壊れたフレームは捨てる
      }
    }
  }
}
