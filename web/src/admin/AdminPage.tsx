import { useEffect, useMemo, useRef, useState } from "react";
import {
  adminOverview,
  checkSources,
  setCoAdmin,
  setToolGrant,
  type AdminOverview,
  type AdminUserUsage,
  type SourceCheckResult,
} from "./api";
import { LoadingScreen } from "../components/LoadingScreen";
import { SelectMenu, type SelectOption } from "../components/SelectMenu";
import { Toast } from "../components/Toast";
import { APP_URLS } from "../config";
import { useToast } from "../hooks/useToast";

type Props = {
  username: string;
  profileIcon: string;
  onBack: () => void;
  onLogout: () => void;
};

type SortKey = "username" | "today" | "sevenDays" | "thirtyDays" | "lastUsed";
type AdminTab = "overview" | "sources" | "users" | "quota" | "logs";

const MILLISECONDS_PER_SECOND = 1_000;
const MILLISECONDS_PER_DAY = 24 * 60 * 60 * MILLISECONDS_PER_SECOND;
const API_CALLS_PER_QUESTION = 3;
const DEFAULT_USAGE_PERIOD = "7";
const DEFAULT_AUDIT_PERIOD = "30";
const USER_SORT_OPTIONS: SelectOption[] = [
  { value: "thirtyDays:desc", label: "30日利用が多い順" },
  { value: "sevenDays:desc", label: "7日利用が多い順" },
  { value: "today:desc", label: "今日の利用が多い順" },
  { value: "lastUsed:desc", label: "最近利用した順" },
  { value: "username:asc", label: "名前順" },
];
const PERIOD_OPTIONS: SelectOption[] = [
  { value: "1", label: "24時間" },
  { value: "7", label: "7日" },
  { value: "30", label: "30日" },
  { value: "all", label: "すべて" },
];
const tabs: { id: AdminTab; label: string; description: string }[] = [
  { id: "overview", label: "概要", description: "今日の利用状況とシステムの状態を確認します。" },
  { id: "sources", label: "資料更新", description: "公開元の変更を確認し、変わっていれば手元で取り込み直します。" },
  { id: "users", label: "利用者・権限", description: "利用回数の確認と、共同管理者や共有ドライブの許可を行います。" },
  { id: "quota", label: "API利用状況", description: "Gemini無料枠の利用量とリセット時刻を確認します。" },
  { id: "logs", label: "監査ログ", description: "質問本文を含まない利用記録と管理者操作を確認します。" },
];

const number = new Intl.NumberFormat("ja-JP");
const dateTime = new Intl.DateTimeFormat("ja-JP", {
  month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit",
});

function formatDateTime(value?: string): string {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : dateTime.format(date);
}

function duration(milliseconds: number): string {
  if (milliseconds < MILLISECONDS_PER_SECOND) return `${milliseconds}ms`;
  return `${(milliseconds / MILLISECONDS_PER_SECOND).toFixed(1)}秒`;
}

const outcomeLabels: Record<string, string> = {
  success: "成功",
  daily_quota: "日次上限",
  rate_limit: "短時間制限",
  unavailable: "API障害",
  images_unsupported: "画像非対応",
  cancelled: "中止",
  failed: "失敗",
  user_daily_limit: "個人上限",
};

const auditLabels: Record<string, string> = {
  "admin.login": "管理者としてログイン",
  "admin.overview.view": "管理画面を閲覧",
  "admin.role.grant": "共同管理者に追加",
  "admin.role.revoke": "共同管理者を解除",
  "admin.tool.grant": "参照先を許可",
  "admin.tool.revoke": "参照先の許可を解除",
  "assistant.delete": "管理者権限でアシスタントを削除",
  "source.check": "資料の更新を確認",
};

function quotaStateLabel(state: AdminOverview["quota"]["state"]): string {
  if (state === "daily_quota") return "本日分を使い切りました";
  if (state === "rate_limited") return "短時間の利用制限中";
  return "利用可能";
}

/** 出所の表示名。Go側 pipeline.OriginLabel と同じにすること。
 *  Wiki以外をまとめて「公式サイト」と出していたため、
 *  フライトシミュレータの差分も公式サイトとして表示されていた（2026-09-12に発見）。 */
function sourceLabel(source: string): string {
  if (source === "site") return "公式サイト";
  if (source === "fee") return "フライトシミュレータ";
  return "Wiki";
}

function changeCount(result: SourceCheckResult): number {
  return result.deltas.reduce(
    (total, delta) => total + delta.added.length + delta.updated.length + delta.removed.length,
    0,
  );
}

function sortValue(user: AdminUserUsage, key: SortKey): string | number {
  if (key === "lastUsed") return user.lastUsed ? new Date(user.lastUsed).getTime() : 0;
  return user[key];
}

function inPeriod(value: string, period: string): boolean {
  if (period === "all") return true;
  const occurred = new Date(value).getTime();
  if (Number.isNaN(occurred)) return false;
  const days = Number(period);
  return occurred >= Date.now() - days * MILLISECONDS_PER_DAY;
}

function sameVersion(frontend: string, backend: string): boolean {
  if (!frontend || !backend || frontend === "local" || backend === "local") return true;
  return frontend.startsWith(backend) || backend.startsWith(frontend);
}

function progressLabel(stage: AdminOverview["updateProgress"]["stage"]): string {
  switch (stage) {
    case "unavailable": return "更新確認を利用できません";
    case "not_checked": return "更新確認待ち";
    case "changes_detected": return "再構築待ち";
    case "verify_needed": return "反映後の再確認待ち";
    default: return "最新です";
  }
}

function nextUpdateAction(stage: AdminOverview["updateProgress"]["stage"]): string {
  switch (stage) {
    case "unavailable": return "更新確認用のWikiアカウント設定が必要です。";
    case "not_checked": return "「更新を確認」を押してください。";
    case "changes_detected": return "手元で再構築し、差分を確認してから差し替えてください。";
    case "verify_needed": return "もう一度更新を確認し、変更なしになることを確かめてください。";
    default: return "現在必要な作業はありません。";
  }
}

export function AdminPage({ username, profileIcon, onBack, onLogout }: Props) {
  const [data, setData] = useState<AdminOverview | null>(null);
  const [loading, setLoading] = useState(true);
  const [roleBusy, setRoleBusy] = useState("");
  const [driveBusy, setDriveBusy] = useState("");
  const [checkingSources, setCheckingSources] = useState(false);
  const [activeTab, setActiveTab] = useState<AdminTab>("overview");
  const [search, setSearch] = useState("");
  const [sortKey, setSortKey] = useState<SortKey>("thirtyDays");
  const [sortDirection, setSortDirection] = useState<"asc" | "desc">("desc");
  const [usageLogSearch, setUsageLogSearch] = useState("");
  const [usageLogPeriod, setUsageLogPeriod] = useState(DEFAULT_USAGE_PERIOD);
  const [usageLogOutcome, setUsageLogOutcome] = useState("all");
  const [auditLogSearch, setAuditLogSearch] = useState("");
  const [auditLogPeriod, setAuditLogPeriod] = useState(DEFAULT_AUDIT_PERIOD);
  const [auditLogAction, setAuditLogAction] = useState("all");
  const [profileOpen, setProfileOpen] = useState(false);
  const { toast, showToast, hideToast } = useToast();
  const headerMenus = useRef<HTMLDivElement>(null);
  const profileTrigger = useRef<HTMLButtonElement>(null);

  async function refresh() {
    setLoading(true);
    try {
      setData(await adminOverview());
    } catch (reason) {
      showToast(reason instanceof Error ? reason.message : "管理情報を読み込めませんでした");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refresh();
  }, []);

  useEffect(() => {
    if (!profileOpen) return;
    function close(event: MouseEvent) {
      if (!headerMenus.current?.contains(event.target as Node)) setProfileOpen(false);
    }
    function closeWithEscape(event: KeyboardEvent) {
      if (event.key !== "Escape") return;
      setProfileOpen(false);
      profileTrigger.current?.focus();
    }
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", closeWithEscape);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", closeWithEscape);
    };
  }, [profileOpen]);

  const users = useMemo(() => {
    const query = search.trim().toLocaleLowerCase("ja");
    const filtered = data?.users.filter((user) =>
      !query || user.username.toLocaleLowerCase("ja").includes(query)) ?? [];
    return [...filtered].sort((left, right) => {
      const a = sortValue(left, sortKey);
      const b = sortValue(right, sortKey);
      const order = typeof a === "string" && typeof b === "string"
        ? a.localeCompare(b, "ja")
        : Number(a) - Number(b);
      return sortDirection === "asc" ? order : -order;
    });
  }, [data?.users, search, sortDirection, sortKey]);

  const usageEvents = useMemo(() => {
    const query = usageLogSearch.trim().toLocaleLowerCase("ja");
    return data?.usageEvents.filter((event) => {
      const matchesText = !query || [event.username, event.assistantId, event.responseMode, event.resolvedMode]
        .some((value) => value?.toLocaleLowerCase("ja").includes(query));
      return matchesText && inPeriod(event.occurredAt, usageLogPeriod) &&
        (usageLogOutcome === "all" || event.outcome === usageLogOutcome);
    }) ?? [];
  }, [data?.usageEvents, usageLogOutcome, usageLogPeriod, usageLogSearch]);

  const adminAudits = useMemo(() => {
    const query = auditLogSearch.trim().toLocaleLowerCase("ja");
    return data?.adminAudits.filter((audit) => {
      const matchesText = !query || [audit.actor, audit.target, auditLabels[audit.action], audit.action]
        .some((value) => value?.toLocaleLowerCase("ja").includes(query));
      return matchesText && inPeriod(audit.occurredAt, auditLogPeriod) &&
        (auditLogAction === "all" || audit.action === auditLogAction);
    }) ?? [];
  }, [auditLogAction, auditLogPeriod, auditLogSearch, data?.adminAudits]);

  const usageOutcomes = useMemo(() => [...new Set(data?.usageEvents.map((event) => event.outcome) ?? [])], [data?.usageEvents]);
  const auditActions = useMemo(() => [...new Set(data?.adminAudits.map((audit) => audit.action) ?? [])], [data?.adminAudits]);

  function changeSort(next: SortKey) {
    if (next === sortKey) {
      setSortDirection((current) => current === "asc" ? "desc" : "asc");
    } else {
      setSortKey(next);
      setSortDirection(next === "username" ? "asc" : "desc");
    }
  }

  function sortMark(key: SortKey): string {
    if (key !== sortKey) return "";
    return sortDirection === "asc" ? " ↑" : " ↓";
  }

  function ariaSort(key: SortKey): "ascending" | "descending" | "none" {
    if (key !== sortKey) return "none";
    return sortDirection === "asc" ? "ascending" : "descending";
  }

  function changeSortSelect(value: string) {
    const [key, direction] = value.split(":") as [SortKey, "asc" | "desc"];
    setSortKey(key);
    setSortDirection(direction);
  }

  async function changeRole(target: AdminUserUsage, enabled: boolean) {
    const action = enabled ? "共同管理者にします" : "共同管理者権限を解除します";
    if (!window.confirm(`${target.username}さんを${action}。よろしいですか？`)) return;
    setRoleBusy(target.username);
    try {
      await setCoAdmin(target.username, enabled);
      await refresh();
      showToast(enabled ? `${target.username}さんを共同管理者にしました` : `${target.username}さんの共同管理者権限を解除しました`);
    } catch (reason) {
      showToast(reason instanceof Error ? reason.message : "管理者権限を変更できませんでした");
    } finally {
      setRoleBusy("");
    }
  }

  /**
   * 共有ドライブの許可を切り替える。
   *
   * ⚠️ **確認を挟む。** 許可した人は、Wikiに書かない部員が置いた資料まで
   * 読めるようになる。押し間違いで広がると気づけない
   */
  async function changeDrive(target: AdminUserUsage, enabled: boolean) {
    const action = enabled
      ? "共有ドライブの資料を参照できるようにします"
      : "共有ドライブの許可を解除します";
    if (!window.confirm(`${target.username}さんに${action}。よろしいですか？`)) return;
    setDriveBusy(target.username);
    try {
      await setToolGrant(target.username, "drive", enabled);
      await refresh();
      showToast(enabled
        ? `${target.username}さんに共有ドライブを許可しました`
        : `${target.username}さんの共有ドライブの許可を解除しました`);
    } catch (reason) {
      showToast(reason instanceof Error ? reason.message : "参照先の許可を変更できませんでした");
    } finally {
      setDriveBusy("");
    }
  }

  async function runSourceCheck() {
    setCheckingSources(true);
    try {
      const result = await checkSources();
      await refresh();
      showToast(result.changed ? `${changeCount(result)}件の資料変更を検出しました` : "Wikiと公式サイトに変更はありませんでした");
    } catch (reason) {
      showToast(reason instanceof Error ? reason.message : "更新を確認できませんでした");
    } finally {
      setCheckingSources(false);
    }
  }

  async function copyPublishSteps() {
    try {
      await navigator.clipboard.writeText("python rebuild.py\nsh tools/publish-index.sh");
      showToast("再構築と差し替えのコマンドをコピーしました");
    } catch {
      showToast("コピーできませんでした。コマンドを選択してコピーしてください");
    }
  }

  const isOwner = data?.currentAdmin.role === "owner";
  const lastCheck = data?.sourceCheck.last;
  const currentTab = tabs.find((tab) => tab.id === activeTab) ?? tabs[0];
  const versionMismatch = Boolean(data && !sameVersion(__WASA_BUILD_VERSION__, data.system.codeVersion));
  const outcomeOptions = useMemo<SelectOption[]>(() => [
    { value: "all", label: "すべて" },
    ...usageOutcomes.map((outcome) => ({ value: outcome, label: outcomeLabels[outcome] ?? outcome })),
  ], [usageOutcomes]);
  const actionOptions = useMemo<SelectOption[]>(() => [
    { value: "all", label: "すべて" },
    ...auditActions.map((action) => ({ value: action, label: auditLabels[action] ?? action })),
  ], [auditActions]);

  if (!data && loading) {
    return <LoadingScreen label="管理者情報を確認しています" admin />;
  }

  return (
    <div className="admin-shell">
      <header className="admin-header">
        <div className="admin-brand">
          <button type="button" className="sidebar-toggle admin-back" onClick={onBack} aria-label="チャットへ戻る" title="チャットへ戻る">
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m15 6-6 6 6 6" /></svg>
          </button>
          <img src={APP_URLS.logo} alt="WASA Chat" className="admin-logo" />
          <span className="admin-mode-label">管理画面</span>
        </div>
        <div className="header-actions" ref={headerMenus}>
          <button type="button" className="header-icon admin-refresh" onClick={() => void refresh()} disabled={loading} aria-label="管理情報を再読み込み" title="再読み込み">
            {loading ? <span className="admin-spinner" aria-hidden="true" /> : (
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 12a8 8 0 0 0-14.9-4M4 4v4h4M4 12a8 8 0 0 0 14.9 4M20 20v-4h-4" /></svg>
            )}
          </button>
          <div className="header-menu-wrap">
            <button
              ref={profileTrigger}
              type="button"
              className="profile-avatar"
              aria-label={`利用者メニュー: ${username}`}
              aria-expanded={profileOpen}
              aria-controls="admin-profile-popover"
              onClick={() => setProfileOpen((open) => !open)}
            >
              {profileIcon
                ? <img src={profileIcon} alt="" />
                : Array.from(username)[0] ?? "W"}
            </button>
            {profileOpen && (
              <section className="header-popover profile-popover" id="admin-profile-popover" aria-label="利用者メニュー">
                <div className="profile-summary"><span>管理者としてログイン中</span><strong>{username}</strong></div>
                <button type="button" onClick={onBack}>チャットへ戻る</button>
                <a href={APP_URLS.wiki} target="_blank" rel="noreferrer noopener">WASA Wikiを開く</a>
                <a href={APP_URLS.support} target="_blank" rel="noreferrer noopener">ヘルプとポリシー</a>
                <button type="button" onClick={onLogout}>ログアウト</button>
              </section>
            )}
          </div>
        </div>
      </header>

      {data && (
        <main className="admin-main">
          <div className="admin-tabs" role="tablist" aria-label="管理メニュー">
            {tabs.map((tab) => (
              <button key={tab.id} type="button" role="tab" aria-selected={activeTab === tab.id} onClick={() => setActiveTab(tab.id)}>
                {tab.label}
              </button>
            ))}
          </div>

          <header className="admin-page-title">
            <h2>{currentTab.label}</h2>
            <p>{currentTab.description}</p>
          </header>

          <div key={activeTab} className="admin-tab-panel">
          {activeTab === "overview" && (
            <>
              <section aria-labelledby="admin-summary-title">
                <div className="admin-section-head">
                  <div><h3 id="admin-summary-title">今日の状態</h3><p>最終取得: {formatDateTime(data.generatedAt)}</p></div>
                </div>
                <div className="admin-stat-grid">
                  <article className="admin-stat"><span>質問</span><strong>{number.format(data.summary.todayQuestions)}</strong><small>日本時間の今日</small></article>
                  <article className="admin-stat"><span>利用者</span><strong>{number.format(data.summary.activeUsersToday)}</strong><small>登録 {number.format(data.summary.knownUsers)}人</small></article>
                  <article className={`admin-stat quota-${data.quota.state}`}><span>Gemini</span><strong>{quotaStateLabel(data.quota.state)}</strong><small>{data.quota.retryAt ? `${formatDateTime(data.quota.retryAt)}まで` : `リセット ${formatDateTime(data.quota.resetAt)}`}</small></article>
                </div>
              </section>
              <section aria-labelledby="admin-version-title">
                <div className="admin-section-head"><div><h3 id="admin-version-title">本番バージョン</h3><p>画面・API・索引が意図したバージョンへ切り替わったか確認します。</p></div><span className="admin-pill">{versionMismatch ? "不一致" : "一致"}</span></div>
                <div className="admin-version-grid">
                  <article><span>画面</span><strong>{__WASA_BUILD_VERSION__}</strong><small>Cloudflare Pages</small></article>
                  <article><span>API</span><strong>{data.system.codeVersion || "不明"}</strong><small>{data.system.revision || "リビジョン不明"}</small></article>
                  <article><span>索引</span><strong>{data.system.indexVersion || "不明"}</strong><small>{data.system.indexPublishedAt ? `${formatDateTime(data.system.indexPublishedAt)}公開` : "公開日時不明"}</small></article>
                </div>
              </section>
              <section className="admin-system" aria-labelledby="admin-system-title">
                <div className="admin-section-head"><div><h3 id="admin-system-title">障害調査用の実行情報</h3><p>問い合わせ時に確認する情報です。</p></div></div>
                <dl><div><dt>LLM</dt><dd>{data.system.llm || "未設定"}</dd></div><div><dt>保存先</dt><dd>{data.system.store || "不明"}</dd></div><div><dt>索引読込元</dt><dd>{data.system.indexSource || "不明"}</dd></div><div><dt>API起動</dt><dd>{formatDateTime(data.system.startedAt)}</dd></div></dl>
              </section>
            </>
          )}

          {activeTab === "sources" && (
            <>
              <section aria-labelledby="admin-source-title">
                <div className="admin-section-head">
                  <div><h3 id="admin-source-title">資料の状態</h3><p>公開元と現在の索引を比較します。</p></div>
                  <button type="button" className="admin-primary" onClick={() => void runSourceCheck()} disabled={checkingSources || !data.sourceCheck.available}>
                    {checkingSources ? "確認中…" : "更新を確認"}
                  </button>
                </div>
                <div className="admin-source-card">
                  <div className="admin-source-result">
                    <div><strong>{progressLabel(data.updateProgress.stage)}</strong><small>{nextUpdateAction(data.updateProgress.stage)}</small></div>
                    <span>{lastCheck ? `${formatDateTime(lastCheck.checkedAt)}・${lastCheck.checkedBy}` : "未確認"}</span>
                  </div>
                  <div className="admin-update-flow" aria-label="資料更新の流れ">
                    {/* 「反映後を再確認」は無くした。索引を差し替えると本番が自分で読み直すので、
                        反映を確かめに戻る必要がなくなった（2026-09-12） */}
                    {['公開元を確認', '手元で再構築', '差し替え'].map((label, index) => <span key={label}><i>{index + 1}</i>{label}</span>)}
                  </div>
                  {lastCheck?.changed && (
                    <details className="admin-source-details">
                      <summary>{changeCount(lastCheck)}件の変更内訳</summary>
                      <div className="admin-source-deltas">
                        {lastCheck.deltas.map((delta) => {
                          const items = [
                            ...delta.added.map((name) => `追加: ${name}`),
                            ...delta.updated.map((name) => `更新: ${name}`),
                            ...delta.removed.map((name) => `削除: ${name}`),
                          ];
                          return (
                            <div key={delta.source}>
                              <strong>{sourceLabel(delta.source)}　追加 {delta.added.length}・更新 {delta.updated.length}・削除 {delta.removed.length}</strong>
                              {items.length > 0 ? <ul>{items.map((item) => <li key={item}>{item}</li>)}</ul> : <p>変更なし</p>}
                            </div>
                          );
                        })}
                      </div>
                    </details>
                  )}
                  {/* 取得と再構築は手元で行う。**ここは自動化していない。**
                      自動になったのは「差し替えたあとの反映」だけ */}
                  <details className="admin-publish-guide">
                    <summary>変更があったときの手順</summary>
                    <ol>
                      <li><span>1</span><div><strong>再構築</strong><code>python rebuild.py</code></div></li>
                      <li><span>2</span><div><strong>差分を確認</strong><small>意図しない削除や誤編集がないことを確認します。</small></div></li>
                      <li><span>3</span><div><strong>差し替え</strong><code>sh tools/publish-index.sh</code><small>本番は1分以内に自分で読み直します。再デプロイは要りません。</small></div></li>
                    </ol>
                    <button type="button" className="admin-secondary" onClick={() => void copyPublishSteps()}>コマンドをコピー</button>
                  </details>
                </div>
              </section>
            </>
          )}

          {activeTab === "users" && (
            <>
              <section aria-labelledby="admin-roles-title">
                <div className="admin-section-head">
                  <div><h3 id="admin-roles-title">管理者</h3><p>主管理者は設定に残る復旧担当です。共同管理者の追加・解除は利用者一覧から行います。</p></div>
                  <span className="admin-pill">{data.admins.length}人</span>
                </div>
                <div className="admin-table-wrap">
                  <table className="admin-table">
                    <thead><tr><th>Wiki利用者名</th><th>権限</th><th>付与者</th><th>付与日時</th></tr></thead>
                    <tbody>{data.admins.map((admin) => (
                      <tr key={admin.username}><th data-label="Wiki利用者名">{admin.username}</th><td data-label="権限"><span className="admin-role-badge">{admin.role === "owner" ? "主管理者" : "共同管理者"}</span></td><td data-label="付与者">{admin.grantedBy || "—"}</td><td data-label="付与日時">{formatDateTime(admin.grantedAt)}</td></tr>
                    ))}</tbody>
                  </table>
                </div>
              </section>

              <section aria-labelledby="admin-users-title">
                <div className="admin-section-head admin-user-head">
                  <div><h3 id="admin-users-title">利用者一覧</h3><p>WASA Chatへログインした全利用者です。一覧にいない人は、一度ログインすると追加されます。</p></div>
                  <div className="admin-user-tools">
                    <label><span className="sr-only">利用者名を検索</span><input type="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="名前で検索" /></label>
                    <div className="admin-select-control"><SelectMenu label="利用者の並び順" value={`${sortKey}:${sortDirection}`} options={USER_SORT_OPTIONS} onChange={changeSortSelect} /></div>
                    <span className="admin-pill">{users.length} / {data.users.length}人</span>
                  </div>
                </div>
                <div className="admin-table-wrap">
                  <table className="admin-table">
                    <thead><tr>
                      <th aria-sort={ariaSort("username")}><button type="button" onClick={() => changeSort("username")}>Wiki利用者名{sortMark("username")}</button></th>
                      <th aria-sort={ariaSort("today")}><button type="button" onClick={() => changeSort("today")}>今日{sortMark("today")}</button></th>
                      <th aria-sort={ariaSort("sevenDays")}><button type="button" onClick={() => changeSort("sevenDays")}>7日{sortMark("sevenDays")}</button></th>
                      <th aria-sort={ariaSort("thirtyDays")}><button type="button" onClick={() => changeSort("thirtyDays")}>30日{sortMark("thirtyDays")}</button></th>
                      <th aria-sort={ariaSort("lastUsed")}><button type="button" onClick={() => changeSort("lastUsed")}>最終利用{sortMark("lastUsed")}</button></th>
                      <th>管理権限</th>
                      <th>共有ドライブ</th>
                    </tr></thead>
                    <tbody>{users.length === 0 ? (
                      <tr className="admin-empty-row"><td colSpan={7} className="admin-empty">該当する利用者はいません</td></tr>
                    ) : users.map((user) => (
                      <tr key={user.username}>
                        <th data-label="Wiki利用者名">{user.username}</th><td data-label="今日">{user.today}{user.limitReached && <span className="admin-warning">上限</span>}</td><td data-label="7日">{user.sevenDays}</td><td data-label="30日">{user.thirtyDays}</td><td data-label="最終利用">{formatDateTime(user.lastUsed)}</td>
                        <td data-label="管理権限">{user.role === "owner" ? <span className="admin-role-badge">主管理者</span> : user.role === "co_admin" ? (
                          <div className="admin-role-action"><span className="admin-role-badge">共同管理者</span>{isOwner && <button type="button" disabled={roleBusy === user.username} onClick={() => void changeRole(user, false)}>解除</button>}</div>
                        ) : isOwner ? <button type="button" className="admin-link-button" disabled={roleBusy === user.username} onClick={() => void changeRole(user, true)}>共同管理者にする</button> : "—"}</td>
                        {/* 管理者は共有フォルダの中身を決める側なので、指定しなくても読める */}
                        <td data-label="共有ドライブ">{user.role ? <span className="admin-role-badge">管理者は利用可</span>
                          : user.tools?.includes("drive") ? (
                            <div className="admin-role-action"><span className="admin-role-badge">許可済み</span><button type="button" disabled={driveBusy === user.username} onClick={() => void changeDrive(user, false)}>解除</button></div>
                          ) : <button type="button" className="admin-link-button" disabled={driveBusy === user.username} onClick={() => void changeDrive(user, true)}>許可する</button>}</td>
                      </tr>
                    ))}</tbody>
                  </table>
                </div>
                {!isOwner && <p className="admin-footnote">共同管理者の追加・解除は主管理者だけが行えます。</p>}
                <p className="admin-footnote">
                  共有ドライブはWikiに書かない部員の資料が入る場所です。許可した人だけが入力欄の「+」から参照できます。
                  <strong>共有フォルダに「一部の部員だけが見てよい資料」を置かないでください。</strong>
                </p>
              </section>
            </>
          )}

          {activeTab === "quota" && (
            <section aria-labelledby="admin-quota-title">
              <div className="admin-section-head"><div><h3 id="admin-quota-title">Gemini無料枠</h3><p>WASA ChatからGeminiへ送った、本日分のリクエストを確認します。</p></div><span className="admin-pill">太平洋時間 {data.quota.day}</span></div>
              <div className="admin-quota-summary">
                <article><span>本日のAPI送信</span><strong>{number.format(data.quota.totalRequests)}</strong><small>再試行を含む実測値</small></article>
                <article><span>質問数の目安</span><strong>約{number.format(Math.floor(data.quota.totalRequests / API_CALLS_PER_QUESTION))}</strong><small>質問1回につき通常約{API_CALLS_PER_QUESTION}回送信</small></article>
                <article><span>リセット</span><strong>{formatDateTime(data.quota.resetAt)}</strong><small>Gemini無料枠の基準</small></article>
              </div>
              <div className="admin-quota-list">
                {data.quota.models.length === 0 && <p className="admin-empty">現在はGeminiを使用していません</p>}
                {data.quota.models.map((model) => {
                  const ratio = model.limit > 0 ? Math.min(model.requests / model.limit, 1) : 0;
                  return (
                    <article key={model.model} className="admin-quota-card">
                      <div><strong>{model.model}</strong><span>推定残り {number.format(model.remaining)}回</span></div>
                      <div className="admin-meter" role="meter" aria-label={`${model.model}の利用率`} aria-valuemin={0} aria-valuemax={model.limit} aria-valuenow={model.requests}><span style={{ width: `${ratio * 100}%` }} /></div>
                      <small>{number.format(model.requests)} / {number.format(model.limit)}リクエスト・通常約{number.format(Math.floor(model.remaining / API_CALLS_PER_QUESTION))}質問分</small>
                    </article>
                  );
                })}
              </div>
              <p className="admin-footnote">この集計機能を本番へ反映した後の送信だけを記録します。反映前の質問は遡って加算されません。</p>
            </section>
          )}

          {activeTab === "logs" && (
            <>
              <section aria-labelledby="admin-events-title">
                <div className="admin-section-head admin-log-head"><div><h3 id="admin-events-title">利用ログ</h3><p>質問本文を含まない直近100件です。90日で削除します。</p></div><span className="admin-pill">{usageEvents.length} / {data.usageEvents.length}件</span></div>
                <div className="admin-log-tools" aria-label="利用ログの絞り込み">
                  <label><span>検索</span><input type="search" value={usageLogSearch} onChange={(event) => setUsageLogSearch(event.target.value)} placeholder="利用者・アシスタント" /></label>
                  <div className="admin-filter-field"><span>期間</span><div className="admin-select-control"><SelectMenu label="利用ログの期間" value={usageLogPeriod} options={PERIOD_OPTIONS} onChange={setUsageLogPeriod} /></div></div>
                  <div className="admin-filter-field"><span>結果</span><div className="admin-select-control"><SelectMenu label="利用ログの結果" value={usageLogOutcome} options={outcomeOptions} onChange={setUsageLogOutcome} /></div></div>
                  <button type="button" className="admin-filter-reset" aria-label="利用ログの絞り込みを解除" title="絞り込みを解除" onClick={() => { setUsageLogSearch(""); setUsageLogPeriod(DEFAULT_USAGE_PERIOD); setUsageLogOutcome("all"); }}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 4v5h5M4.8 9A8 8 0 1 1 6 17.5" /></svg></button>
                </div>
                <div className="admin-table-wrap">
                  <table className="admin-table admin-log-table">
                    <thead><tr><th>日時</th><th>利用者</th><th>結果</th><th>利用</th><th>モード</th><th>所要時間</th></tr></thead>
                    <tbody>{usageEvents.length === 0 ? <tr className="admin-empty-row"><td colSpan={6} className="admin-empty">条件に一致する記録はありません</td></tr> : usageEvents.map((event) => (
                      <tr key={event.id}><td data-label="日時">{formatDateTime(event.occurredAt)}</td><th data-label="利用者">{event.username}</th><td data-label="結果">{outcomeLabels[event.outcome] ?? event.outcome}</td><td data-label="利用">{event.assistantId || "汎用"}{event.hasAttachment ? "・画像" : ""}</td><td data-label="モード">{event.resolvedMode || event.responseMode || "—"}</td><td data-label="所要時間">{duration(event.durationMs)}</td></tr>
                    ))}</tbody>
                  </table>
                </div>
              </section>
              <section aria-labelledby="admin-audits-title">
                <div className="admin-section-head admin-log-head"><div><h3 id="admin-audits-title">管理者操作ログ</h3><p>管理者として行った操作を1年間保持します。</p></div><span className="admin-pill">{adminAudits.length} / {data.adminAudits.length}件</span></div>
                <div className="admin-log-tools" aria-label="管理者操作ログの絞り込み">
                  <label><span>検索</span><input type="search" value={auditLogSearch} onChange={(event) => setAuditLogSearch(event.target.value)} placeholder="管理者・対象" /></label>
                  <div className="admin-filter-field"><span>期間</span><div className="admin-select-control"><SelectMenu label="管理者操作ログの期間" value={auditLogPeriod} options={PERIOD_OPTIONS} onChange={setAuditLogPeriod} /></div></div>
                  <div className="admin-filter-field"><span>操作</span><div className="admin-select-control"><SelectMenu label="管理者操作ログの操作" value={auditLogAction} options={actionOptions} onChange={setAuditLogAction} /></div></div>
                  <button type="button" className="admin-filter-reset" aria-label="管理者操作ログの絞り込みを解除" title="絞り込みを解除" onClick={() => { setAuditLogSearch(""); setAuditLogPeriod(DEFAULT_AUDIT_PERIOD); setAuditLogAction("all"); }}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 4v5h5M4.8 9A8 8 0 1 1 6 17.5" /></svg></button>
                </div>
                <div className="admin-table-wrap">
                  <table className="admin-table admin-log-table">
                    <thead><tr><th>日時</th><th>管理者</th><th>操作</th><th>対象</th></tr></thead>
                    <tbody>{adminAudits.length === 0 ? <tr className="admin-empty-row"><td colSpan={4} className="admin-empty">条件に一致する記録はありません</td></tr> : adminAudits.map((audit) => (
                      <tr key={audit.id}><td data-label="日時">{formatDateTime(audit.occurredAt)}</td><th data-label="管理者">{audit.actor}</th><td data-label="操作">{auditLabels[audit.action] ?? audit.action}</td><td data-label="対象">{audit.target || "—"}</td></tr>
                    ))}</tbody>
                  </table>
                </div>
              </section>
            </>
          )}
          </div>
        </main>
      )}

      <Toast message={toast} onClose={hideToast} />
    </div>
  );
}
