import { useEffect, useRef, useState } from "react";
import type { Tool } from "../api";
import { SelectMenu } from "./SelectMenu";

type Props = {
  tools: Tool[];
  enabled: string[];
  onChange: (enabled: string[]) => void;
  /** 検索するDiscordのサーバー。空ならすべて */
  discordServer: string;
  onDiscordServerChange: (id: string) => void;
  disabled?: boolean;
};

/**
 * サービスごとの印。**どのサービスかは名前より形で分かる。**
 * 知らないIDが来ても崩れないよう、既定の形を持たせておく。
 */
function ToolIcon({ id }: { id: string }) {
  if (id === "discord") {
    return (
      <svg className="tool-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path
          fill="#5865F2"
          d="M19.3 5.3A16.9 16.9 0 0 0 15.1 4l-.2.4a12.6 12.6 0 0 1 3.7 1.9 15.7 15.7 0 0 0-13.2 0A12.6 12.6 0 0 1 9.1 4.4L8.9 4a16.9 16.9 0 0 0-4.2 1.3C2.1 9.3 1.4 13.1 1.8 16.9a17 17 0 0 0 5.1 2.6l1.1-1.7a11 11 0 0 1-1.7-.8l.4-.3a12.1 12.1 0 0 0 10.6 0l.4.3c-.5.3-1.1.6-1.7.8l1.1 1.7a17 17 0 0 0 5.1-2.6c.5-4.4-.7-8.2-2.9-11.6ZM8.5 14.7c-1 0-1.9-.9-1.9-2.1s.8-2.1 1.9-2.1 1.9 1 1.9 2.1-.8 2.1-1.9 2.1Zm7 0c-1 0-1.9-.9-1.9-2.1s.8-2.1 1.9-2.1 1.9 1 1.9 2.1-.8 2.1-1.9 2.1Z"
        />
      </svg>
    );
  }
  if (id === "drive") {
    // Googleドライブの三角。3つの面を色で分ける
    return (
      <svg className="tool-icon" viewBox="0 0 24 24" aria-hidden="true">
        <path fill="#0066DA" d="M2 18.2 4.3 22h9.2l-2.3-3.8H2Z" />
        <path fill="#00AC47" d="m8.9 2-4.6 8 2.3 3.9L11.2 6 8.9 2Z" />
        <path fill="#EA4335" d="M15.1 2H8.9l6.9 12h6.2L15.1 2Z" />
        <path fill="#FFBA00" d="M22 14h-6.2l-2.3 4h6.2L22 14Z" />
        <path fill="#00832D" d="M2 18.2h9.2L15.8 10 11.2 2 2 18.2Z" opacity=".0" />
      </svg>
    );
  }
  return (
    <svg className="tool-icon" viewBox="0 0 24 24" aria-hidden="true">
      <circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeWidth="1.6" />
    </svg>
  );
}

/**
 * 入力欄の「+」から、外部サービスとの連携を切り替える。
 *
 * **既定は全部オフ。** 引き継ぎ資料（Wiki・公式サイト・フライトシミュレータ）は
 * 常に読むので、ここに出るのは**それ以外の置き場所**だけである。増やすほど
 * 1回の質問で読む量が増え、無料枠を早く使うので、要るときだけ足す形にする。
 *
 * ⚠️ **アシスタントの参照範囲とは向きが違う。** アシスタントは範囲を
 * **狭める**もの（docs/09 D-5）、ここは**足す**もの。両方指定されたときは
 * 足したうえで狭めるので、範囲外のものは結局読まれない。
 */
export function ToolMenu({
  tools, enabled, onChange, discordServer, onDiscordServerChange, disabled = false,
}: Props) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    const closeFromOutside = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      setOpen(false);
      trigger.current?.focus();
    };
    document.addEventListener("pointerdown", closeFromOutside);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeFromOutside);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [open]);

  if (tools.length === 0) return null;

  // 使えないものも並べる。**黙って消すと「無い機能」に見える。**
  // 設定が要るだけなら、そう書いてあるほうが管理者に伝わる
  const active = tools.filter((tool) => tool.available && enabled.includes(tool.id));

  function toggle(id: string) {
    onChange(enabled.includes(id) ? enabled.filter((value) => value !== id) : [...enabled, id]);
  }

  return (
    <div className="tool-menu" ref={root}>
      <button
        ref={trigger}
        type="button"
        className={`tool-trigger${active.length > 0 ? " is-active" : ""}`}
        aria-label="外部サービスと連携"
        title="外部サービスと連携"
        aria-expanded={open}
        aria-haspopup="true"
        disabled={disabled}
        onClick={() => setOpen((value) => !value)}
      >
        <svg viewBox="0 0 24 24" aria-hidden="true">
          <path d="M12 5v14M5 12h14" />
        </svg>
        {/* 何かオンになっていることを、開かなくても分かるようにする */}
        {active.length > 0 && <span className="tool-badge" aria-hidden="true" />}
      </button>

      {open && (
        <div className="tool-popover" role="group" aria-label="外部サービスと連携">
          <p className="tool-popover-head">外部サービスと連携</p>
          <ul>
            {tools.map((tool) => (
              <li key={tool.id}>
                <ToolIcon id={tool.id} />
                <div className="tool-item-text">
                  <span className="tool-item-name">{tool.name}</span>
                  <span className="tool-item-note">{tool.available ? tool.description : tool.reason}</span>
                  {/* ⚠️ **代ごとにDiscordのサーバーが変わる。** どの代の会話を
                      読むかは利用者にしか決められないので、ここで選ばせる。
                      選択肢の見た目はOSごとに変わるので、画面と同じ部品を使う。
                      **1つしか無いうちは出さない。** 選びようが無いものを並べても、
                      設定が増えたように見えるだけ（2026-09-13の指摘） */}
                  {tool.available && enabled.includes(tool.id) && (tool.servers?.length ?? 0) > 1 && (
                    <div className="tool-item-server">
                      <SelectMenu
                        label="検索するDiscordサーバー"
                        value={discordServer}
                        options={[
                          { value: "", label: "すべてのサーバー" },
                          ...(tool.servers ?? []).map((server) => ({ value: server.id, label: server.name })),
                        ]}
                        onChange={onDiscordServerChange}
                      />
                    </div>
                  )}
                </div>
                {tool.available ? (
                  <label className="tool-switch">
                    <input
                      type="checkbox"
                      checked={enabled.includes(tool.id)}
                      onChange={() => toggle(tool.id)}
                    />
                    <span className="tool-switch-track" aria-hidden="true" />
                    <span className="visually-hidden">{tool.name}を参照する</span>
                  </label>
                ) : (
                  <span className="tool-unavailable">未接続</span>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
