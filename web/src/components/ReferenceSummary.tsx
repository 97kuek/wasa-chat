import type { Source } from "../api";

type Props = {
  sources: Source[];
  active?: boolean;
};

/** 出所の呼び名。Go側 pipeline.OriginLabel と同じにすること。 */
const ORIGIN_LABELS: Record<string, string> = {
  wiki: "引き継ぎWiki",
  site: "公式サイト",
  fee: "フライトシミュレータ",
};

/**
 * 回答が読んだ資料を、**資料ごとのブロック**で出す。
 *
 * 以前は読んだ節を「／」で1行につないでいた。資料が4件あると
 * 「空力設計 > 詳細設計 > 翼型設計／空力設計(41st) > …」と延々と続き、
 * **どの節がどの資料のものか読めなかった**（2026-09-12の報告）。
 *
 * 畳んだ状態では資料名と出所だけ。開くと最終更新と、その資料から読んだ節が出る。
 *
 * ⚠️ **本文の抜粋は出さない。** 出すにはサーバーが節の本文を返す必要があるが、
 * 履歴はFirestoreの1ドキュメント1MBに収めているので保存できない。回答直後だけ
 * 見えて履歴では消える、という食い違いを作らないほうがよい（2026-09-12に人間が判断）。
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
      <div className="reference-list">
        {sources.map((source) => (
          <details className="reference-card" key={source.url || source.title}>
            <summary>
              <span className="reference-title">{source.title}</span>
              <span className="reference-origin">{ORIGIN_LABELS[source.origin ?? "wiki"] ?? "資料"}</span>
            </summary>
            <div className="reference-body">
              {source.last_edited && (
                <p className="reference-meta">最終更新 {source.last_edited}</p>
              )}
              {/* 節が確定するのは回答の途中。届くまでは資料名だけになる */}
              {source.sections && source.sections.length > 0 && (
                <>
                  <p className="reference-meta">読んだ節（{source.sections.length}）</p>
                  <ul className="reference-sections">
                    {source.sections.map((section) => (
                      <li key={section}>{section}</li>
                    ))}
                  </ul>
                </>
              )}
              {source.url && (
                <a className="reference-open" href={source.url} target="_blank" rel="noreferrer noopener">
                  資料を開く
                </a>
              )}
            </div>
          </details>
        ))}
      </div>
    </div>
  );
}
