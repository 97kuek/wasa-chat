import { API_ORIGIN } from "../config";

export type AdminUserUsage = {
  username: string;
  today: number;
  sevenDays: number;
  thirtyDays: number;
  lastUsed?: string;
  limitReached: boolean;
  role?: "owner" | "co_admin";
  /** 許可済みの参照先。誰に何を許しているかを一覧で見えるようにする */
  tools?: string[];
};

export type AdminRole = {
  username: string;
  role: "owner" | "co_admin";
  grantedBy?: string;
  grantedAt?: string;
};

export type SourceDelta = {
  source: "wiki" | "site";
  added: string[];
  updated: string[];
  removed: string[];
};

export type SourceCheckResult = {
  checkedAt: string;
  checkedBy: string;
  changed: boolean;
  deltas: SourceDelta[];
};

export type AdminUsageEvent = {
  id: string;
  username: string;
  occurredAt: string;
  outcome: string;
  responseMode?: string;
  resolvedMode?: string;
  assistantId?: string;
  hasAttachment?: boolean;
  durationMs: number;
};

export type AdminAudit = {
  id: string;
  actor: string;
  action: string;
  target?: string;
  occurredAt: string;
};

export type AdminOverview = {
  generatedAt: string;
  system: {
    ok: boolean;
    llm: string;
    store: string;
    indexSource: string;
    revision: string;
    codeVersion: string;
    indexVersion: string;
    indexPublishedAt?: string;
    startedAt: string;
  };
  currentAdmin: { username: string; role: "owner" | "co_admin" };
  admins: AdminRole[];
  sourceCheck: { available: boolean; hasResult: boolean; last?: SourceCheckResult };
  updateProgress: {
    stage: "unavailable" | "not_checked" | "current" | "changes_detected" | "verify_needed";
    checkedAt?: string;
    publishedAt?: string;
    changes: number;
  };
  quota: {
    day: string;
    resetAt: string;
    state: "available" | "rate_limited" | "daily_quota";
    retryAt?: string;
    totalRequests: number;
    estimated: boolean;
    models: { model: string; requests: number; limit: number; remaining: number }[];
  };
  summary: { todayQuestions: number; activeUsersToday: number; knownUsers: number; dailyLimit: number };
  users: AdminUserUsage[];
  usageEvents: AdminUsageEvent[];
  adminAudits: AdminAudit[];
};

async function adminRequest<T>(path: string, fallbackError: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_ORIGIN}${path}`, { credentials: "include", ...init });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error ?? fallbackError);
  return body as T;
}

export function adminOverview(): Promise<AdminOverview> {
  return adminRequest<AdminOverview>("/api/admin/overview", "管理情報を読み込めませんでした");
}

export async function setCoAdmin(username: string, enabled: boolean): Promise<void> {
  await adminRequest("/api/admin/roles", "管理者権限を変更できませんでした", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, enabled }),
  });
}

/**
 * 共有ドライブを読んでよい利用者を切り替える。
 *
 * ⚠️ **共有ドライブは部内資料より緩い場所。** Wikiに書かない人が置いた資料が
 * 入るため、誰が読めるかを個別に決める。Discordは公開チャンネルだけを読むので
 * 許可の対象ではない。
 */
export async function setToolGrant(username: string, tool: string, enabled: boolean): Promise<void> {
  await adminRequest("/api/admin/tools", "参照先の許可を変更できませんでした", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, tool, enabled }),
  });
}

export function checkSources(): Promise<SourceCheckResult> {
  return adminRequest<SourceCheckResult>("/api/admin/source-check", "更新を確認できませんでした", { method: "POST" });
}
