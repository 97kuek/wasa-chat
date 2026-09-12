import type { Source } from "../api";

type Props = {
  sources: Source[];
  active?: boolean;
};

/**
 * 回答が読んだ資料を、**資料ごとに畳めるブロック**で出す。
 *
 * 以前は読んだ節を「／」で1行につないでいたが、資料が4件あると
 * 「空力設計 > 詳細設計 > 翼型設計／空力設計(41st) > …」と延々と続き、
 * **どの節がどの資料のものか読めなかった**（2026-09-12の報告）。
 *
 * 資料名だけを畳んだ状態で並べ、開くとその資料から読んだ節が出る。
 * 回答前後で同じ位置に出すので、カードの出入りでレイアウトは動かない。
 */
export function ReferenceSummary({ sources, active = false }: Props) {
  if (sources.length === 0) return null;

  return (
    <div className={`answer-reference${active ? " is-active" : ""}`} aria-live={active ? "polite" : undefined}>
      <p className="answer-reference-head">
        <span className="citation-mark" aria-hidden="true" />
        <strong>{active ? "参照中" : "参照"}</strong>
        <span className="answer-reference-count">{sources.length}件</span>
      </p>
      <ul className="reference-list">
        {sources.map((source) => (
          <li key={source.url || source.title}>
            <details>
              <summary>
                <span className="reference-title">{source.title}</span>
                {source.sections && source.sections.length > 0 && (
                  <span className="reference-sections-count">{source.sections.length}節</span>
                )}
              </summary>
              <div className="reference-body">
                {/* どこを開けば確かめられるかまで示す。節が確定するのは
                    回答の途中なので、届くまでは資料名だけになる */}
                {source.sections && source.sections.length > 0 && (
                  <ul className="reference-sections">
                    {source.sections.map((section) => (
                      <li key={section}>{section}</li>
                    ))}
                  </ul>
                )}
                {source.url && (
                  <a href={source.url} target="_blank" rel="noreferrer noopener">
                    資料を開く
                  </a>
                )}
              </div>
            </details>
          </li>
        ))}
      </ul>
    </div>
  );
}
