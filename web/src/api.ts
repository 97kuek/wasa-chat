// 通常画面とバックエンドの通信。管理画面は遅延読込を保つため admin/api.ts へ分離する。

import { API_ORIGIN } from "./config";

/**
 * 資料の出所。サーバー側の `assistant.origins` と同じ集合にすること。
 *
 * ここが欠けていると、画面からその出所を選べないまま
 * サーバーだけが受け付ける状態になる（実際 `fee` がその状態だった）。
 */
/** アシスタントが参照範囲を**狭める**ときの指定。索引の資料だけが対象。 */
export type AssistantOrigin = "wiki" | "site" | "fee";

/**
 * 出典の出所。
 *
 * ⚠️ **AssistantOrigin と同じにしない。** アシスタントは範囲を狭めるもので、
 * 索引に入っている資料しか選べない。一方で出典には、設定画面でつないだ
 * 共有ドライブやDiscordも出る。同じ型に押し込むと、アシスタントの選択肢に
 * Discordが並ぶことになる（2026-09-13のCodex指摘）。
 */
export type SourceOrigin = AssistantOrigin | "drive" | "discord" | "calendar";

export type Source = {
  title: string;
  url: string;
  last_edited: string;
  /** 資料の出所。**部外に出せる情報かどうかの判断に要る**ので、出典に必ず出す。
   *  値はサーバー側（pipeline.originLabels）と同じにすること。 */
  origin?: SourceOrigin;
  /** 回答が実際に根拠として挙げた資料か。
   *
   *  ⚠️ **選んだ資料と、使った資料は違う。** 4件選んで1件しか引用しないことは
   *  普通に起きる。全部を「参照」に並べると、回答が「記載がありません」と
   *  言っているのに参照だけ並ぶ食い違いが出る（2026-09-13に指摘）。
   *
   *  古い履歴にはこの項目が無い。**無ければ「使った」扱い**にする
   *  （当時の表示と変えないため）。 */
  used?: boolean;
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
 * 設定画面の「外部サービス連携」でオン・オフする参照先。
 *
 * **引き継ぎ資料（Wiki・公式サイト・フライトシミュレータ）はここに出ない。**
 * それらは常に読むので、ここは「それ以外の置き場所」だけを並べる。
 *
 * available が false のものも返る。**黙って消すと「無い機能」に見える**ので、
 * 設定が要るだけなら reason にそう書いて出す。
 */
export type SettingsTool = {
  id: string;
  name: string;
  description: string;
  available: boolean;
  /** available が false のときだけ入る。なぜ使えないか */
  reason?: string;
  /** 利用者がオンにしているか */
  enabled: boolean;
};

/**
 * ボットが入っているDiscordサーバー。
 *
 * ⚠️ **名前だけでは選べない。**「WASA 41代」「WASA 42代」と並んでも、初めて
 * 設定する人には**どれが自分のいるサーバーか分からない**（2026-09-14の指摘）。
 * 見分けが付く材料（アイコン・人数）と、選んだら何を読むか（チャンネル数）を添える。
 */
export type DiscordServer = {
  id: string;
  name: string;
  /** サーバーアイコン（data URI）。無ければ画面が頭文字で描く */
  icon?: string;
  /** おおよその参加人数。取れなければ入らない */
  members?: number;
  /** 実際に検索する公開チャンネルの数 */
  channels?: number;
};

/**
 * Discordの連携。
 *
 * ⚠️ **オン・オフのスイッチは無い。** 連携したサーバーの有無がそのまま
 * オン・オフである。**代ごとにサーバーが変わり、つなぐ先は人によって違う**ので、
 * 設定に1つ書いて全員へ効かせることはできない（2026-09-14の指摘）。
 */
export type DiscordSettings = {
  /** 連携中のサーバー */
  connected: DiscordServer[];
  /** ボットが入っていて、まだ連携していないサーバー */
  joinable: DiscordServer[];
  /** ボットを新しいサーバーへ入れるURL。空なら設定が足りない */
  inviteUrl?: string;
  /** 同時に連携できるサーバーの数 */
  maxServers: number;
  /** 連携そのものができないときの理由 */
  reason?: string;
};

export type Settings = { tools: SettingsTool[]; discord: DiscordSettings };

/**
 * 連携の状態を読む。
 *
 * `refresh` を渡すと、Discordのサーバー一覧を取り直す。**ボットを入れた直後**の
 * ために要る（サーバー側が10分覚えているので、そのままでは出てこない）。
 *
 * ⚠️ **読めなかったときに空を返さない。** 空で返すと、通信に失敗しただけなのに
 * 「連携できるサーバーはありません」と読める画面になる（つないだ覚えのある人に、
 * 外れたと思わせる）。読めなかったことは、読めなかったと出す。
 */
export async function settings(refresh = false): Promise<Settings> {
  const res = await fetch(`${API_ORIGIN}/api/settings${refresh ? "?refresh=1" : ""}`, {
    credentials: "include",
  });
  if (!res.ok) throw new Error("連携の状態を読み込めませんでした");
  return res.json();
}

/**
 * 連携を保存する。**保存した結果がそのまま返る。**
 *
 * 画面の手元の値を正としない。上限を超えた指定や、ボットが入っていない
 * サーバーはサーバー側が断るので、返ってきたものを描き直す。
 */
export async function saveSettings(input: { tools: string[]; discordServers: string[] }): Promise<Settings> {
  const res = await fetch(`${API_ORIGIN}/api/settings`, {
    method: "PUT",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error ?? "連携を保存できませんでした");
  return body as Settings;
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
  context: ConversationContextTurn[] = [],
  responseMode: ResponseMode = "auto",
  attachments: string[] = [],
): Promise<void> {
  // ⚠️ **参照先はここで送らない。** 外部サービス連携は設定画面に保存した
  // 利用者ごとの設定で、サーバーが保存先から読む。送る作りだと、設定を
  // 変えた直後の質問が古い指定のまま飛ぶ（2026-09-14に設定画面へ移した）。
  //
  // 非公開Wikiに関する質問をURLへ載せるとアクセスログに残るため、本文で送る。
  const res = await fetch(`${API_ORIGIN}/api/ask`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ question, assistantId: assistantId ?? "", context, responseMode, attachments }),
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
