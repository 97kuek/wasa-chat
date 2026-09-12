import type { ChangeEvent, Dispatch, FormEvent, SetStateAction } from "react";

import type { Assistant, AssistantDraft, GlossaryEntry, ScopeCounts, Team } from "../api";
import { AssistantAvatar } from "../avatar";
import { SelectMenu, type SelectOption } from "../components/SelectMenu";
import { APP_LIMITS } from "../config";

/**
 * 指示の書き方を4つに分ける。
 *
 * 以前は1500字の自由記述が1つだけで、**何を書けばよいかが画面から分からなかった。**
 * 分けたぶんは保存時に1本の文字列へ戻すので、サーバー側（検証・system規則・
 * プロンプトの組み立て）は何も変わらない。
 */
const INSTRUCTION_PARTS = [
  { key: "role", heading: "役割", hint: "例: 新入生に教える先輩として答える", rows: 3 },
  { key: "tone", heading: "口調", hint: "例: 語尾を「〜しゅよ」にする。一人称は「ぼく」", rows: 3 },
  { key: "format", heading: "出力の形", hint: "例: 箇条書き中心。手順には番号を振る", rows: 3 },
  { key: "avoid", heading: "やらないこと", hint: "例: 専門用語をそのまま使わない", rows: 2 },
] as const;

type InstructionParts = Record<(typeof INSTRUCTION_PARTS)[number]["key"], string>;

const emptyInstructionParts = (): InstructionParts =>
  Object.fromEntries(INSTRUCTION_PARTS.map((part) => [part.key, ""])) as InstructionParts;

/**
 * 型に沿った指示を4つの欄へ戻す。沿っていなければ null を返す。
 *
 * **自由記述で書かれた既存の指示を壊さないこと。** 無理に分解すると、
 * 作った人の文章が勝手に並べ替わる。読めないものは自由記述のまま扱う。
 */
function splitInstruction(text: string): InstructionParts | null {
  if (!text.trim()) return emptyInstructionParts(); // 新規作成は型から始める
  const headings = new Map<string, keyof InstructionParts>(
    INSTRUCTION_PARTS.map((part) => [part.heading as string, part.key]),
  );
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  if (!lines[0].startsWith("## ") || !headings.has(lines[0].slice(3).trim())) return null;

  const parts = emptyInstructionParts();
  let current: keyof InstructionParts | null = null;
  const buffer: string[] = [];
  const flush = () => {
    if (current) parts[current] = buffer.join("\n").trim();
    buffer.length = 0;
  };
  for (const line of lines) {
    if (line.startsWith("## ")) {
      const key = headings.get(line.slice(3).trim());
      if (!key) return null; // 知らない見出しがある ＝ 手で書かれた文章
      flush();
      current = key;
      continue;
    }
    buffer.push(line);
  }
  flush();
  return parts;
}

/** 4つの欄を1本の指示へ戻す。**空の欄は見出しごと落とす**（プロンプトに空行を並べない）。 */
function joinInstruction(parts: InstructionParts): string {
  return INSTRUCTION_PARTS.filter((part) => parts[part.key].trim())
    .map((part) => `## ${part.heading}\n${parts[part.key].trim()}`)
    .join("\n\n");
}

/** 作成・編集・閲覧で同じ画面を使う。閲覧では入力を読み取り専用にするだけ。 */
export type AssistantFormMode = "create" | "edit" | "view";

const ORIGIN_OPTIONS: SelectOption[] = [
  { value: "", label: "すべて" },
  { value: "wiki", label: "引き継ぎWikiのみ" },
  { value: "site", label: "公式サイトのみ（部外に出せる情報だけ）" },
];

type Props = {
  mode: AssistantFormMode;
  readOnly: boolean;
  /** 既存を開いているときだけ入る。新規作成では null */
  assistant: Assistant | null;
  draft: AssistantDraft;
  setDraft: Dispatch<SetStateAction<AssistantDraft>>;
  teams: Team[];
  scopeCounts: ScopeCounts;
  error: string;
  onSubmit: (event: FormEvent) => void;
  onPickIcon: (event: ChangeEvent<HTMLInputElement>) => void;
  onClose: () => void;
  onDuplicate: (source: Assistant) => void;
  onStartEdit: () => void;
};

/**
 * アシスタントの設定画面。
 *
 * App.tsx から切り出してある。**ここが触れるのは draft だけ**で、保存・複製・
 * 画面遷移は呼び出し側が持つ。設定項目が増えてもApp.tsxが太らないようにするため。
 */
export function AssistantSettingsForm({
  mode, readOnly, assistant, draft, setDraft, teams, scopeCounts, error,
  onSubmit, onPickIcon, onClose, onDuplicate, onStartEdit,
}: Props) {
  // 指示は1本の文字列が正本で、4つの欄はその見え方にすぎない。
  // 状態を2つ持つと必ずずれるので、毎回そこから読み直す
  const instructionParts = splitInstruction(draft.instruction);
  const scopeCount = scopeCounts[`${draft.origin ?? ""}/${draft.team ?? ""}`] ?? 0;

  /** 指示の4つの欄のうち1つを書き換える。保存する形は1本の文字列のまま。 */
  const updateInstructionPart = (parts: InstructionParts, key: keyof InstructionParts, value: string) =>
    setDraft((d) => ({ ...d, instruction: joinInstruction({ ...parts, [key]: value }) }));

  const updateGlossary = (next: GlossaryEntry[]) => setDraft((d) => ({ ...d, glossary: next }));

  return (
          <form className="assistant-form" onSubmit={onSubmit}>
            <header className="assistant-form-head">
              <div className="assistant-form-title">
                <AssistantAvatar name={draft.name || "?"} icon={draft.icon} size={40} />
                <div>
                  <h2>{draft.name || (mode === "create" ? "アシスタントを作る" : "アシスタント")}</h2>
                  <p className="muted">
                    {assistant ? <>作成: {assistant.author}</> : "誰でも作れて、全員が使えます"}
                  </p>
                </div>
              </div>
              {/* 開いた直後は閲覧。編集はここから明示的に入る */}
              {readOnly && assistant?.canEdit && (
                <button type="button" className="primary" onClick={onStartEdit}>編集する</button>
              )}
            </header>

            <p className="assistant-form-note">
              書けるのは<strong>口調・書き方・参照範囲・用語集</strong>だけです。
              出典の一覧と参照範囲はサーバー側で決まるため、指示では変えられません。
            </p>

            {/* 設定する項目は多くない。タブで分けると、全部見るのに
                4回切り替えることになる。1画面に並べて横幅を使う */}
            <div className="assistant-sections">
            <section className="assistant-section">
              <h3>基本設定</h3>
              <div className="assistant-panel">
                <div className="assistant-icon-editor">
                  <div className="assistant-icon-pick">
                    {/* 画像が無くても頭文字で成立させる。用意しないと見栄えが悪い状態にすると、
                        結局だれもアシスタントを作らなくなる */}
                    <AssistantAvatar name={draft.name || "?"} icon={draft.icon} size={72} />
                    {!readOnly && <label className="assistant-icon-button">
                      <span>{draft.icon ? "変更" : "画像を選ぶ"}</span>
                      <input type="file" accept="image/png,image/jpeg,image/webp" onChange={(event) => onPickIcon(event)} />
                    </label>}
                    {!readOnly && draft.icon && (
                      <button type="button" className="linkish" onClick={() => {
                        setDraft((d) => ({ ...d, icon: undefined }));
                      }}>
                        画像を外す
                      </button>
                    )}
                  </div>
                </div>
                <label>
                  <span>名前</span>
                  <input value={draft.name} maxLength={APP_LIMITS.assistantNameRunes} required readOnly={readOnly}
                    onChange={(event) => setDraft((d) => ({ ...d, name: event.target.value }))} />
                </label>
                <label>
                  <span>説明（一覧に出ます）</span>
                  <input value={draft.description} maxLength={APP_LIMITS.assistantDescriptionRunes} readOnly={readOnly}
                    onChange={(event) => setDraft((d) => ({ ...d, description: event.target.value }))} />
                </label>
              </div>
            </section>

            <section className="assistant-section">
              <h3>参照範囲</h3>
              <div className="assistant-panel">
                {/* 選んだ結果が何件になるかを出す。0件の組み合わせ（公式サイト×電装班など）
                    を選んでも、これが無いと質問するまで気付けない */}
                <p className="assistant-scope-count">
                  この条件で参照できる資料: <strong>{scopeCount}件</strong>
                  {scopeCount === 0 && <span className="assistant-scope-warn">（0件です。範囲を広げてください）</span>}
                </p>
                {readOnly ? (
                  <div className="assistant-scope">
                    <div className="assistant-field">
                      <span>参照する出所</span>
                      <p className="assistant-readonly-value">
                        {ORIGIN_OPTIONS.find((option) => option.value === (draft.origin ?? ""))?.label ?? "すべて"}
                      </p>
                    </div>
                    <div className="assistant-field">
                      <span>参照する区分</span>
                      <p className="assistant-readonly-value">
                        {teams.find((team) => team.value === draft.team)?.label ?? "すべて"}
                      </p>
                    </div>
                  </div>
                ) : (
                  <div className="assistant-scope">
                    <div className="assistant-field">
                      <span>参照する出所</span>
                      <SelectMenu
                        label="参照する出所"
                        value={draft.origin ?? ""}
                        options={ORIGIN_OPTIONS}
                        onChange={(value) => setDraft((d) => ({
                          ...d,
                          origin: (value || undefined) as AssistantDraft["origin"],
                        }))}
                      />
                    </div>
                    <div className="assistant-field">
                      <span>参照する区分</span>
                      <SelectMenu
                        label="参照する区分"
                        value={draft.team ?? ""}
                        options={[
                          { value: "", label: "すべて" },
                          ...teams.map((team) => ({ value: team.value, label: team.label })),
                        ]}
                        onChange={(value) => setDraft((d) => ({ ...d, team: value || undefined }))}
                      />
                    </div>
                  </div>
                )}
                <p className="assistant-hint">
                  指定できるのは<strong>狭める方向だけ</strong>です。範囲外の資料はサーバー側で外れるため、
                  指示に何を書いても混ざりません。
                </p>
              </div>
            </section>

            <section className="assistant-section">
              <h3>指示（口調・書き方）</h3>
              <div className="assistant-panel">
                {instructionParts ? (
                  INSTRUCTION_PARTS.map((part) => (
                    <label key={part.key}>
                      <span>{part.heading}</span>
                      <textarea
                        value={instructionParts[part.key]}
                        rows={part.rows}
                        readOnly={readOnly}
                        placeholder={part.hint}
                        onChange={(event) => updateInstructionPart(instructionParts, part.key, event.target.value)}
                      />
                    </label>
                  ))
                ) : (
                  <>
                    {/* 型に沿っていない既存の指示は、勝手に分解しない。
                        文章が並べ替わると、作った人の意図が変わる */}
                    <label>
                      <span>指示（自由記述）</span>
                      <textarea value={draft.instruction} rows={10} maxLength={APP_LIMITS.assistantInstructionRunes}
                        required readOnly={readOnly}
                        onChange={(event) => setDraft((d) => ({ ...d, instruction: event.target.value }))} />
                    </label>
                    {!readOnly && (
                      <button type="button" className="linkish" onClick={() => setDraft((d) => ({
                        ...d,
                        instruction: joinInstruction({ ...emptyInstructionParts(), role: d.instruction.trim() }),
                      }))}>
                        型に沿って書く（いまの文章は「役割」へ入ります）
                      </button>
                    )}
                  </>
                )}
                <p className="assistant-hint">
                  {draft.instruction.length} / {APP_LIMITS.assistantInstructionRunes}文字。
                  指示が長いほど、資料に使える文脈が減ります。
                </p>
              </div>
            </section>

            <section className="assistant-section">
              <h3>用語集</h3>
              <div className="assistant-panel">
                <p className="assistant-hint">
                  部内でしか通じない言い方を、資料での言い方へ読み替えるための表です
                  （「ペラ」→「プロペラ」など）。
                  <strong>事実を書く場所ではありません。</strong>
                  ここに書いたものには出典が付かないため、事実としては扱われません。
                </p>
                <ul className="glossary-list">
                  {(draft.glossary ?? []).map((entry, index) => (
                    <li key={index}>
                      <input
                        value={entry.term}
                        aria-label={`用語 ${index + 1}`}
                        placeholder="ペラ"
                        maxLength={APP_LIMITS.glossaryTermRunes}
                        readOnly={readOnly}
                        onChange={(event) => updateGlossary((draft.glossary ?? []).map((item, i) =>
                          i === index ? { ...item, term: event.target.value } : item))}
                      />
                      <input
                        value={entry.meaning}
                        aria-label={`意味 ${index + 1}`}
                        placeholder="プロペラ"
                        maxLength={APP_LIMITS.glossaryMeaningRunes}
                        readOnly={readOnly}
                        onChange={(event) => updateGlossary((draft.glossary ?? []).map((item, i) =>
                          i === index ? { ...item, meaning: event.target.value } : item))}
                      />
                      {!readOnly && (
                        <button type="button" aria-label={`${index + 1}行目を削除`} title="削除"
                          onClick={() => updateGlossary((draft.glossary ?? []).filter((_, i) => i !== index))}>
                          ×
                        </button>
                      )}
                    </li>
                  ))}
                </ul>
                {(draft.glossary ?? []).length === 0 && (
                  <p className="muted">まだ登録がありません。</p>
                )}
                {!readOnly && (
                  <button type="button" className="linkish"
                    disabled={(draft.glossary ?? []).length >= APP_LIMITS.glossaryEntries}
                    onClick={() => updateGlossary([...(draft.glossary ?? []), { term: "", meaning: "" }])}>
                    用語を追加（{(draft.glossary ?? []).length} / {APP_LIMITS.glossaryEntries}）
                  </button>
                )}
              </div>
            </section>
            </div>

            {error && <p className="assistant-error" role="alert">{error}</p>}
            <div className="assistant-actions">
              <button type="button" onClick={onClose}>戻る</button>
              {readOnly && assistant ? (
                <button type="button" className="primary" onClick={() => onDuplicate(assistant)}>複製して作る</button>
              ) : (
                <button type="submit" className="primary" disabled={!draft.name.trim() || !draft.instruction.trim()}>
                  {mode === "edit" ? "保存する" : "作成する"}
                </button>
              )}
            </div>
          </form>
  );
}
