/**
 * チャット画面の純粋な計算だけを集めた場所。
 *
 * ここにあるのは、DOMにもAPIにも触らず、入力から出力が決まる関数だけである。
 * App.tsx の中に置いていた間はテストが書けず、**画像だけを送るとチャットの
 * タイトルが空になる不具合を見逃していた**（2026-08-11に確認）。
 * 切り出した理由はそれで、`scripts/chat.test.mjs` が実際の失敗を固定している。
 *
 * 副作用のある処理（localStorage、fetch、状態更新）はここへ持ち込まないこと。
 * 持ち込んだ時点でテストできなくなり、切り出した意味が消える。
 */

import type { Chat, Source } from "./api";

/** チャット一覧に出すタイトルの長さ。日本語なので文字数で数える。 */
export const CHAT_TITLE_RUNES = 28;

/** 「今日」「昨日」の次にまとめる日数。 */
export const RECENT_HISTORY_DAYS = 7;

/** アシスタントIDに使える最大長。サーバー側の検証と揃える。 */
export const ASSISTANT_ID_MAX = 64;

/** 本文が無い質問（画像だけを送ったとき）のタイトル。 */
export const IMAGE_ONLY_TITLE = "画像の質問";

/**
 * 名前からIDの候補を作る。日本語名だとほぼ空になるので、そのときは呼び出し側で補う。
 */
export function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, ASSISTANT_ID_MAX);
}

/**
 * 最初の質問からチャットのタイトルを作る。
 *
 * **本文が空でも必ず名前が付く。** 「これでツイート作って」のように画像だけを
 * 送る使い方があり、そのとき空文字を返していたため、履歴一覧の項目が
 * 名前の無い行になっていた（2026-08-11に確認）。吹き出し側は
 * 「（画像のみ）」と出す手当てがあったのに、タイトルだけ漏れていた。
 */
export function chatTitle(question: string, hasAttachment = false): string {
  const runes = Array.from(question.trim());
  if (runes.length === 0) return hasAttachment ? IMAGE_ONLY_TITLE : "新しいチャット";
  return runes.slice(0, CHAT_TITLE_RUNES).join("") + (runes.length > CHAT_TITLE_RUNES ? "…" : "");
}

export type HistorySection = { label: string; chats: Chat[] };

/**
 * チャット履歴を、ピン留め・今日・昨日・過去7日間・年月へ分ける。
 *
 * 節の並び順は、並べ替えた後のチャットに現れた順で決まる。ピン留めを先頭へ
 * 寄せてあるので、結果として「ピン留め → 新しい順」になる。
 */
export function groupChats(chats: Chat[], now: Date = new Date()): HistorySection[] {
  const ordered = [...chats].sort((a, b) => {
    if (Boolean(a.pinned) !== Boolean(b.pinned)) return a.pinned ? -1 : 1;
    return new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime();
  });
  const today = new Date(now);
  today.setHours(0, 0, 0, 0);
  const sections = new Map<string, Chat[]>();

  for (const chat of ordered) {
    const updated = new Date(chat.updatedAt);
    let label = "以前";
    if (chat.pinned) {
      label = "ピン留め";
    } else if (!Number.isNaN(updated.getTime())) {
      const day = new Date(updated);
      day.setHours(0, 0, 0, 0);
      const daysAgo = Math.round((today.getTime() - day.getTime()) / 86_400_000);
      if (daysAgo === 0) label = "今日";
      else if (daysAgo === 1) label = "昨日";
      else if (daysAgo <= RECENT_HISTORY_DAYS) label = "過去7日間";
      else label = `${updated.getFullYear()}年${updated.getMonth() + 1}月`;
    }
    const section = sections.get(label) ?? [];
    section.push(chat);
    sections.set(label, section);
  }
  return Array.from(sections, ([label, grouped]) => ({ label, chats: grouped }));
}

/**
 * 回答1件分の平文。コピーボタンで持ち出すときに使う。
 *
 * 本文の `[1]` は残す。**消すと、どの文がどの資料に基づくのかが失われる。**
 * 貼り付け先で番号を追えるよう、出典を番号付きで添える。
 */
export function answerPlainText(turn: { answer: string; sources: Source[] }): string {
  const list = turn.sources.map((source, index) => {
    const sections = source.sections?.length ? `\n   参照: ${source.sections.join(" / ")}` : "";
    return `[${index + 1}] ${source.title}: ${source.url}${sections}`;
  });
  return list.length === 0 ? turn.answer.trim() : `${turn.answer.trim()}\n\n出典:\n${list.join("\n")}`;
}

/**
 * 回答末尾の「参照」に出す行。どこを開けば確かめられるかまで示す。
 *
 * 節が取れていない資料はページ名で代替する。空欄を出すより、
 * 少なくともどのページかは分かるほうがよい。
 */
export type ReferenceItem = { label: string; url?: string };

/**
 * 回答が読んだ節の一覧。
 *
 * Discordの会話には節が無いので、チャンネル名をそのまま出し、**発言へ飛べる
 * リンクを添える**。索引のページではないので出典カードには出せないが、
 * 「どこを開けば確かめられるか」は示せる。示さないと、部員の発言を根拠に
 * 答えたことが後から誰にも追えない（2026-09-13の指摘）。
 */
export function referenceItems(sources: Source[]): ReferenceItem[] {
  const seen = new Set<string>();
  const out: ReferenceItem[] = [];
  for (const source of sources) {
    const sections = source.sections?.length ? source.sections : [source.title];
    for (const section of sections) {
      if (!section || seen.has(section)) continue;
      seen.add(section);
      // 節はページの一部なので、リンクはページ単位のDiscordだけに付ける
      out.push(source.origin === "discord" ? { label: section, url: source.url } : { label: section });
    }
  }
  return out;
}

export function referenceSections(sources: Source[]): string[] {
  return referenceItems(sources).map((item) => item.label);
}

/** 共有・コピー用の平文。出典のURLも一緒に持ち出せるようにする。 */
export function shareText(chat: Chat): string {
  const turns = chat.turns.map((turn) => {
    const sources = turn.sources.length > 0
      ? `\n\n出典:\n${turn.sources.map((source) => `- ${source.title}: ${source.url}`).join("\n")}`
      : "";
    return `質問:\n${turn.question}\n\n回答:\n${turn.answer || turn.error || "回答なし"}${sources}`;
  });
  return `${chat.title}\n\n${turns.join("\n\n---\n\n")}`;
}

/**
 * 再開予定時刻を「あと何分」の形にする。
 *
 * 日次上限に当たったときの `retryDelay` は数時間先を指すことがある（M34）。
 * 秒数だけを出すと桁が読めないので、時刻も添える。
 */
export function retryLabel(value: string | undefined, now: number): string {
  if (!value) return "";
  const at = new Date(value);
  if (Number.isNaN(at.getTime())) return "";
  const milliseconds = at.getTime() - now;
  if (milliseconds <= 0) return "まもなく再開予定です";
  const seconds = Math.ceil(milliseconds / 1000);
  const minutes = Math.ceil(milliseconds / 60_000);
  const duration = seconds < 60
    ? `約${seconds}秒後`
    : minutes < 60
      ? `約${minutes}分後`
      : `約${Math.floor(minutes / 60)}時間${minutes % 60 ? `${minutes % 60}分` : ""}後`;
  const clock = new Intl.DateTimeFormat("ja-JP", { hour: "2-digit", minute: "2-digit" }).format(at);
  return `再開目安: ${duration}（${clock}頃）`;
}
