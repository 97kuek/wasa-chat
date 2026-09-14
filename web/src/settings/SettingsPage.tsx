import type { DiscordServer, Settings } from "../api";
import { Spinner } from "../components/Spinner";
import { ToolIcon } from "../components/ToolIcon";

/**
 * サーバー1件。**名前だけを並べない。**
 *
 * ⚠️ 「WASA 41代」「WASA 42代」と4つ並ぶのが普通の状態で、初めて設定する人には
 * **どれが自分のいるサーバーか分からない**（2026-09-14の指摘）。Discordで見慣れた
 * アイコンと人数を出して、見分けが付くようにする。チャンネル数は「選んだら何を
 * 読むか」であり、説明文を足さずに機能の範囲を示せる。
 */
function ServerRow({ server, children }: { server: DiscordServer; children: React.ReactNode }) {
  const facts = [
    server.members ? `${server.members}人` : "",
    server.channels ? `公開${server.channels}チャンネル` : "",
  ].filter(Boolean);
  return (
    <li>
      {server.icon
        ? <img className="settings-server-icon" src={server.icon} alt="" />
        : <span className="settings-server-icon is-blank" aria-hidden="true">{Array.from(server.name)[0] ?? "?"}</span>}
      <span className="settings-server-name">
        {server.name}
        {facts.length > 0 && <small>{facts.join("・")}</small>}
      </span>
      {children}
    </li>
  );
}

type Props = {
  settings: Settings | null;
  /** 保存中の項目。連打で二重に保存しないために持つ */
  busy: string;
  error: string;
  refreshing: boolean;
  onToggleTool: (id: string, enabled: boolean) => void;
  onConnectServer: (id: string) => void;
  onDisconnectServer: (id: string) => void;
  onRefreshServers: () => void;
};

/**
 * 設定画面（外部サービス連携）。
 *
 * ⚠️ **入力欄の「+」から移した**（2026-09-14に人間が判断）。連携は質問のたびに
 * 切り替えるものではなく、一度つないだら続くものである。「+」のままだと、
 * つなぐ・外す・つなぎ替えるという操作の置き場所が無かった。
 *
 * ⚠️ **Discordだけ作りが違う。** ほかはオン・オフだが、Discordは**どのサーバーと
 * つなぐか**が人によって違う（代ごとにサーバーが変わる）。連携したサーバーの
 * 有無がそのままオン・オフなので、スイッチは置かない。
 *
 * ⚠️ **説明文を置かない**（2026-09-14の指摘）。一度は「Discordの `/wasa` とは
 * 別物だ」という説明を並べ、次に畳んだ補足（details）へ移したが、**どちらも
 * 要らない**と判断された。画面で示すのは見出し（「検索する」「追加できる」）と、
 * 1行で足りるものだけ。手順・区別・注意は**サポートページ**にある
 * （`web/public/support.html`。利用者メニューの「ヘルプとポリシー」から開く）。
 */
export function SettingsPage({
  settings, busy, error, refreshing,
  onToggleTool, onConnectServer, onDisconnectServer, onRefreshServers,
}: Props) {
  if (!settings) {
    // 読めなかったときは、読めなかったと出す（空の一覧を出すと「外れた」と読める）
    return (
      <main className="settings-page">
        {error ? (
          <p className="settings-error" role="alert">{error}</p>
        ) : (
          <div className="settings-loading" role="status">
            <Spinner />
            <span>読み込み中</span>
          </div>
        )}
      </main>
    );
  }

  const { connected, joinable, inviteUrl, maxServers, reason } = settings.discord;
  const full = connected.length >= maxServers;

  return (
    <main className="settings-page">
      <header className="settings-head">
        <h2>外部サービス連携</h2>
      </header>

      {error && <p className="settings-error" role="alert">{error}</p>}

      <article className="settings-card">
        <div className="settings-card-head">
          <ToolIcon id="discord" />
          <div className="settings-card-copy">
            <h3>Discord</h3>
            {/* ⚠️ **1行で何をするかだけ書く。** 「公開チャンネル」までをこの1行に
                入れてあるので、読む範囲の断りを別に置かなくてよい */}
            <p>公開チャンネルの会話を検索します</p>
          </div>
          {/* ⚠️ **「なし」は出さない。** つないでいない状態は一覧を見れば分かる。
              出すと、直さないといけない設定に見える（2026-09-14の指摘） */}
          {connected.length > 0 && (
            <span className="settings-state is-on">{connected.length}サーバー</span>
          )}
        </div>

        {/* 使えない理由は隠さない。設定すれば使えることが伝わらないため */}
        {reason && <p className="settings-note">{reason}</p>}

        {connected.length > 0 && (
          <>
            <h4 className="settings-subhead">検索する</h4>
            <ul className="settings-servers">
              {connected.map((server) => (
                <ServerRow key={server.id} server={server}>
                  <button
                    type="button"
                    className="settings-disconnect"
                    disabled={busy === server.id}
                    onClick={() => onDisconnectServer(server.id)}
                  >
                    {busy === server.id ? "…" : "解除"}
                  </button>
                </ServerRow>
              ))}
            </ul>
          </>
        )}

        {joinable.length > 0 && (
          <>
            <h4 className="settings-subhead">追加できる</h4>
            <ul className="settings-servers">
              {joinable.map((server) => (
                <ServerRow key={server.id} server={server}>
                  <button
                    type="button"
                    className="settings-connect"
                    disabled={busy === server.id || full}
                    onClick={() => onConnectServer(server.id)}
                  >
                    {busy === server.id ? "…" : "追加"}
                  </button>
                </ServerRow>
              ))}
            </ul>
          </>
        )}

        {connected.length === 0 && joinable.length === 0 && !reason && (
          <p className="settings-empty">ボットが入っているサーバーがありません。</p>
        )}
        {/* ⚠️ 押せない理由をボタンの近くに書く。書かないと、壊れているように見える */}
        {full && joinable.length > 0 && <p className="settings-note">{maxServers}サーバーまで</p>}

        <div className="settings-card-actions">
          {inviteUrl ? (
            <a className="settings-invite" href={inviteUrl} target="_blank" rel="noreferrer noopener">
              ボットをサーバーに入れる
            </a>
          ) : (
            <p className="settings-note">ボットの追加URLを出せません。管理者へ連絡してください。</p>
          )}
          {/* 入れた直後は一覧に出てこない（サーバー側が10分覚えている）。
              「入れたのに出てこない」を、入れ直しで解決させないための口 */}
          <button type="button" className="settings-secondary" disabled={refreshing} onClick={onRefreshServers}>
            {refreshing ? "更新中…" : "一覧を更新"}
          </button>
        </div>
      </article>

      {settings.tools.map((tool) => (
        <article className="settings-card" key={tool.id}>
          <div className="settings-card-head">
            <ToolIcon id={tool.id} />
            <div className="settings-card-copy">
              <h3>{tool.name}</h3>
              {/* 使えないものも並べる。**黙って消すと「無い機能」に見える** */}
              <p>{tool.available ? tool.description : tool.reason}</p>
            </div>
            {tool.available ? (
              <label className="tool-switch">
                <input
                  type="checkbox"
                  checked={tool.enabled}
                  disabled={busy === tool.id}
                  onChange={() => onToggleTool(tool.id, !tool.enabled)}
                />
                <span className="tool-switch-track" aria-hidden="true" />
                <span className="visually-hidden">{tool.name}を参照する</span>
              </label>
            ) : (
              <span className="settings-state">未接続</span>
            )}
          </div>
        </article>
      ))}
    </main>
  );
}
