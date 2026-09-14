import { lazy, Suspense, useEffect, useLayoutEffect, useRef, useState } from "react";
import {
  ask,
  createAssistant,
  deleteAssistant,
  deleteChat,
  listAssistants,
  listChats,
  login,
  logout,
  saveChat,
  session,
  settings as fetchSettings,
  saveSettings,
  submitFeedback,
  updateAssistant,
  updateProfileIcon,
  type Assistant,
  type AssistantDraft,
  type ScopeCounts,
  type Chat,
  type FeedbackReason,
  type Settings,
  type StageTimingName,
  type Team,
  type Turn,
} from "./api";
import { stripCitation } from "./answer";
import { answerPlainText, chatTitle, groupChats, retryLabel, shareText, slugify } from "./chat";
import { ACCEPTED_IMAGE_TYPES, toAttachment, type Attachment } from "./attachment";
import { AssistantAvatar, DefaultAvatar, toIconDataURL } from "./avatar";
import { AssistantSettingsForm, type AssistantFormMode } from "./assistant/SettingsForm";
import { LoadingScreen } from "./components/LoadingScreen";
import { ReferenceSummary } from "./components/ReferenceSummary";
import { SelectMenu, type SelectOption } from "./components/SelectMenu";
import { ToolIcon } from "./components/ToolIcon";
import { SettingsPage } from "./settings/SettingsPage";
import { Spinner } from "./components/Spinner";
import { Toast } from "./components/Toast";
import {
  APP_LIMITS,
  APP_URLS,
  LAYOUT_QUERY,
  SHOW_SCROLL_BOTTOM_PX,
  STICK_TO_BOTTOM_PX,
  UI_TIMING,
} from "./config";
import {
  AnswerFeedback,
  FEEDBACK_ANSWER_MAX,
  FEEDBACK_SOURCE_MAX,
  FeedbackPopover,
  feedbackNotificationMessage,
} from "./feedback";
import { useToast } from "./hooks/useToast";
import { readStored, readStoredIds, writeStored } from "./lib/storage";

// KaTeXは初期JSの大半を占めるが、回答が表示されるまでは不要である。
// 質問送信時に先読みし、ログイン画面の初期表示と回答開始時の待ち時間を両立する。
const loadMarkdownModule = () => import("./markdown");
const AdminPage = lazy(async () => {
  const module = await import("./admin/AdminPage");
  return { default: module.AdminPage };
});

const CHUNK_RELOAD_KEY = "wasa-chat-chunk-reload";

const Markdown = lazy(async () => {
  try {
    const module = await loadMarkdownModule();
    writeStored("session", CHUNK_RELOAD_KEY, null);
    return { default: module.Markdown };
  } catch (error) {
    // デプロイすると配信物のファイル名（ハッシュ）が変わり、開いたままのタブが
    // 参照している旧ファイルは消える。そのまま失敗させると lazy が描画中に
    // 例外を投げ、画面が真っ白になる（2026-08-09に本番で報告あり）。
    // 新しい配信物を取り直せば直るので、1回だけ再読み込みする。
    // 印を残すのは、取得そのものが壊れているときに再読み込みを繰り返さないため。
    if (!readStored("session", CHUNK_RELOAD_KEY)) {
      writeStored("session", CHUNK_RELOAD_KEY, "1");
      location.reload();
      await new Promise<never>(() => {}); // 再読み込みするので解決させない
    }
    throw error;
  }
});

type Announcement = {
  id: string;
  title: string;
  body: string;
  date: string;
};

const ANNOUNCEMENT_READ_KEY = "wasa-chat-read-announcements";
const ASSISTANT_KEY = "wasa-chat-assistant";
/* ⚠️ 外部サービス連携は端末に覚えない。**アカウントに持つ**（設定画面で保存し、
   サーバー側が質問のたびに読む）。端末ごとだと、別の端末やブラウザで開いたときに
   つないだはずの連携が黙って外れる（2026-09-14に設定画面へ移した） */
const ASSISTANT_FAVORITES_KEY = "wasa-chat-assistant-favorites";
const ASSISTANT_SORT_KEY = "wasa-chat-assistant-sort";
function resizeComposerTextarea(target: HTMLTextAreaElement | null): void {
  if (!target) return;
  target.style.height = "auto";
  const computed = getComputedStyle(target);
  const lineHeight = Number.parseFloat(computed.lineHeight);
  const verticalPadding = Number.parseFloat(computed.paddingTop) + Number.parseFloat(computed.paddingBottom);
  const maxHeight = lineHeight * APP_LIMITS.composerVisibleLines + verticalPadding;
  const nextHeight = Math.min(target.scrollHeight, maxHeight);
  target.style.height = `${nextHeight}px`;
  target.style.overflowY = target.scrollHeight > maxHeight ? "auto" : "hidden";
}

type AssistantSort = "favorite" | "name" | "author";

const ASSISTANT_SORT_OPTIONS: SelectOption[] = [
  { value: "favorite", label: "お気に入り順" },
  { value: "name", label: "名前順" },
  { value: "author", label: "作成者順" },
];



const emptyDraft: AssistantDraft = { id: "", name: "", description: "", instruction: "", glossary: [] };



/**
 * 回答末尾の「出典: ページ名（最終更新: YYYY-MM）」を落とす。
 * 同じ情報を構造化された出典カードでも表示するため、本文との重複だけを除く。
 */
/** 新入生や代替わり直後でも質問を始められるよう、具体的な入口を用意する。 */
const SUGGESTIONS: { title: string; body: string }[] = [
  { title: "空力設計の手順", body: "空力設計の設計手順について、詳しく分かりやすく説明してください" },
  { title: "荷重試験の申請", body: "荷重試験の申請方法を教えてください。申請先のメールアドレスも知りたいです" },
  { title: "鳥コンまでの流れ", body: "鳥人間コンテストまでにやっておくべきことを教えてください" },
  { title: "代ごとの違い", body: "空力設計は38代から41代にかけて何が変化しましたか？" },
  { title: "テストフライト", body: "テストフライトの申請方法と、TF前日までにやるべきことを教えてください" },
  // 公式サイト側にしかない情報。Wikiだけでは答えられない問いも入口に置く。
  { title: "WASAの成り立ち", body: "WASAはどんなサークルですか？設立年や歴代の機体名も教えてください" },
];

const makeId = () => crypto.randomUUID();

const loadReadAnnouncementIds = () => readStoredIds(ANNOUNCEMENT_READ_KEY);
const loadFavoriteAssistantIds = () => readStoredIds(ASSISTANT_FAVORITES_KEY);

function loadAssistantSort(): AssistantSort {
  const saved = readStored("local", ASSISTANT_SORT_KEY);
  return saved === "name" || saved === "author" ? saved : "favorite";
}

function HistoryIcon() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true">
      <path d="M4 5.5h16v11H8l-4 3v-14Z" />
    </svg>
  );
}

/** 読込中を示す円形スピナー。待ち時間の表示に凝ると、待っている事実が伝わりにくい。 */
export default function App() {
  const [authed, setAuthed] = useState<boolean | null>(null);
  const [username, setUsername] = useState("");
  const [profileIcon, setProfileIcon] = useState("");
	const [isAdmin, setIsAdmin] = useState(false);
  const [remaining, setRemaining] = useState(0);
  const [form, setForm] = useState({ username: "", password: "" });
  const [showPassword, setShowPassword] = useState(false);
  const [loginError, setLoginError] = useState("");
  const [busy, setBusy] = useState(false);
  const [question, setQuestion] = useState("");
  // 添付は保存しない。**送信後も消すまで残す**ので、「もう5案」のような
  // 追質問で同じ画像を使い直せる。チップとして見えているので予測できる
  const [attachment, setAttachment] = useState<Attachment | null>(null);
  const attachmentInput = useRef<HTMLInputElement>(null);
  const profileIconInput = useRef<HTMLInputElement>(null);
  // ドラッグは子要素をまたぐたびに enter/leave が飛ぶ。数えないと、
  // 画面の上を動かしただけで枠が点滅する
  const dragDepth = useRef(0);
  const [dragging, setDragging] = useState(false);
  const [chats, setChats] = useState<Chat[]>([]);
  const [activeChatId, setActiveChatId] = useState<string | null>(null);
  const [sidebarOpen, setSidebarOpen] = useState(() => window.matchMedia(LAYOUT_QUERY.wide).matches);
  // 入力欄の案内文は、狭い画面では語の途中で切れる（LAYOUT_QUERY.narrow の実測値）
  const [narrow, setNarrow] = useState(() => window.matchMedia(LAYOUT_QUERY.narrow).matches);
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [readAnnouncementIds, setReadAnnouncementIds] = useState(loadReadAnnouncementIds);
  const [noticeOpen, setNoticeOpen] = useState(false);
  const [profileOpen, setProfileOpen] = useState(false);
  const [profileIconBusy, setProfileIconBusy] = useState(false);
  const [feedbackOpen, setFeedbackOpen] = useState(false);
  const [generalFeedbackId, setGeneralFeedbackId] = useState(makeId);
  const [generalReason, setGeneralReason] = useState<FeedbackReason | null>(null);
  const [generalComment, setGeneralComment] = useState("");
  const [generalSubmitting, setGeneralSubmitting] = useState(false);
  const [answerFeedbackOpen, setAnswerFeedbackOpen] = useState<string | null>(null);
  const [answerComments, setAnswerComments] = useState<Record<string, string>>({});
  const [historyMenuId, setHistoryMenuId] = useState<string | null>(null);
  const [renamingChatId, setRenamingChatId] = useState<string | null>(null);
  const [renameTitle, setRenameTitle] = useState("");
  const { toast, showToast, hideToast } = useToast();
  const [clock, setClock] = useState(Date.now());
  const [assistants, setAssistants] = useState<Assistant[]>([]);
  const [teams, setTeams] = useState<Team[]>([]);
  const [favoriteAssistantIds, setFavoriteAssistantIds] = useState(loadFavoriteAssistantIds);
  const [assistantSort, setAssistantSort] = useState<AssistantSort>(loadAssistantSort);
  // **回答モードは選ばせない。** 実運用では thinking しか使われておらず、
  // 選択肢を出すぶんだけ画面と履歴表示が増えていた（2026-09-12に人間が判断）。
  // サーバー側の許可リストは残してあるので、必要になれば画面だけ戻せる
  const responseMode = "deep" as const;
  // 選んだアシスタントは端末に覚える。毎回選び直させると、結局
  // 誰も使わない機能になる（サーバーに持つほどの情報でもない）
  const [assistantId, setAssistantId] = useState(() => readStored("local", ASSISTANT_KEY) ?? "");
  // 外部サービス連携。**サーバーが持つ**ので、読み込むまでは null
  const [settings, setSettings] = useState<Settings | null>(null);
  const [settingsBusy, setSettingsBusy] = useState("");
  const [settingsError, setSettingsError] = useState("");
  const [serversRefreshing, setServersRefreshing] = useState(false);
  // チャット画面・アシスタント一覧・設定を切り替える。モーダルではなく画面ごと
  // 差し替えるのは、どれも「決める場所」であって会話の付随物ではないため
  const [view, setView] = useState<"chat" | "assistants" | "settings" | "admin">(
    () => location.pathname === "/admin" ? "admin" : location.pathname === "/settings" ? "settings" : "chat",
  );
  // 一覧の中で作成／編集／閲覧画面を開いているか。閲覧は他人の設定にも使う。
  const [assistantForm, setAssistantForm] = useState<{ mode: AssistantFormMode; assistantId?: string } | null>(null);
  const [assistantDraft, setAssistantDraft] = useState<AssistantDraft>(emptyDraft);
  const [assistantError, setAssistantError] = useState("");
  // 出所と区分の組み合わせごとの参照できるページ数。サーバーが索引を数えて返す
  const [scopeCounts, setScopeCounts] = useState<ScopeCounts>({});
  const bottom = useRef<HTMLDivElement>(null);
  const conversation = useRef<HTMLElement>(null);
  const questionInput = useRef<HTMLTextAreaElement>(null);
  // 下端に張り付いているか。読み返すために上へ動かした人を、回答が届くたびに
  // 引き戻さないための判定である
  const stickToBottom = useRef(true);
  // 末尾から離れて読んでいるか。離れている間だけ、入力欄の上へ戻るボタンを出す
  const [awayFromBottom, setAwayFromBottom] = useState(false);
  // 実行中の質問。停止ボタンから中断する
  const inflight = useRef<AbortController | null>(null);
  const headerMenus = useRef<HTMLDivElement>(null);
  const historyArea = useRef<HTMLElement>(null);
  // ポップオーバーを閉じたときの戻り先。保持しないとフォーカスがbodyへ落ち、
  // キーボードだけの利用者はタブ順の最初からやり直しになる
  const feedbackTrigger = useRef<HTMLButtonElement>(null);
  const noticeTrigger = useRef<HTMLButtonElement>(null);
  const profileTrigger = useRef<HTMLButtonElement>(null);
  const historyMenuTrigger = useRef<HTMLButtonElement>(null);
  const syncedChats = useRef<Map<string, string>>(new Map());

  const activeAssistant = assistants.find((item) => item.id === assistantId);
  const formAssistant = assistantForm?.assistantId
    ? assistants.find((item) => item.id === assistantForm.assistantId)
    : undefined;
  const assistantFormReadOnly = assistantForm?.mode === "view";
  const activeChat = chats.find((chat) => chat.id === activeChatId);
  /**
   * いま連携している参照先。入力欄の印に使う。
   *
   * ⚠️ **Discordは連携したサーバーの有無で決まる**（オン・オフを別に持たない）。
   * 使えなくなったものは出さない。印だけ残ると、読んでいるつもりで読んでいない。
   */
  const connectedTools = [
    ...(settings && settings.discord.connected.length > 0 ? [{ id: "discord", name: "Discord" }] : []),
    ...(settings?.tools ?? [])
      .filter((tool) => tool.enabled && tool.available)
      .map((tool) => ({ id: tool.id, name: tool.name })),
  ];
  const streaming = chats.some((chat) => chat.turns.some((turn) => turn.streaming));
  const unreadCount = announcements.filter((announcement) => !readAnnouncementIds.includes(announcement.id)).length;
  const historySections = groupChats(chats);
  const sortedAssistants = [...assistants].sort((left, right) => {
    if (assistantSort === "favorite") {
      return Number(favoriteAssistantIds.includes(right.id)) - Number(favoriteAssistantIds.includes(left.id));
    }
    if (assistantSort === "author") {
      return left.author.localeCompare(right.author, "ja") || left.name.localeCompare(right.name, "ja");
    }
    return left.name.localeCompare(right.name, "ja");
  });

  useLayoutEffect(() => {
    resizeComposerTextarea(questionInput.current);
  }, [question]);

  async function restoreHistory() {
    try {
      const restored = (await listChats())
        .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
        .slice(0, APP_LIMITS.chats)
        .map((chat) => ({
          ...chat,
          turns: chat.turns.map((turn) => ({ ...turn, status: "", streaming: false })),
        }));
      syncedChats.current = new Map(restored.map((chat) => [chat.id, JSON.stringify(chat)]));
      setChats(restored);
      setActiveChatId((current) => current && restored.some((chat) => chat.id === current)
        ? current
        : restored[0]?.id ?? null);
    } catch {
      syncedChats.current = new Map();
      setChats([]);
      setActiveChatId(null);
      showToast("チャット履歴を同期できませんでした");
    }
  }

  useEffect(() => {
    session().then((current) => {
      setAuthed(current.authenticated);
      setUsername(current.username);
      setProfileIcon(current.icon ?? "");
      setRemaining(current.remaining);
		setIsAdmin(current.admin);
		if (location.pathname === "/admin" && !current.admin) {
			history.replaceState(null, "", "/");
			setView("chat");
		}
      if (current.authenticated) {
        void restoreAfterSignIn();
      }
    });
  }, []);

	useEffect(() => {
		const followPath = () => {
			if (location.pathname === "/admin" && isAdmin) setView("admin");
			else if (location.pathname === "/settings") setView("settings");
			else if (view === "admin" || view === "settings") setView("chat");
		};
		window.addEventListener("popstate", followPath);
		return () => window.removeEventListener("popstate", followPath);
	}, [isAdmin, view]);

	// 設定画面から出たらURLも戻す。**出口を1か所にまとめる**（チャットへ戻る道は
	// 新しいチャット・履歴・アシスタントと複数あり、各所へ書くと必ず漏れる）
	useEffect(() => {
		if (view !== "settings" && location.pathname === "/settings") {
			history.replaceState(null, "", "/");
		}
	}, [view]);

  useEffect(() => {
    fetch("/announcements.json")
      .then((response) => (response.ok ? response.json() : []))
      .then((value: unknown) => {
        if (!Array.isArray(value)) return;
        setAnnouncements(
          value.filter(
            (item): item is Announcement =>
              typeof item === "object" &&
              item !== null &&
              typeof (item as Announcement).id === "string" &&
              typeof (item as Announcement).title === "string" &&
              typeof (item as Announcement).body === "string" &&
              typeof (item as Announcement).date === "string",
          ),
        );
      })
      .catch(() => setAnnouncements([]));
  }, []);

  useEffect(() => {
    if (!noticeOpen && !profileOpen && !historyMenuId && !renamingChatId && !feedbackOpen) return;
    const closeOnOutsideClick = (event: PointerEvent) => {
      if (!headerMenus.current?.contains(event.target as Node)) {
        setNoticeOpen(false);
        setProfileOpen(false);
        setFeedbackOpen(false);
      }
      if (!historyArea.current?.contains(event.target as Node)) setHistoryMenuId(null);
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      // 開いていたものを開いた元へ戻す。同時に開くのは1つなので上から順で決まる
      const restore = feedbackOpen ? feedbackTrigger.current
        : noticeOpen ? noticeTrigger.current
        : profileOpen ? profileTrigger.current
        : historyMenuId ? historyMenuTrigger.current
        : null;
      setNoticeOpen(false);
      setProfileOpen(false);
      setFeedbackOpen(false);
      setHistoryMenuId(null);
      setRenamingChatId(null);
      restore?.focus();
    };
    window.addEventListener("pointerdown", closeOnOutsideClick);
    window.addEventListener("keydown", closeOnEscape);
    return () => {
      window.removeEventListener("pointerdown", closeOnOutsideClick);
      window.removeEventListener("keydown", closeOnEscape);
    };
  }, [feedbackOpen, historyMenuId, noticeOpen, profileOpen, renamingChatId]);

  useEffect(() => {
    const wide = window.matchMedia(LAYOUT_QUERY.wide);
    const followViewport = (event: MediaQueryListEvent) => setSidebarOpen(event.matches);
    wide.addEventListener("change", followViewport);
    const small = window.matchMedia(LAYOUT_QUERY.narrow);
    const followWidth = (event: MediaQueryListEvent) => setNarrow(event.matches);
    small.addEventListener("change", followWidth);
    return () => {
      wide.removeEventListener("change", followViewport);
      small.removeEventListener("change", followWidth);
    };
  }, []);

  // 利用者が自分でスクロールしたかを見る。下端から離れていれば追従をやめる
  useEffect(() => {
    const area = conversation.current;
    if (!area) return;
    const follow = () => {
      stickToBottom.current = area.scrollHeight - area.scrollTop - area.clientHeight < STICK_TO_BOTTOM_PX;
    };
    follow();
    area.addEventListener("scroll", follow, { passive: true });

    // 末尾へ戻るボタンの出し入れ。
    //
    // **スクロール位置の計算ではなく、末尾の目印が見えているかで判定する。**
    // 回答は1文字ずつ伸びるので、スクロール操作が無いまま下端が遠ざかる。
    // 計算方式だと、そのとき測り直す仕掛けを別に持つことになる。
    const sentinel = bottom.current;
    const watcher = sentinel
      ? new IntersectionObserver(([entry]) => setAwayFromBottom(!entry.isIntersecting), {
          root: area,
          // 目印の少し手前でも「末尾にいる」とみなす。ぴったり0だと、
          // 1px足りないだけでボタンが出たり消えたりする
          rootMargin: `0px 0px ${SHOW_SCROLL_BOTTOM_PX}px 0px`,
        })
      : null;
    watcher?.observe(sentinel!);

    return () => {
      area.removeEventListener("scroll", follow);
      watcher?.disconnect();
    };
  }, [view, authed, activeChatId]);

  // 往復が増えたときとチャットを切り替えたときだけ、滑らかに送る
  const turnCount = activeChat?.turns.length ?? 0;
  useEffect(() => {
    stickToBottom.current = true;
    bottom.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [activeChatId, turnCount]);

  /**
   * 回答が伸びる間の追従。
   *
   * 以前は `activeChat?.turns` を依存にしていたが、**1文字届くたびに配列が
   * 作り直されるので、デルタごとに smooth スクロールが走っていた。**
   * 読み返そうと上へ動かしても即座に引き戻され、スマホではアニメーションが
   * 連続して重くなる（2026-08-11に確認）。
   *
   * 下端に張り付いているときだけ、アニメーションなしで位置を合わせる。
   */
  const streamingLength = activeChat?.turns.at(-1)?.answer.length ?? 0;
  useEffect(() => {
    if (!stickToBottom.current) return;
    const area = conversation.current;
    if (area) area.scrollTop = area.scrollHeight;
  }, [streamingLength]);

  useEffect(() => {
    if (!authed || !username || streaming) return;
    const timer = window.setTimeout(() => {
      const pending = chats.slice(0, APP_LIMITS.chats).filter((chat) => (
        syncedChats.current.get(chat.id) !== JSON.stringify(chat)
      ));
      void Promise.allSettled(pending.map(async (chat) => {
        await saveChat(chat);
        syncedChats.current.set(chat.id, JSON.stringify(chat));
      })).then((results) => {
        if (results.some((result) => result.status === "rejected")) {
          showToast("チャット履歴を同期できませんでした");
        }
      });
    }, UI_TIMING.historySaveDebounceMs);
    return () => window.clearTimeout(timer);
  }, [authed, chats, streaming, username]);

  useEffect(() => {
    if (!chats.some((chat) => chat.turns.some((turn) => turn.retryAt))) return;
    const timer = window.setInterval(() => setClock(Date.now()), UI_TIMING.clockTickMs);
    return () => window.clearInterval(timer);
  }, [chats]);

  async function handleLogin(event: React.FormEvent) {
    event.preventDefault();
    setLoginError("");
    setBusy(true);
    const error = await login(form.username, form.password);
    setBusy(false);
    // 成否にかかわらず、入力したWikiパスワードは即座に画面から消す。
    setForm((current) => ({ ...current, password: "" }));
    setShowPassword(false);
    if (error) {
      setLoginError(error);
      return;
    }
    const current = await session();
    setAuthed(true);
    setUsername(current.username);
    setProfileIcon(current.icon ?? "");
    setRemaining(current.remaining);
		setIsAdmin(current.admin);
		if (location.pathname === "/admin" && !current.admin) {
			history.replaceState(null, "", "/");
			setView("chat");
		}
    void restoreAfterSignIn();
  }

  /**
   * ログイン後に読み直すもの。
   *
   * **起動時（セッション復元）とログイン直後で同じ関数を通す。** 以前は2か所へ
   * 並べて書いており、連携の読み込みを片方だけに足していたため、
   * **一度ログインしたまま開き直すと連携が消えていた**（2026-09-13に発覚）。
   */
  async function restoreAfterSignIn() {
    await Promise.all([restoreHistory(), refreshAssistants(), refreshSettings()]);
  }

  async function handleLogout() {
    setNoticeOpen(false);
    setProfileOpen(false);
    setFeedbackOpen(false);
    // 受け取り先を消す前に止める。残しても書き込む先が無い
    inflight.current?.abort();
    await logout();
    setAuthed(false);
    setUsername("");
    setProfileIcon("");
		setIsAdmin(false);
		history.replaceState(null, "", "/");
		setView("chat");
    syncedChats.current.clear();
    setChats([]);
    setActiveChatId(null);
  }

  async function handlePickProfileIcon(event: React.ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file || profileIconBusy) return;
    setProfileIconBusy(true);
    try {
      const icon = await toIconDataURL(file);
      setProfileIcon(await updateProfileIcon(icon));
      showToast("利用者画像を変更しました");
    } catch (error) {
      showToast(error instanceof Error ? error.message : "利用者画像を保存できませんでした");
    } finally {
      setProfileIconBusy(false);
    }
  }

  async function removeProfileIcon() {
    if (!profileIcon || profileIconBusy) return;
    setProfileIconBusy(true);
    try {
      await updateProfileIcon("");
      setProfileIcon("");
      showToast("利用者画像を外しました");
    } catch (error) {
      showToast(error instanceof Error ? error.message : "利用者画像を外せませんでした");
    } finally {
      setProfileIconBusy(false);
    }
  }

	function openAdmin() {
		setProfileOpen(false);
		history.pushState(null, "", "/admin");
		setView("admin");
	}

	function closeAdmin() {
		history.pushState(null, "", "/");
		setView("chat");
	}

	/**
	 * 設定画面を開く。
	 *
	 * **URLを持たせる。** 「設定はここ」と人に伝えられるようにするためと、
	 * 戻るボタンでチャットへ戻れるようにするため（管理画面と同じ作り）。
	 */
	function openSettings() {
		setProfileOpen(false);
		setSettingsError("");
		// 開くたびに読み直す。管理者が許可を出した直後でも、開けば最新になる
		void refreshSettings();
		if (location.pathname !== "/settings") history.pushState(null, "", "/settings");
		setView("settings");
		closeSidebarOnMobile();
	}

  function handleNoticeToggle() {
    const opening = !noticeOpen;
    setNoticeOpen(opening);
    setProfileOpen(false);
    setFeedbackOpen(false);
    if (!opening || announcements.length === 0) return;
    const ids = announcements.map((announcement) => announcement.id);
    setReadAnnouncementIds(ids);
    // お知らせIDだけなので、非公開Wikiの内容を端末へ保存することにはならない。
    writeStored("local", ANNOUNCEMENT_READ_KEY, JSON.stringify(ids));
  }

  function handleFeedbackToggle() {
    const opening = !feedbackOpen;
    setFeedbackOpen(opening);
    setNoticeOpen(false);
    setProfileOpen(false);
    if (!opening) return;
    setGeneralFeedbackId(makeId());
    setGeneralReason(null);
    setGeneralComment("");
  }

  async function sendGeneralFeedback() {
    if (!generalReason || generalSubmitting) return;
    setGeneralSubmitting(true);
    try {
      await submitFeedback({
        clientId: generalFeedbackId,
        kind: "general",
        reasons: [generalReason],
        comment: generalComment.trim(),
        page: view === "assistants" ? "assistants" : "chat",
      });
      setFeedbackOpen(false);
      showToast(feedbackNotificationMessage());
    } catch (error) {
      showToast(error instanceof Error ? error.message : "フィードバックを送信できませんでした");
    } finally {
      setGeneralSubmitting(false);
    }
  }

  function patchTurnFeedback(chatId: string, turnIndex: number, update: Partial<Turn>) {
    setChats((current) => current.map((chat) => chat.id === chatId
      ? { ...chat, turns: chat.turns.map((turn, index) => index === turnIndex ? { ...turn, ...update } : turn) }
      : chat));
  }

  async function sendAnswerFeedback(
    chat: Chat,
    turnIndex: number,
    turn: Turn,
    rating: "good" | "bad",
    reasons: FeedbackReason[],
    comment: string,
  ) {
    return submitFeedback({
      clientId: `${chat.id}:${turnIndex}`,
      kind: "answer",
      rating,
      reasons,
      comment,
      question: turn.question,
      answer: turn.answer.slice(0, FEEDBACK_ANSWER_MAX),
      sources: turn.sources.slice(0, FEEDBACK_SOURCE_MAX),
      assistantId: turn.assistantId,
      assistantName: turn.assistantName,
      responseMode: turn.responseMode,
      resolvedMode: turn.resolvedMode,
      timings: turn.timings,
      chatId: chat.id,
      turnIndex,
      page: "chat",
    });
  }

  async function handleAnswerRating(chat: Chat, turnIndex: number, turn: Turn, rating: "good" | "bad") {
    const key = `${chat.id}:${turnIndex}`;
    const previous = {
      feedbackRating: turn.feedbackRating,
      feedbackReasons: turn.feedbackReasons,
      feedbackComment: turn.feedbackComment,
    };
    const reasons = turn.feedbackRating === rating ? (turn.feedbackReasons ?? []) : [];
    const comment = turn.feedbackRating === rating ? (turn.feedbackComment ?? "") : "";
    patchTurnFeedback(chat.id, turnIndex, {
      feedbackRating: rating,
      feedbackReasons: reasons,
      feedbackComment: comment,
    });
    setAnswerFeedbackOpen(key);
    setAnswerComments((current) => ({ ...current, [key]: comment }));
    try {
      await sendAnswerFeedback(chat, turnIndex, turn, rating, reasons, comment);
      showToast(feedbackNotificationMessage("評価"));
    } catch (error) {
      patchTurnFeedback(chat.id, turnIndex, previous);
      showToast(error instanceof Error ? error.message : "評価を送信できませんでした");
    }
  }

  async function toggleAnswerReason(chat: Chat, turnIndex: number, turn: Turn, reason: FeedbackReason) {
    if (!turn.feedbackRating) return;
    const current = turn.feedbackReasons ?? [];
    const reasons = current.includes(reason) ? current.filter((item) => item !== reason) : [...current, reason];
    patchTurnFeedback(chat.id, turnIndex, { feedbackReasons: reasons });
    try {
      await sendAnswerFeedback(chat, turnIndex, turn, turn.feedbackRating, reasons, turn.feedbackComment ?? "");
      showToast(feedbackNotificationMessage("理由"));
    } catch (error) {
      patchTurnFeedback(chat.id, turnIndex, { feedbackReasons: current });
      showToast(error instanceof Error ? error.message : "理由を送信できませんでした");
    }
  }

  async function submitAnswerComment(chat: Chat, turnIndex: number, turn: Turn) {
    if (!turn.feedbackRating) return;
    const key = `${chat.id}:${turnIndex}`;
    const comment = (answerComments[key] ?? "").trim();
    patchTurnFeedback(chat.id, turnIndex, { feedbackComment: comment });
    try {
      await sendAnswerFeedback(chat, turnIndex, turn, turn.feedbackRating, turn.feedbackReasons ?? [], comment);
      showToast(feedbackNotificationMessage("補足"));
    } catch (error) {
      showToast(error instanceof Error ? error.message : "補足を送信できませんでした");
    }
  }

  /**
   * 回答の生成を止める。
   *
   * `thinking` は `fast` の4.1倍かかる（M23）。聞き方を間違えたと気づいたときに、
   * 終わるまで待つか再読み込みするしかない状態だった。中断してもサーバー側の
   * 1回は消費済みなので、**残回数は戻らない**（`finally` で取り直す）。
   */
  function handleStop() {
    inflight.current?.abort();
  }

  async function handleCopyAnswer(turn: Turn) {
    try {
      await navigator.clipboard.writeText(answerPlainText(turn));
      showToast("回答をコピーしました");
    } catch {
      showToast("コピーできませんでした");
    }
  }

  /**
   * 同じ質問を入力欄へ戻す。
   *
   * 以前はその場で回答を作り直していたが、**前の回答が消えてしまう**ので、
   * 作り直した結果が悪かったときに戻せなかった（2026-09-12の報告）。
   * 入力欄へ入れるだけにすれば、直してから送るのも、やめるのも選べる。
   * 質問の消費も、実際に送るまで起きない。
   */
  function handleRegenerate(_chat: Chat, _turnIndex: number, turn: Turn) {
    if (!turn.question.trim()) {
      showToast("画像だけの質問は入力欄へ戻せません");
      return;
    }
    setQuestion(turn.question);
    const input = questionInput.current;
    if (input) {
      input.focus();
      resizeComposerTextarea(input);
    }
    showToast("同じ質問を入力欄へ入れました");
  }

  function handleNewChat() {
    if (streaming) return;
    setActiveChatId(null);
    setQuestion("");
    setView("chat");
    closeSidebarOnMobile();
  }

  function closeSidebarOnMobile() {
    if (window.matchMedia(LAYOUT_QUERY.compact).matches) setSidebarOpen(false);
  }

  function handleTogglePin(chatId: string) {
    setChats((current) => current.map((chat) => (
      chat.id === chatId ? { ...chat, pinned: !chat.pinned } : chat
    )));
    setHistoryMenuId(null);
  }

  function handleRenameStart(chat: Chat) {
    setRenameTitle(chat.title);
    setRenamingChatId(chat.id);
    setHistoryMenuId(null);
  }

  function handleRenameSubmit(event: React.FormEvent) {
    event.preventDefault();
    const title = renameTitle.trim();
    if (!title || !renamingChatId) return;
    setChats((current) => current.map((chat) => (
      chat.id === renamingChatId ? { ...chat, title } : chat
    )));
    setRenamingChatId(null);
  }

  async function handleShare(chat: Chat) {
    setHistoryMenuId(null);
    if (!window.confirm("このチャットには非公開Wikiの内容が含まれる可能性があります。共有してよいですか？")) return;
    const text = shareText(chat);
    if (navigator.share) {
      try {
        await navigator.share({ title: chat.title, text });
        return;
      } catch (error) {
        if (error instanceof DOMException && error.name === "AbortError") return;
      }
    }
    try {
      await navigator.clipboard.writeText(text);
      showToast("チャットをクリップボードにコピーしました");
    } catch {
      showToast("共有できませんでした");
    }
  }

  async function handleDeleteChat(chat: Chat) {
    setHistoryMenuId(null);
    if (chat.turns.some((turn) => turn.streaming)) return;
    if (!window.confirm(`「${chat.title}」を削除しますか？`)) return;
    const next = chats.filter((item) => item.id !== chat.id);
    setChats(next);
    syncedChats.current.delete(chat.id);
    if (activeChatId === chat.id) setActiveChatId(next[0]?.id ?? null);
    try {
      await deleteChat(chat.id);
    } catch {
      showToast("チャット履歴を削除できませんでした");
      void restoreHistory();
    }
  }

  /**
   * 連携の状態を読み直す。
   *
   * **使えるかどうかも、つないである先も、サーバーが決める。** 画面に固定で
   * 並べると、未設定のものが押せてしまい「つないだのに効かない」になる。
   */
  async function refreshSettings() {
    try {
      setSettings(await fetchSettings());
      setSettingsError("");
    } catch (error) {
      // 読めなかったことは、読めなかったと出す。空の一覧を出すと、
      // つないだ覚えのある人に「外れた」と思わせる
      setSettings(null);
      setSettingsError(error instanceof Error ? error.message : "連携の状態を読み込めませんでした");
    }
  }

  /** 連携を保存する。**保存した結果で描き直す**（画面の手元の値を正としない）。 */
  async function applySettings(next: { tools: string[]; discordServers: string[] }, busyId: string, done: string) {
    setSettingsBusy(busyId);
    setSettingsError("");
    try {
      setSettings(await saveSettings(next));
      showToast(done);
    } catch (error) {
      setSettingsError(error instanceof Error ? error.message : "連携を保存できませんでした");
    } finally {
      setSettingsBusy("");
    }
  }

  /** いま保存してある連携から、画面へ出す一覧を作る。Discordは連携の有無で決まる */
  function currentConnections(): { tools: string[]; discordServers: string[] } {
    return {
      tools: (settings?.tools ?? []).filter((tool) => tool.enabled).map((tool) => tool.id),
      discordServers: (settings?.discord.connected ?? []).map((server) => server.id),
    };
  }

  function toggleTool(id: string, enabled: boolean) {
    const current = currentConnections();
    const name = settings?.tools.find((tool) => tool.id === id)?.name ?? id;
    void applySettings(
      { ...current, tools: enabled ? [...current.tools, id] : current.tools.filter((tool) => tool !== id) },
      id,
      enabled ? `${name}を参照するようにしました` : `${name}の参照をやめました`,
    );
  }

  function connectDiscordServer(id: string) {
    const current = currentConnections();
    const name = settings?.discord.joinable.find((server) => server.id === id)?.name ?? id;
    void applySettings(
      { ...current, discordServers: [...current.discordServers, id] },
      id,
      `「${name}」を検索先に追加しました`,
    );
  }

  function disconnectDiscordServer(id: string) {
    const current = currentConnections();
    const name = settings?.discord.connected.find((server) => server.id === id)?.name ?? id;
    void applySettings(
      { ...current, discordServers: current.discordServers.filter((server) => server !== id) },
      id,
      `「${name}」を検索先から外しました`,
    );
  }

  /** ボットを入れた直後のための取り直し。サーバー側が一覧を10分覚えている */
  async function refreshDiscordServers() {
    setServersRefreshing(true);
    try {
      setSettings(await fetchSettings(true));
      setSettingsError("");
    } catch {
      // ⚠️ **取り直しに失敗しても、いま出ている一覧は消さない。**
      // 消すと、連携中のサーバーまで画面から消えて外れたように見える
      setSettingsError("サーバーの一覧を取り直せませんでした");
    } finally {
      setServersRefreshing(false);
    }
  }

  async function refreshAssistants() {
    try {
      const { assistants: list, teams: names, scopeCounts: counts } = await listAssistants();
      setAssistants(list);
      setTeams(names);
      setScopeCounts(counts);
      // 削除済みのIDを選んだままだと、絞り込みが効いているつもりで
      // 効いていない状態になる。存在しなければ汎用へ戻す
      setAssistantId((current) => (list.some((item) => item.id === current) ? current : ""));
    } catch {
      // アシスタントが読めなくても汎用チャットは使えるので、画面は止めない。
      // ただし選択も外す。一覧だけ空にすると、画面は「汎用」と表示しながら
      // 質問には古いIDを送る状態になり、表示と実際の参照範囲が食い違う
      setAssistants([]);
      setAssistantId("");
      writeStored("local", ASSISTANT_KEY, null);
    }
  }

  function chooseAssistant(id: string, selectedName?: string) {
    if (streaming) {
      showToast("回答が終わってからアシスタントを切り替えてください");
      return;
    }
    const changed = id !== assistantId;
    const name = selectedName ?? assistants.find((item) => item.id === id)?.name ?? "汎用";
    setAssistantId(id);
    writeStored("local", ASSISTANT_KEY, id);
    // 選んだらチャットへ戻る。選ぶために開いた画面なので、留まる理由がない
    closeAssistantForm();
    setView("chat");
    closeSidebarOnMobile();
    if (!changed) return;
    // 同じ履歴で参照範囲や一般知識の可否が途中から変わると、過去回答を
    // どの規則で読めばよいか分からなくなる。切り替え時は会話を分離する。
    setActiveChatId(null);
    setQuestion("");
    showToast(`「${name}」に切り替え、新しいチャットを開きました`);
  }

  function openAssistants() {
    if (streaming) {
      showToast("回答が終わってからアシスタントを切り替えてください");
      return;
    }
    setAssistantError("");
    closeAssistantForm();
    setView("assistants");
    closeSidebarOnMobile();
  }

  function toggleFavoriteAssistant(item: Assistant) {
    const adding = !favoriteAssistantIds.includes(item.id);
    const next = adding
      ? [...favoriteAssistantIds, item.id]
      : favoriteAssistantIds.filter((id) => id !== item.id);
    setFavoriteAssistantIds(next);
    writeStored("local", ASSISTANT_FAVORITES_KEY, JSON.stringify(next));
    showToast(adding
      ? `「${item.name}」をお気に入りに追加しました`
      : `「${item.name}」をお気に入りから外しました`);
  }

  function changeAssistantSort(value: string) {
    const next = value === "name" || value === "author" ? value : "favorite";
    setAssistantSort(next);
    writeStored("local", ASSISTANT_SORT_KEY, next);
  }

  function closeAssistantForm() {
    setAssistantForm(null);
  }

  function startCreate() {
    setAssistantDraft(emptyDraft);
    setAssistantError("");
    setAssistantForm({ mode: "create" });
  }

  /** 閲覧から編集へ入る。開いた瞬間から編集できる作りにはしない。 */
  function startEditFromView() {
    if (!assistantForm?.assistantId) return;
    setAssistantError("");
    setAssistantForm({ mode: "edit", assistantId: assistantForm.assistantId });
  }

  function openAssistantSettings(item: Assistant) {
    if (streaming) {
      showToast("回答が終わってからアシスタントの設定を開いてください");
      return;
    }
    setAssistantDraft({
      id: item.id,
      name: item.name,
      description: item.description,
      instruction: item.instruction,
      team: item.team,
      origin: item.origin,
      icon: item.icon,
      glossary: item.glossary ?? [],
    });
    setAssistantError("");
    // **作成者でもまず閲覧から始める。** 設定を見に来ただけのときに編集欄が
    // 開いていると、触るつもりのない値を書き換えてしまう。編集は明示的に入る。
    setAssistantForm({ mode: "view", assistantId: item.id });
    setView("assistants");
    closeSidebarOnMobile();
  }

  async function handleSubmitAssistant(event: React.FormEvent) {
    event.preventDefault();
    setAssistantError("");
    const editing = assistantForm?.mode === "edit" ? assistantForm.assistantId ?? null : null;
    if (assistantForm?.mode === "view") return;
    try {
      if (editing) {
        const saved = await updateAssistant(editing, assistantDraft);
        setAssistants((current) => current.map((item) => (item.id === editing ? saved : item)));
        closeAssistantForm();
        showToast(`「${saved.name}」を保存しました`);
        return;
      }
      const created = await createAssistant({
        ...assistantDraft,
        // 日本語名だとslugがほぼ空になるので、そのときは時刻から作る
        id: assistantDraft.id || slugify(assistantDraft.name) || `assistant-${Date.now().toString(36)}`,
      });
      setAssistants((current) => [...current, created]);
      setAssistantDraft(emptyDraft);
      chooseAssistant(created.id, created.name);
      showToast(`「${created.name}」を作成し、新しいチャットを開きました`);
    } catch (error) {
      setAssistantError(error instanceof Error ? error.message : "保存できませんでした");
    }
  }

  async function handleDeleteAssistant(target: Assistant) {
    if (!window.confirm(`「${target.name}」を削除しますか？元に戻せません。`)) return;
    try {
      await deleteAssistant(target.id);
      setAssistants((current) => current.filter((item) => item.id !== target.id));
      if (favoriteAssistantIds.includes(target.id)) {
        const nextFavorites = favoriteAssistantIds.filter((id) => id !== target.id);
        setFavoriteAssistantIds(nextFavorites);
        writeStored("local", ASSISTANT_FAVORITES_KEY, JSON.stringify(nextFavorites));
      }
      if (assistantId === target.id) chooseAssistant("");
      showToast(`「${target.name}」を削除しました`);
    } catch (error) {
      setAssistantError(error instanceof Error ? error.message : "削除できませんでした");
    }
  }

  async function handlePickIcon(event: React.ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = ""; // 同じ画像を選び直せるようにする
    if (!file) return;
    setAssistantError("");
    try {
      const icon = await toIconDataURL(file);
      setAssistantDraft((d) => ({ ...d, icon }));
    } catch (error) {
      setAssistantError(error instanceof Error ? error.message : "画像を読み込めませんでした");
    }
  }

  /** 他人のアシスタントは編集できない。複製してから直す（編集権の調整を発生させないため）。 */
  function duplicateAssistant(source: Assistant) {
    setAssistantDraft({
      id: "",
      name: `${source.name}のコピー`,
      description: source.description,
      instruction: source.instruction,
      team: source.team,
      origin: source.origin,
      icon: source.icon,
      glossary: source.glossary ?? [],
    });
    setAssistantError("");
    setAssistantForm({ mode: "create" });
  }

  function turnAssistantAvatar(turn: Turn) {
    if (!turn.assistantName) {
      return <img src={APP_URLS.mark} alt="" className="assistant-avatar" />;
    }
    const item = assistants.find((assistant) => assistant.id === turn.assistantId);
    const avatar = <AssistantAvatar name={turn.assistantName} icon={item?.icon} size={34} />;
    if (!item) return avatar;
    return (
      <button
        type="button"
        className="assistant-avatar-settings"
        aria-label={`「${item.name}」の設定を見る`}
        title="アシスタントの設定を見る"
        onClick={() => openAssistantSettings(item)}
      >
        {avatar}
      </button>
    );
  }

  /**
   * 画像を添付する。ファイル選択・貼り付け・ドラッグの3経路で共通に使う。
   *
   * ミーム画像を扱う流れでは「コピーして貼る」が一番短い。ファイル選択だけに
   * すると、いったん保存してから選び直すことになる。
   */
  async function attachImage(file: File | null | undefined) {
    if (!file || streaming) return;
    try {
      setAttachment(await toAttachment(file));
    } catch (error) {
      showToast(error instanceof Error ? error.message : "画像を添付できませんでした");
    }
  }

  /**
   * ドラッグ中は中身を読めないため、種別だけで画像かどうかを見る。
   * 文字の選択をドラッグしただけで枠が出ないようにするための判定である。
   */
  function imageTypeInDataTransfer(data: DataTransfer | null): boolean {
    if (!data) return false;
    return Array.from(data.items).some(
      (item) => item.kind === "file" && item.type.startsWith("image/"),
    );
  }

  /** 貼り付けられた中身から画像を1枚だけ取り出す。文字だけなら何もしない。 */
  function imageFromDataTransfer(data: DataTransfer | null): File | null {
    if (!data) return null;
    for (const item of Array.from(data.files)) {
      if (item.type.startsWith("image/")) return item;
    }
    return null;
  }

  /**
   * 質問を送る。`regenerateFrom` を渡すと、その往復以降を捨ててから
   * 同じ位置へ入れ直す（＝回答の作り直し）。
   */
  async function handleAsk(text: string, regenerateFrom?: number) {
    const q = text.trim();
    // 画像だけでも送れる。「これでツイート作って」のように、文章より
    // 画像のほうが本体である使い方があるため
    if ((!q && !attachment) || streaming) return;
    setQuestion("");
    // 送ったら添付も外す。**残すと次の質問にも黙って付いていく。**
    // 「もう5案」で使い回せるようにと残していたが、実際に使うと
    // 別の話題へ移ったときに前の画像が紛れ込むほうが困った
    // （2026-08-10に利用者から報告）。使い回したいときは選び直す
    const sent = attachment;
    setAttachment(null);
    // 先読みなので、失敗しても握りつぶす。実際に必要になった時点で
    // 上の lazy 側が読み直しと再読み込みを引き受ける
    void loadMarkdownModule().catch(() => {});

    const now = new Date().toISOString();
    const chatId = activeChat?.id ?? makeId();
    const turnIndex = regenerateFrom ?? activeChat?.turns.length ?? 0;
    // 全履歴を毎回送ると入力費用が増え続ける。指示語の解決に必要な直近2往復だけを送り、
    // 出典やエラー状態はサーバーへ再送しない。長さの上限はサーバー側でも検証する。
    const context = (activeChat?.turns ?? [])
      .slice(0, turnIndex)
      .filter((item) => !item.streaming && item.question.trim() && item.answer.trim())
      .slice(-APP_LIMITS.conversationTurns)
      .map((item) => ({ question: item.question, answer: item.answer.slice(0, APP_LIMITS.conversationAnswerRunes) }));
    const turn: Turn = {
      question: q,
      answer: "",
      sources: [],
      status: "送信中",
      streaming: true,
      // 後から一覧が変わっても、この回答を出したアシスタントを辿れるようにする
      assistantId: activeAssistant?.id,
      assistantName: activeAssistant?.name,
      responseMode,
      // 画像そのものは履歴へ保存しない。印だけ残す
      hasAttachment: sent !== null,
    };

    // 送信した瞬間に質問の吹き出しを出し、過去の会話への追質問なら履歴の先頭へ戻す。
    setChats((current) => {
      const existing = current.find((chat) => chat.id === chatId);
      const updated: Chat = existing
        ? { ...existing, updatedAt: now, turns: [...existing.turns.slice(0, turnIndex), turn] }
        : {
            id: chatId,
            // 本文が空でも名前が付く。画像だけを送ると空欄になっていた
            title: chatTitle(q, sent !== null),
            createdAt: now,
            updatedAt: now,
            turns: [turn],
          };
      return [updated, ...current.filter((chat) => chat.id !== chatId)].slice(0, APP_LIMITS.chats);
    });
    setActiveChatId(chatId);

    const patch = (update: (turn: Turn) => Turn) =>
      setChats((current) =>
        current.map((chat) =>
          chat.id === chatId
            ? {
                ...chat,
                turns: chat.turns.map((item, index) => (index === turnIndex ? update(item) : item)),
              }
            : chat,
        ),
      );

    const controller = new AbortController();
    inflight.current = controller;

    try {
      await ask(q, (event) => {
      switch (event.type) {
        case "mode":
          patch((current) => ({ ...current, resolvedMode: event.mode }));
          break;
        case "status":
          patch((current) => ({ ...current, status: event.message, retryAt: event.retry_at }));
          break;
        case "timing": {
          const key: `${StageTimingName}Ms` = `${event.stage}Ms`;
          patch((current) => ({
            ...current,
            timings: { ...current.timings, [key]: event.milliseconds },
          }));
          break;
        }
        case "pages":
          // 該当ページが0件のとき、サーバーは pages を省いて送ってくる。
          // そのまま入れると undefined が履歴へ保存され、読み直したときに
          // sources が無い（＝null）状態になって描画時に落ちる
          patch((current) => ({ ...current, sources: event.pages ?? [] }));
          break;
        case "delta":
          patch((current) => ({ ...current, answer: current.answer + event.text, status: "", retryAt: undefined }));
          break;
        case "error":
          patch((current) => ({
            ...current,
            error: event.message,
            errorCode: event.code,
            streaming: false,
            status: "",
            retryAt: event.retry_at,
          }));
          break;
        case "done":
          patch((current) => ({ ...current, streaming: false, status: "", retryAt: undefined }));
          break;
      }
      }, controller.signal, assistantId, context, responseMode, sent ? [sent.dataUrl] : []);
    } catch (error) {
      // fetch自体の失敗やストリームの切断は ask() の中でイベントにならない。
      // ここで拾わないと streaming が立ったままになり、入力欄が永久に
      // disabled のまま、履歴同期も残回数の更新も止まる
      // ブラウザによって、中断時の例外は AbortError にも NetworkError にもなる。
      // 例外の名前ではなく、自分で止めたかどうかで判断する
      const stopped = controller.signal.aborted || (error instanceof Error && error.name === "AbortError");
      patch((current) => ({
        ...current,
        // 途中まで届いていれば、それは利用者が読める回答である。止めた事実を
        // エラーとして被せず、そこまでを残して評価もできるようにする
        error: stopped
          ? (current.answer ? undefined : "送信を中止しました")
          : "通信に失敗しました。接続を確認してもう一度お試しください",
        streaming: false,
        status: "",
        retryAt: undefined,
      }));
    } finally {
      inflight.current = null;
      patch((current) => ({ ...current, streaming: false, status: "" }));
      // Gemini側の制限などでサーバーが回数を返却する場合もあるため、推測で1回減らさない。
      try {
        const current = await session();
        setProfileIcon(current.icon ?? "");
        setRemaining(current.remaining);
      } catch {
        // 残回数が取れなくても操作は続けられる。次の質問で取り直す
      }
    }
  }

  if (authed === null) return <LoadingScreen label="WASA Chatを読み込んでいます" message="読み込み中" />;

  if (!authed) {
    return (
      <div className="center login-page">
        <form className="card gate" onSubmit={handleLogin}>
          <img src={APP_URLS.logo} alt="WASA Chat" className="brand-logo-large" />
          <div className="login-heading">
            <h1 className="visually-hidden">WASA Chat</h1>
            <p className="muted">WASA Wikiと同じアカウントでログイン</p>
          </div>
          <label>
            <span>利用者名</span>
            <input
              type="text"
              name="username"
              autoComplete="username"
              value={form.username}
              onChange={(event) => setForm({ ...form, username: event.target.value })}
              autoFocus
            />
          </label>
          <label>
            <span>パスワード</span>
            <div className="password-field">
              <input
                type={showPassword ? "text" : "password"}
                name="password"
                autoComplete="current-password"
                value={form.password}
                onChange={(event) => setForm({ ...form, password: event.target.value })}
              />
              <button
                type="button"
                className="password-toggle"
                onClick={() => setShowPassword((visible) => !visible)}
                aria-label={showPassword ? "パスワードを隠す" : "パスワードを表示"}
                aria-pressed={showPassword}
              >
                {showPassword ? (
                  <svg viewBox="0 0 24 24" aria-hidden="true">
                    <path d="m3 3 18 18M10.6 10.7a2 2 0 0 0 2.7 2.7M9.9 5.2A10.8 10.8 0 0 1 12 5c5.5 0 9 7 9 7a16 16 0 0 1-2.2 3.2M6.2 6.2C4.2 7.6 3 10 3 12c0 0 3.5 7 9 7 1.2 0 2.3-.3 3.3-.7" />
                  </svg>
                ) : (
                  <svg viewBox="0 0 24 24" aria-hidden="true">
                    <path d="M3 12s3.5-7 9-7 9 7 9 7-3.5 7-9 7-9-7-9-7Z" />
                    <circle cx="12" cy="12" r="2.5" />
                  </svg>
                )}
              </button>
            </div>
          </label>
          {loginError && <p className="error" role="alert">{loginError}</p>}
          <button type="submit" disabled={busy || !form.username || !form.password}>
            {busy ? "Wikiで確認中…" : "ログイン"}
          </button>
          <p className="note">パスワードはWikiで本人確認をするためだけに使用します</p>
          {/* ログイン前に読めないと、初めて使う人が何に同意して入るのか分からない */}
          <p className="login-links">
            <a href={APP_URLS.support} target="_blank" rel="noreferrer noopener">使い方</a>
            <a href={`${APP_URLS.support}#terms`} target="_blank" rel="noreferrer noopener">注意事項</a>
            <a href={`${APP_URLS.support}#privacy`} target="_blank" rel="noreferrer noopener">プライバシーポリシー</a>
          </p>
        </form>
      </div>
    );
  }

	if (view === "admin" && isAdmin) {
		return <Suspense fallback={<LoadingScreen label="管理者情報を確認しています" admin />}><AdminPage username={username} profileIcon={profileIcon} onBack={closeAdmin} onLogout={() => void handleLogout()} /></Suspense>;
	}

  return (
    <div className={`app-shell ${sidebarOpen ? "" : "sidebar-collapsed"}`}>
      <aside className="history-panel" aria-label="チャット履歴" aria-hidden={!sidebarOpen}>
        <div className="brand-row">
          <div className="brand">
            <img src={APP_URLS.logo} alt="WASA Chat" className="brand-logo" />
          </div>
          <button type="button" className="sidebar-close" onClick={() => setSidebarOpen(false)} aria-label="履歴を閉じる">
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m15 6-6 6 6 6" /></svg>
          </button>
        </div>

        <button type="button" className="new-chat" onClick={handleNewChat} disabled={streaming}>
          <span aria-hidden="true">＋</span> 新しいチャット
        </button>

        {/* 選択中の名前をここに出す。ヘッダーの表示をやめた分、どのアシスタントで
            答えているかが分かる場所がここだけになるため、入口と現在値を兼ねさせる */}
        <button
          type="button"
          className={`sidebar-assistant ${activeAssistant ? "on" : ""} ${view === "assistants" ? "active" : ""}`}
          onClick={openAssistants}
          aria-current={view === "assistants"}
        >
          {activeAssistant
            ? <AssistantAvatar name={activeAssistant.name} icon={activeAssistant.icon} size={22} />
            : <DefaultAvatar size={22} />}
          <span className="sidebar-assistant-label">アシスタント</span>
          <span className="sidebar-assistant-current">{activeAssistant?.name ?? "汎用"}</span>
        </button>

        <div className="history-heading">
          <span>チャット履歴</span>
        </div>
        <nav className="history-list" ref={historyArea}>
          {chats.length === 0 ? (
            <p className="history-empty">このタブの履歴はまだありません</p>
          ) : (
            historySections.map((section) => (
              <section className="history-section" key={section.label}>
                <h2>{section.label}</h2>
                {section.chats.map((chat) => (
                  <div className={`history-item ${historyMenuId === chat.id ? "menu-open" : ""}`} key={chat.id}>
                    <button
                      type="button"
                      className={`history-select ${chat.id === activeChatId ? "active" : ""}`}
                      aria-current={chat.id === activeChatId ? "page" : undefined}
                      onClick={() => {
                        setActiveChatId(chat.id);
                        setHistoryMenuId(null);
                        setView("chat");
                        closeSidebarOnMobile();
                      }}
                    >
                      <HistoryIcon />
                      <span>{chat.title}</span>
                      {/* aria-labelは役割のない要素では読み上げられないので、
                          ボタン名へ足す隠し文字で伝える */}
                      {chat.turns.some((turn) => turn.streaming) && (
                        <>
                          <i aria-hidden="true" />
                          <span className="visually-hidden">回答中</span>
                        </>
                      )}
                    </button>
                    <button
                      ref={historyMenuId === chat.id ? historyMenuTrigger : undefined}
                      type="button"
                      className="history-more"
                      aria-label={`「${chat.title}」のメニュー`}
                      aria-expanded={historyMenuId === chat.id}
                      onClick={() => setHistoryMenuId((current) => current === chat.id ? null : chat.id)}
                    >
                      <svg viewBox="0 0 24 24" aria-hidden="true">
                        <circle cx="5" cy="12" r="1" /><circle cx="12" cy="12" r="1" /><circle cx="19" cy="12" r="1" />
                      </svg>
                    </button>
                    {historyMenuId === chat.id && (
                      // role="menu" は矢印キーでの移動を約束する役割だが、それは実装していない。
                      // 素のボタンはTabで辿れるので、実装に合う「操作のまとまり」として示す
                      <div className="history-menu" role="group" aria-label={`「${chat.title}」の操作`}>
                        <button type="button" onClick={() => handleTogglePin(chat.id)}>
                          {chat.pinned ? "ピン留めを解除" : "ピン留め"}
                        </button>
                        <button type="button" onClick={() => handleRenameStart(chat)}>タイトル変更</button>
                        <button type="button" onClick={() => handleShare(chat)} disabled={chat.turns.some((turn) => turn.streaming)}>共有する</button>
                        <button type="button" className="danger" onClick={() => handleDeleteChat(chat)} disabled={chat.turns.some((turn) => turn.streaming)}>削除</button>
                      </div>
                    )}
                  </div>
                ))}
              </section>
            ))
          )}
        </nav>

        <div className="account-card">
          <div>
            <span className="account-name">{username}</span>
            <span>本日はあと{remaining}回</span>
          </div>
          <button type="button" className="linkish" onClick={handleLogout}>ログアウト</button>
        </div>
      </aside>
      {sidebarOpen && (
        <button type="button" className="sidebar-backdrop" onClick={() => setSidebarOpen(false)} aria-label="履歴を閉じる" />
      )}

      <section
        className={`chat-panel${dragging ? " dragging" : ""}`}
        // 画面のどこへ落としても添付になる。入力欄の小さな的を狙わせない
        onDragEnter={(event) => {
          if (!imageTypeInDataTransfer(event.dataTransfer)) return;
          dragDepth.current += 1;
          setDragging(true);
        }}
        onDragOver={(event) => {
          // 既定の動作を止めないとブラウザが画像を別タブで開いてしまう
          if (dragDepth.current > 0) event.preventDefault();
        }}
        onDragLeave={() => {
          dragDepth.current = Math.max(0, dragDepth.current - 1);
          if (dragDepth.current === 0) setDragging(false);
        }}
        onDrop={(event) => {
          if (dragDepth.current === 0) return;
          event.preventDefault();
          dragDepth.current = 0;
          setDragging(false);
          void attachImage(imageFromDataTransfer(event.dataTransfer));
        }}
      >
        {dragging && (
          <div className="drop-overlay" aria-hidden="true">
            <span>ここに画像を落とすと添付します</span>
          </div>
        )}
        <header className="chat-header">
          <div className="header-title">
            <button type="button" className="sidebar-toggle" onClick={() => setSidebarOpen((open) => !open)} aria-label="チャット履歴を開く" aria-expanded={sidebarOpen}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h16" /></svg>
            </button>
            <h1>
              {view === "assistants" ? "アシスタント"
                : view === "settings" ? "設定"
                : activeChat?.title ?? "新しいチャット"}
            </h1>
          </div>
          <div className="header-actions" ref={headerMenus}>
            <div className="header-menu-wrap">
              <button
                ref={feedbackTrigger}
                type="button"
                className="feedback-trigger"
                aria-expanded={feedbackOpen}
                aria-controls="feedback-popover"
                onClick={handleFeedbackToggle}
              >
                <svg viewBox="0 0 24 24" aria-hidden="true">
                  <path d="M4 5.5A2.5 2.5 0 0 1 6.5 3h11A2.5 2.5 0 0 1 20 5.5v8a2.5 2.5 0 0 1-2.5 2.5H10l-5 4v-4.5A2.5 2.5 0 0 1 4 13.5v-8Z" />
                  <path d="M8 8h8M8 12h5" />
                </svg>
                <span>改善を送る</span>
              </button>
              {feedbackOpen && (
                <FeedbackPopover
                  reason={generalReason}
                  comment={generalComment}
                  submitting={generalSubmitting}
                  onReason={setGeneralReason}
                  onComment={setGeneralComment}
                  onSubmit={() => void sendGeneralFeedback()}
                  onClose={() => {
                    setFeedbackOpen(false);
                    feedbackTrigger.current?.focus();
                  }}
                />
              )}
            </div>
            <div className="header-menu-wrap">
              <button
                ref={noticeTrigger}
                type="button"
                className="header-icon"
                aria-label={`お知らせ${unreadCount > 0 ? `、未読${unreadCount}件` : ""}`}
                aria-expanded={noticeOpen}
                aria-controls="announcement-popover"
                onClick={handleNoticeToggle}
              >
                <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M18 9a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9ZM10 21h4" /></svg>
                {unreadCount > 0 && <span className="unread-dot" aria-hidden="true" />}
              </button>
              {noticeOpen && (
                <section className="header-popover announcement-popover" id="announcement-popover" aria-label="お知らせ">
                  <h2>お知らせ</h2>
                  {announcements.length === 0 ? (
                    <p className="popover-empty">現在のお知らせはありません</p>
                  ) : (
                    <div className="announcement-list">
                      {announcements.map((announcement) => (
                        <article key={announcement.id}>
                          <time dateTime={announcement.date}>{announcement.date}</time>
                          <h3>{announcement.title}</h3>
                          <p>{announcement.body}</p>
                        </article>
                      ))}
                    </div>
                  )}
                </section>
              )}
            </div>
            <div className="header-menu-wrap">
              <button
                ref={profileTrigger}
                type="button"
                className="profile-avatar"
                aria-label={`利用者メニュー: ${username}`}
                aria-expanded={profileOpen}
                aria-controls="profile-popover"
                onClick={() => {
                  setProfileOpen((open) => !open);
                  setNoticeOpen(false);
                  setFeedbackOpen(false);
                }}
              >
                {profileIcon
                  ? <img src={profileIcon} alt="" />
                  : Array.from(username)[0] ?? "W"}
              </button>
              {profileOpen && (
                <section className="header-popover profile-popover" id="profile-popover" aria-label="利用者メニュー">
                  <div className="profile-summary">
                    <span>ログイン中</span>
                    <strong>{username}</strong>
                  </div>
                  <input
                    ref={profileIconInput}
                    type="file"
                    className="visually-hidden"
                    accept="image/png,image/jpeg,image/webp"
                    onChange={(event) => void handlePickProfileIcon(event)}
                  />
                  <button type="button" disabled={profileIconBusy} onClick={() => profileIconInput.current?.click()}>
                    {profileIconBusy ? "画像を保存中…" : profileIcon ? "利用者画像を変更" : "利用者画像を設定"}
                  </button>
                  {profileIcon && <button type="button" disabled={profileIconBusy} onClick={() => void removeProfileIcon()}>利用者画像を外す</button>}
                  {/* 外部サービス連携の入口。**利用者ごとの設定**なので、
                      管理画面ではなくここに置く（2026-09-14） */}
                  <button type="button" onClick={openSettings}>設定（外部サービス連携）</button>
                  <a href={APP_URLS.wiki} target="_blank" rel="noreferrer noopener">WASA Wikiを開く</a>
                  <a href={APP_URLS.support} target="_blank" rel="noreferrer noopener">ヘルプとポリシー</a>
									{isAdmin && <button type="button" onClick={openAdmin}>管理画面</button>}
                  <button type="button" onClick={handleLogout}>ログアウト</button>
                </section>
              )}
            </div>
          </div>
        </header>

        {view === "settings" ? (
          <SettingsPage
            settings={settings}
            busy={settingsBusy}
            error={settingsError}
            refreshing={serversRefreshing}
            onToggleTool={toggleTool}
            onConnectServer={connectDiscordServer}
            onDisconnectServer={disconnectDiscordServer}
            onRefreshServers={() => void refreshDiscordServers()}
          />
        ) : view === "assistants" ? (
          <main className="assistant-page">
            {assistantForm ? (
              <AssistantSettingsForm
                mode={assistantForm.mode}
                readOnly={assistantFormReadOnly}
                assistant={formAssistant ?? null}
                draft={assistantDraft}
                setDraft={setAssistantDraft}
                teams={teams}
                scopeCounts={scopeCounts}
                error={assistantError}
                onSubmit={(event) => void handleSubmitAssistant(event)}
                onPickIcon={(event) => void handlePickIcon(event)}
                onClose={closeAssistantForm}
                onDuplicate={duplicateAssistant}
                onStartEdit={startEditFromView}
              />
            ) : (
              <>
                <header className="assistant-page-head">
                  <div className="assistant-page-copy">
                    <h2>アシスタント</h2>
                    <p className="muted">
                      口調や参照範囲を決めた設定です。誰でも作れて全員が使えます。
                    </p>
                  </div>
                  <div className="assistant-page-controls">
                    <span className="assistant-count">{assistants.length + 1}件</span>
                    {/* 見出しは付けない。SelectMenu自身が「アシスタントの並べ替え」を
                        名前として持っており、置くと同じ語を二度読み上げる */}
                    <div className="assistant-sort">
                      <SelectMenu
                        label="アシスタントの並べ替え"
                        value={assistantSort}
                        options={ASSISTANT_SORT_OPTIONS}
                        onChange={changeAssistantSort}
                      />
                    </div>
                    <button type="button" className="primary assistant-create" onClick={startCreate}>新しく作る</button>
                  </div>
                </header>
                {assistantError && <p className="assistant-error" role="alert">{assistantError}</p>}
                <ul className="assistant-grid">
                  <li>
                    <button type="button" className={`assistant-card assistant-card-default ${assistantId === "" ? "on" : ""}`} onClick={() => chooseAssistant("")}>
                      <DefaultAvatar size={64} />
                      <span className="assistant-card-copy">
                        <span className="assistant-name">汎用</span>
                        <span className="assistant-desc">資料全体から、ふつうの口調で答えます</span>
                        <span className="assistant-meta">標準アシスタント</span>
                      </span>
                    </button>
                  </li>
                  {sortedAssistants.map((item) => (
                    <li key={item.id}>
                      <button type="button" className={`assistant-card ${assistantId === item.id ? "on" : ""}`} onClick={() => chooseAssistant(item.id)}>
                        <AssistantAvatar name={item.name} icon={item.icon} size={64} />
                        <span className="assistant-card-copy">
                          <span className="assistant-name">{item.name}</span>
                          <span className="assistant-desc">{item.description || "説明はありません"}</span>
                          <span className="assistant-meta">
                            作成: {item.author}
                            {item.scope && <><br />{item.scope}</>}
                          </span>
                        </span>
                      </button>
                      <button
                        type="button"
                        className="assistant-favorite"
                        aria-label={`「${item.name}」をお気に入り${favoriteAssistantIds.includes(item.id) ? "から外す" : "に追加"}`}
                        aria-pressed={favoriteAssistantIds.includes(item.id)}
                        title={favoriteAssistantIds.includes(item.id) ? "お気に入りから外す" : "お気に入りに追加"}
                        onClick={() => toggleFavoriteAssistant(item)}
                      >
                        <span aria-hidden="true">{favoriteAssistantIds.includes(item.id) ? "★" : "☆"}</span>
                      </button>
                      <div className="assistant-card-actions">
                        <button type="button" onClick={() => openAssistantSettings(item)}>設定</button>
                        {/* 他人のものは設定を読めるが、保存先は必ず自分の複製にする。 */}
                        {!item.canEdit && <button type="button" onClick={() => duplicateAssistant(item)}>複製</button>}
                        {item.canEdit && (
                          <button type="button" className="danger" onClick={() => void handleDeleteAssistant(item)}>削除</button>
                        )}
                      </div>
                    </li>
                  ))}
                </ul>
              </>
            )}
          </main>
        ) : (
        <>
        <main className="conversation" ref={conversation} aria-live="polite">
          {!activeChat && (
            <div className="intro">
              <img src={APP_URLS.logo} alt="WASA Chat" className="intro-logo" />
              <h2>引き継ぎ資料を、会話で探す</h2>
              <p className="muted">
                部内Wikiと公式サイトを横断して回答し、参照した資料を示します。
              </p>
              <div className="suggestions">
                {SUGGESTIONS.map((suggestion) => (
                  <button
                    type="button"
                    key={suggestion.title}
                    className="suggestion"
                    onClick={() => handleAsk(suggestion.body)}
                  >
                    <span className="suggestion-title">{suggestion.title}</span>
                    <span className="suggestion-body">{suggestion.body}</span>
                    <span className="suggestion-arrow" aria-hidden="true">→</span>
                  </button>
                ))}
              </div>
            </div>
          )}

          {activeChat?.turns.map((turn, index) => (
            <article key={index} className="turn">
              <div className="user-row">
                <div className="question">
                  {/* 画像そのものは保存していないので、添えたという事実だけを出す。
                      これが無いと、履歴を見返したときに質問だけが宙に浮く */}
                  {turn.hasAttachment && <span className="question-attachment">画像を添付</span>}
                  {turn.question || (turn.hasAttachment ? "（画像のみ）" : "")}
                </div>
              </div>

              <div className="assistant-row">
                {/* 誰が答えたかを毎回出す。しゅよっくんに切り替えたのに
                    WASAロゴのままだと、口調が変わった理由が画面から分からない */}
                {turnAssistantAvatar(turn)}
                <div className="assistant-content">
                  <div className="answer-author">
                    <span>{turn.assistantName ?? "WASA Chat"}</span>
                  </div>
                  {turn.status && (
                    <div className="status">
                      <Spinner />
                      <span>{turn.status}</span>
                      {turn.retryAt && <small>{retryLabel(turn.retryAt, clock)}</small>}
                    </div>
                  )}
                  {turn.answer && (
                    <div>
                      <Suspense fallback={<div className="muted" role="status">回答を表示中…</div>}>
                        {/* 本文中の [1] を出典チップへ変えるために出典を渡す。
                            表示する題名とURLはサーバーが索引から組み立てたもので、
                            モデルが書けるのは番号だけ */}
                        <Markdown text={stripCitation(turn.answer)} sources={turn.sources} />
                      </Suspense>
                      {turn.streaming && <span className="caret" />}
                    </div>
                  )}

                  {/* 回答中だけ大きなカードを出すと完了時に画面が跳ねるため、
                      前後とも実際に読んだ節を同じ1行で示す。 */}
                  {turn.sources.length > 0 && (turn.streaming || turn.answer) && (
                    <ReferenceSummary sources={turn.sources} active={turn.streaming} />
                  )}
                  {turn.error && (turn.errorCode ? (
                    <div className="service-alert" role="alert">
                      <strong>
                        {turn.errorCode === "daily_quota" ? "本日の無料枠を使い切りました" :
                          turn.errorCode === "rate_limit" ? "一時的な利用制限がかかっています" :
                            turn.errorCode === "user_daily_limit" ? "本日の質問上限に達しました" :
                            "Geminiに接続できません"}
                      </strong>
                      <span>{turn.error}</span>
                      {turn.retryAt && <small>{retryLabel(turn.retryAt, clock)}</small>}
                    </div>
                  ) : <div className="error" role="alert">{turn.error}</div>)}

                  {!turn.streaming && turn.answer && !turn.error && activeChat && (
                    <AnswerFeedback
                      turn={turn}
                      open={answerFeedbackOpen === `${activeChat.id}:${index}`}
                      comment={answerComments[`${activeChat.id}:${index}`] ?? turn.feedbackComment ?? ""}
                      regenerating={streaming}
                      onCopy={() => void handleCopyAnswer(turn)}
                      onRegenerate={() => handleRegenerate(activeChat, index, turn)}
                      onRating={(rating) => void handleAnswerRating(activeChat, index, turn, rating)}
                      onReason={(reason) => void toggleAnswerReason(activeChat, index, turn, reason)}
                      onComment={(comment) => setAnswerComments((current) => ({ ...current, [`${activeChat.id}:${index}`]: comment }))}
                      onSubmitComment={() => void submitAnswerComment(activeChat, index, turn)}
                      onClose={() => setAnswerFeedbackOpen(null)}
                    />
                  )}

                </div>
              </div>
            </article>
          ))}
          <div ref={bottom} />
        </main>

        <div className="composer-area">
          {/* 長い回答を上の方で読んでいると、末尾へ戻る手段がスクロールしかない。
              離れているときだけ出し、押したら追従も再開する */}
          {awayFromBottom && (
            <button
              type="button"
              className="scroll-bottom"
              aria-label="最新の回答へ移動"
              title="最新の回答へ移動"
              onClick={() => {
                stickToBottom.current = true;
                bottom.current?.scrollIntoView({ behavior: "smooth", block: "end" });
              }}
            >
              <svg viewBox="0 0 24 24" aria-hidden="true">
                <path d="M12 5v13m0 0 6-6m-6 6-6-6" />
              </svg>
            </button>
          )}
          {attachment && (
            <div className="attachment-row">
            <div className="attachment-chip">
              <img src={attachment.dataUrl} alt="" />
              <span className="attachment-name">{attachment.name}</span>
              <button
                type="button"
                onClick={() => setAttachment(null)}
                aria-label={`添付した画像（${attachment.name}）を外す`}
              >
                ×
              </button>
            </div>
            </div>
          )}
          <form
            className="composer"
            onSubmit={(event) => {
              event.preventDefault();
              handleAsk(question);
            }}
          >
            {/* ⚠️ **ここでは切り替えない。** 連携は設定画面で決めるものへ変えた
                （2026-09-14）。ただし、いま何をつないでいるかは**開かなくても
                分かる**ようにする。黙って外部を読むのと、黙って読まないのは
                どちらも困る。押すと設定画面へ行く */}
            <button
              type="button"
              className={`tool-trigger${connectedTools.length > 0 ? " is-active" : ""}`}
              aria-label={connectedTools.length > 0
                ? `連携中: ${connectedTools.map((tool) => tool.name).join("・")}（設定を開く）`
                : "外部サービスと連携（設定を開く）"}
              title={connectedTools.length > 0
                ? `連携中: ${connectedTools.map((tool) => tool.name).join("・")}`
                : "外部サービスと連携"}
              onClick={openSettings}
            >
              {connectedTools.length === 0 ? (
                <svg viewBox="0 0 24 24" aria-hidden="true">
                  <path d="M12 5v14M5 12h14" />
                </svg>
              ) : (
                <span className="tool-trigger-icons" aria-hidden="true">
                  {connectedTools.map((tool) => <ToolIcon key={tool.id} id={tool.id} />)}
                </span>
              )}
            </button>
            <textarea
              ref={questionInput}
              value={question}
              onChange={(event) => {
                setQuestion(event.target.value);
                resizeComposerTextarea(event.currentTarget);
              }}
              onPaste={(event) => {
                const file = imageFromDataTransfer(event.clipboardData);
                if (!file) return; // 文字の貼り付けはそのまま通す
                event.preventDefault();
                void attachImage(file);
              }}
              aria-label="質問"
              placeholder={narrow ? "質問する" : "引き継ぎ資料について質問する"}
              maxLength={APP_LIMITS.questionRunes}
              rows={1}
              disabled={streaming}
            />
            <div className="composer-actions">
              <input
                ref={attachmentInput}
                type="file"
                className="visually-hidden"
                accept={ACCEPTED_IMAGE_TYPES.join(",")}
                onChange={(event) => {
                  const file = event.target.files?.[0];
                  // 同じ画像をもう一度選べるよう、値は必ず空へ戻す
                  event.target.value = "";
                  void attachImage(file);
                }}
              />
              <button
                type="button"
                className="attach"
                aria-label="画像を添付"
                title="画像を添付"
                disabled={streaming}
                onClick={() => attachmentInput.current?.click()}
              >
                <svg viewBox="0 0 24 24" aria-hidden="true">
                  <path d="M14 6.5 8 12.5a3 3 0 0 0 4.2 4.2l6.3-6.3a5 5 0 0 0-7-7L5 9.7a7 7 0 0 0 9.9 9.9l5.1-5.1" />
                </svg>
              </button>
              {/* 回答中は送信を停止に差し替える。並べて置くと、押し間違いが
                  そのまま1回分の消費になる */}
              {streaming ? (
                <button type="button" className="send stop" aria-label="回答を停止" title="回答を停止" onClick={handleStop}>
                  <svg viewBox="0 0 24 24" aria-hidden="true">
                    <rect x="7" y="7" width="10" height="10" rx="2" />
                  </svg>
                </button>
              ) : (
                <button
                  type="submit"
                  className="send"
                  aria-label="送信"
                  title="送信"
                  disabled={!question.trim() && !attachment}
                >
                  <svg viewBox="0 0 24 24" aria-hidden="true">
                    <path d="m4 12 16-8-5.5 16-3-6.5L4 12Zm7.5 1.5L20 4" />
                  </svg>
                </button>
              )}
            </div>
          </form>
          <p className="disclaimer">生成AIの回答には誤りが含まれることがあります。</p>
        </div>
        </>
        )}
      </section>
      {renamingChatId && (
        <div className="dialog-backdrop" role="presentation" onMouseDown={(event) => {
          if (event.target === event.currentTarget) setRenamingChatId(null);
        }}>
          <form className="rename-dialog" role="dialog" aria-modal="true" aria-labelledby="rename-title" onSubmit={handleRenameSubmit}>
            <h2 id="rename-title">タイトルを変更</h2>
            <input
              value={renameTitle}
              onChange={(event) => setRenameTitle(event.target.value)}
              maxLength={APP_LIMITS.chatTitleRunes}
              autoFocus
              aria-label="新しいタイトル"
            />
            <div>
              <button type="button" onClick={() => setRenamingChatId(null)}>キャンセル</button>
              <button type="submit" className="primary" disabled={!renameTitle.trim()}>保存</button>
            </div>
          </form>
        </div>
      )}
      <Toast message={toast} onClose={hideToast} />
    </div>
  );
}
