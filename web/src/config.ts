/** 画面間で共有する公開URL。認証情報や環境固有の秘密値は置かない。 */
export const APP_URLS = {
  wiki: import.meta.env.VITE_WIKI_URL ?? "https://wasabirdman.sakura.ne.jp/wbwiki/w/index.php",
  support: "/support.html",
  logo: "/assets/wasa-chat-logo-photo-trimmed.png",
  mark: "/assets/wasa-chat-mark-photo-trimmed.png",
} as const;

export const API_ORIGIN = import.meta.env.VITE_API_ORIGIN ?? "";

/** サーバーの入力検証と対応する、利用者が変更できない画面側の上限。 */
export const APP_LIMITS = {
  chats: 30,
  questionRunes: 500,
  chatTitleRunes: 80,
  assistantNameRunes: 40,
  assistantDescriptionRunes: 120,
  assistantInstructionRunes: 1_500,
  // 用語集の上限。サーバー側（internal/assistant）と同じ値にする。
  // 画面で先に止めないと、入力し終えてから保存で弾かれる
  glossaryEntries: 30,
  glossaryTermRunes: 20,
  glossaryMeaningRunes: 60,
  conversationTurns: 2,
  conversationAnswerRunes: 2_000,
  composerVisibleLines: 5,
} as const;

/** UX上の待ち時間。散在させると通常画面と管理画面の挙動がずれるため集約する。 */
export const UI_TIMING = {
  toastMs: 3_000,
  historySaveDebounceMs: 400,
  clockTickMs: 1_000,
} as const;

export const LAYOUT_QUERY = {
  wide: "(min-width: 901px)",
  compact: "(max-width: 900px)",
} as const;

export const STICK_TO_BOTTOM_PX = 80;

/**
 * 末尾へ戻るボタンを出し始める距離。
 *
 * 追従をやめる距離（STICK_TO_BOTTOM_PX）より広くとる。同じ値にすると、
 * 追従が切れた瞬間にボタンが出て、**回答が伸びるたびに出入りして点滅する。**
 */
export const SHOW_SCROLL_BOTTOM_PX = 160;
