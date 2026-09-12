import type { Source } from "../api";
import { referenceSections } from "../chat";

type Props = {
  sources: Source[];
  active?: boolean;
};

/**
 * 回答が読んだ節を**1行**で出す。
 *
 * 一度は資料ごとの畳めるブロックにしたが、資料が4件あると縦に伸びて
 * **チャット画面を占有してしまう**（2026-09-13の指摘で差し戻し）。
 * 出典のタイトルとリンクは下の資料カードが持っているので、ここは
 * 「どの節を読んだか」だけを、回答の邪魔にならない密度で示せばよい。
 *
 * 回答前後で同じ位置と密度を保ち、資料カードの出入りによるレイアウト変化も防ぐ。
 */
export function ReferenceSummary({ sources, active = false }: Props) {
  const sections = referenceSections(sources);
  if (sections.length === 0) return null;

  return (
    <p className={`answer-reference${active ? " is-active" : ""}`} aria-live={active ? "polite" : undefined}>
      <span className="citation-mark" aria-hidden="true" />
      <span><strong>{active ? "参照中" : "参照"}</strong>：{sections.join("／")}</span>
    </p>
  );
}
