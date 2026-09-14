import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const page = readFileSync(new URL("../src/admin/AdminPage.tsx", import.meta.url), "utf8");
const styles = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");
const loadingScreen = readFileSync(new URL("../src/components/LoadingScreen.tsx", import.meta.url), "utf8");
const toast = readFileSync(new URL("../src/components/Toast.tsx", import.meta.url), "utf8");

test("管理タブ名と表示中の画面タイトルを同じ定義から描く", () => {
  for (const label of ["概要", "資料更新", "利用者・権限", "API利用状況", "監査ログ"]) {
    assert.match(page, new RegExp(`label: "${label}"`));
  }
  assert.match(page, /<h2>\{currentTab\.label\}<\/h2>/);
  assert.match(page, /role="tab" aria-selected=/);
});

test("管理通知は通常チャットと共通の黒色トーストを使う", () => {
  assert.match(page, /<Toast message=\{toast\}/);
  assert.match(toast, /className="toast"/);
  assert.doesNotMatch(page, /admin-toast/);
  assert.match(styles, /\.toast\s*\{[\s\S]*background: var\(--text\)/);
});

// 取得と再構築は自動化していない。**自動になったのは差し替えたあとの反映だけ。**
test("資料更新に再構築と差し替えの手順を表示する", () => {
  assert.match(page, /python ingest\/rebuild\.py/);
  assert.match(page, /sh tools\/publish-index\.sh/);
  assert.match(page, /再デプロイは要りません/);
});

test("更新中はSVGを回さず固定寸法のスピナーへ切り替える", () => {
  assert.match(page, /loading \? <span className="admin-spinner"/);
  assert.match(styles, /\.admin-spinner\s*\{[\s\S]*width: 17px;[\s\S]*height: 17px;/);
  assert.doesNotMatch(styles, /admin-refresh\.is-loading svg/);
});

test("通常画面と同じブランドと利用者メニューを管理画面にも表示する", () => {
  assert.match(page, /src=\{APP_URLS\.logo\}/);
  assert.match(page, /className="admin-mode-label">管理画面/);
  assert.match(page, /className="profile-avatar"/);
  assert.match(page, /WASA Wikiを開く/);
  assert.match(page, /ログアウト/);
});

test("要対応を置かず画面・API・索引のバージョンを表示する", () => {
  assert.doesNotMatch(page, /admin-alerts-title|>要対応</);
  assert.match(page, /id="admin-version-title">本番バージョン/);
  assert.match(page, /画面・API・索引が意図したバージョンへ切り替わったか確認します/);
  assert.match(page, /__WASA_BUILD_VERSION__/);
  assert.match(page, /data\.system\.indexVersion/);
});

// 「反映後を再確認」は無くした。索引を差し替えると本番が自分で読み直すので、
// 反映を確かめに戻る必要がない（2026-09-12）。
test("資料更新の3段階と共通選択メニューによる監査ログ絞り込みを表示する", () => {
  for (const label of ["公開元を確認", "手元で再構築", "差し替え"]) {
    assert.match(page, new RegExp(label));
  }
  assert.match(page, /aria-label="利用ログの絞り込み"/);
  assert.match(page, /aria-label="管理者操作ログの絞り込み"/);
  assert.match(page, /usageLogOutcome/);
  assert.match(page, /auditLogAction/);
  assert.match(page, /<SelectMenu label="利用ログの期間"/);
  assert.match(page, /aria-label="利用ログの絞り込みを解除"/);
  assert.doesNotMatch(page, /<select/);
});

test("管理者情報の確認中は通常ログインと同じ中央ローディングを表示する", () => {
  assert.match(page, /<LoadingScreen label="管理者情報を確認しています" admin/);
  assert.match(loadingScreen, /center app-loading.*admin-initial-loading/);
  assert.match(styles, /\.admin-initial-loading\s*\{[\s\S]*min-height: 100dvh/);
});

test("障害調査用の実行情報を折り畳まず表示する", () => {
  assert.match(page, /<section className="admin-system"/);
  assert.doesNotMatch(page, /<details className="admin-system"/);
});

test("API送信の実測値と集計開始前は遡らない旨を表示する", () => {
  assert.match(page, /data\.quota\.totalRequests/);
  assert.match(page, /反映前の質問は遡って加算されません/);
});

test("スマホでは管理タブをヘッダー直下へ固定し、表を項目名付きカードへ変える", () => {
  assert.match(styles, /\.admin-tabs\s*\{[\s\S]*position: sticky;[\s\S]*top: calc\(var\(--admin-header-height\) \+ 8px\)/);
  assert.match(styles, /@media \(max-width: 560px\)[\s\S]*\.admin-table tbody tr > \[data-label\]::before/);
  assert.match(styles, /\.admin-log-tools\s*\{[\s\S]*grid-template-columns: repeat\(2, minmax\(0, 1fr\)\)/);
  assert.match(page, /data-label="利用者"/);
  assert.match(page, /data-label="所要時間"/);
});

test("管理タブは横幅を固定し、短い遷移アニメーションで切り替える", () => {
  assert.match(styles, /\.admin-shell\s*\{[\s\S]*scrollbar-gutter: stable/);
  assert.match(page, /<div key=\{activeTab\} className="admin-tab-panel">/);
  assert.match(styles, /@keyframes admin-tab-enter/);
});
