import { useEffect, useRef, useState } from "react";
import type { Tool } from "../api";

type Props = {
  tools: Tool[];
  enabled: string[];
  onChange: (enabled: string[]) => void;
  disabled?: boolean;
};

/**
 * 入力欄の「+」から、参照先のオン・オフを切り替える。
 *
 * **既定は全部オフ。** 引き継ぎ資料（Wiki・公式サイト・フライトシミュレータ）は
 * 常に読むので、ここに出るのは**それ以外の置き場所**だけである。増やすほど
 * 1回の質問で読む量が増え、無料枠を早く使うので、要るときだけ足す形にする。
 *
 * ⚠️ **アシスタントの参照範囲とは向きが違う。** アシスタントは範囲を
 * **狭める**もの（docs/09 D-5）、ここは**足す**もの。両方指定されたときは
 * 足したうえで狭めるので、範囲外のものは結局読まれない。
 */
export function ToolMenu({ tools, enabled, onChange, disabled = false }: Props) {
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
        aria-label="参照先を選ぶ"
        title="参照先を選ぶ"
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
        <div className="tool-popover" role="group" aria-label="参照先">
          <p className="tool-popover-head">参照先を追加</p>
          <p className="tool-popover-note">引き継ぎ資料はいつでも読みます。ここは置き場所を足すものです。</p>
          <ul>
            {tools.map((tool) => (
              <li key={tool.id}>
                <div className="tool-item-text">
                  <span className="tool-item-name">{tool.name}</span>
                  <span className="tool-item-note">{tool.available ? tool.description : tool.reason}</span>
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
