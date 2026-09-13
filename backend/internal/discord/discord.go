// Package discord は Discord のスラッシュコマンドを受ける。
//
// # なぜ常時接続のボットにしないのか
//
// メンションを受け取るには Gateway（WebSocketの常時接続）が要る。つまり
// `min-instances=0` が使えず、**1か月259万秒のうち無料枠は18万秒＝約7%しか
// カバーしない**。CPUを常に割り当てる必要もあるため、月8,000円規模になる。
// 「API以外は¥0」（docs/01 §7）と両立しない。
//
// スラッシュコマンドは Discord が**呼ばれたときだけ**HTTPでPOSTしてくるので、
// いまのゼロスケールのまま動く。
//
// # 3秒の制約
//
// Discord は**3秒以内の応答**を求める。回答は10〜30秒かかるので、
// まず「考えています」（deferred）を返し、**15分以内に追いかけて書き換える**。
// これは Discord が用意している標準のやり方である。
//
// # 出典の保証はここでも守る
//
// 画面では出典カードをサーバーが索引から組み立てている。Discordには
// カードが無いので**サーバーが文字列として組み立てて添える**。
// モデルに書かせるのは資料番号だけ、という性質は変えない（docs/02）。
package discord

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// 対話の種類。Discordの Interaction Type と対応する。
const (
	TypePing               = 1
	TypeApplicationCommand = 2
	// TypeAutocomplete は、オプションを打っている最中に候補を求めてくるもの。
	// **3秒以内に同期で返す。** 「考えています」で先延ばしできない
	TypeAutocomplete = 4
)

// 応答の種類。
const (
	// ResponsePong は疎通確認への返事。**これを返せないとエンドポイントを登録できない**
	ResponsePong = 1
	// ResponseDeferred は「考えています」。3秒の制約をこれで越える
	ResponseDeferred = 5
	// ResponseAutocomplete は入力中の候補一覧
	ResponseAutocomplete = 8
)

// AutocompleteLimit はDiscordが受け取る候補の最大件数。超えると弾かれる。
const AutocompleteLimit = 25

// 登録するコマンド名。tools/register-discord-command.sh と揃えること。
//
// ⚠️ **サブコマンドにはしない。** Discordは「サブコマンドを持つコマンド」に
// 普通のオプションを混ぜられない。`/wasa 要約` の形にすると、質問のときも
// `/wasa 質問 <text>` と打つ必要が出る。**普段の質問を一番短く打てること**を
// 優先して、要約とToDoは別のコマンドとして登録する（2026-09-13にPMが判断）。
const (
	CommandAsk     = "wasa"
	CommandSummary = "要約"
	CommandTodo    = "todo"
)

// CommandName は呼ばれたコマンドの名前を返す。
func (i *Interaction) CommandName() string { return i.Data.Name }

type Interaction struct {
	Type int    `json:"type"`
	ID   string `json:"id"`
	// Token は追いかけて書き換えるときに使う。**15分で失効する**
	Token string `json:"token"`
	Data  struct {
		Name    string   `json:"name"`
		Options []Option `json:"options"`
	} `json:"data"`
	// サーバー内では member、DMでは user に入る
	Member struct {
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	} `json:"member"`
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
}

// UserID は誰が呼んだかを返す。**利用回数を個人ごとに数えるために要る。**
// サーバー内かDMかで入る場所が違うので、ここで吸収する。
func (i *Interaction) UserID() string {
	if i.Member.User.ID != "" {
		return i.Member.User.ID
	}
	return i.User.ID
}

func (i *Interaction) Username() string {
	if i.Member.User.Username != "" {
		return i.Member.User.Username
	}
	return i.User.Username
}

// Option はコマンドへ渡された引数。
//
// **Value を string で受けてはいけない。** 整数のオプション（期間の日数）は
// JSONで `"値": 7` と数値のまま届くので、string で受けると丸ごと解釈に失敗し、
// **同じリクエストに入っている他のオプションまで消える**。生のまま受けて、
// 読む側で型を決める。
type Option struct {
	Name  string          `json:"name"`
	Type  int             `json:"type"`
	Value json.RawMessage `json:"value"`
	// Focused は補完のとき、いまどのオプションを打っているかを示す
	Focused bool `json:"focused"`
}

// オプションの型。Discordの Application Command Option Type と対応する。
const (
	OptionTypeString  = 3
	OptionTypeInteger = 4
)

func (o Option) text() string {
	var value string
	if err := json.Unmarshal(o.Value, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func (o Option) number() (int, bool) {
	var value int
	if err := json.Unmarshal(o.Value, &value); err != nil {
		return 0, false
	}
	return value, true
}

// Question は最初の文字列オプションを返す。
func (i *Interaction) Question() string {
	for _, option := range i.Data.Options {
		if value := option.text(); value != "" {
			return value
		}
	}
	return ""
}

// OptionText は名前を指定して文字列オプションを読む。未指定なら空文字。
func (i *Interaction) OptionText(name string) string {
	for _, option := range i.Data.Options {
		if option.Name == name {
			return option.text()
		}
	}
	return ""
}

// Focused は補完のとき、打っている最中のオプション名と入力途中の文字を返す。
func (i *Interaction) Focused() (name, typed string) {
	for _, option := range i.Data.Options {
		if option.Focused {
			return option.Name, option.text()
		}
	}
	return "", ""
}

// OptionInt は名前を指定して整数オプションを読む。未指定なら fallback。
func (i *Interaction) OptionInt(name string, fallback int) int {
	for _, option := range i.Data.Options {
		if option.Name != name {
			continue
		}
		if value, ok := option.number(); ok {
			return value
		}
	}
	return fallback
}

// Verify は Discord の署名を確かめる。
//
// ⚠️ **これを通さないリクエストを処理しないこと。** エンドポイントは公開されるので、
// 署名を見なければ誰でも叩けて、無料枠を好きなだけ消費させられる。
// Discord 側も、登録時に**わざと壊した署名**を送ってきて、拒否できるかを試す。
//
// 署名の対象は「タイムスタンプ＋本文の生バイト」なので、**JSONへ解釈する前に
// 生のまま**渡すこと。解釈してから組み立て直すと、空白の違いで一致しなくなる。
func Verify(publicKeyHex, signature, timestamp string, body []byte) bool {
	key, err := hex.DecodeString(publicKeyHex)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(key), append([]byte(timestamp), body...), sig)
}

// citationPattern は本文に付く資料番号 [1] や [1][3]。
// `[見出し](URL)` を巻き込まないよう、直後が `(` のものは対象にしない。
var citationPattern = regexp.MustCompile(`(\[\d+\])+(?:\()?`)

// stripCitations は本文から資料番号を外す。
//
// 画面では [1] が**押せる出典マーク**になるが、Discordには押す先が無く、
// ただの記号として残る。実際に使って「見にくすぎる」という指摘が出た（2026-09-13）。
//
// ⚠️ **プロンプト側で「書くな」とは頼まない。** 根拠を明示させる規則そのものが
// 回答の正確さを支えている（docs/02）。書かせたうえで、**表示の都合だけを
// ここで落とす**。出典の一覧は下に残るので、確かめる道は閉じない。
func stripCitations(answer string) string {
	cleaned := citationPattern.ReplaceAllStringFunc(answer, func(match string) string {
		// 直後が `(` ならMarkdownのリンクなので触らない
		if strings.HasSuffix(match, "(") {
			return match
		}
		return ""
	})
	// 番号を抜いた跡に残る行末の空白を落とす
	lines := strings.Split(cleaned, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

// オプション名。tools/register-discord-command.sh と揃えること。
const (
	OptionDays  = "期間"
	OptionScope = "範囲"
	// OptionAssistant は `/wasa` で口調・参照範囲を選ぶもの。
	// 画面と同じアシスタントを、Discord向けの書き方の指定と重ねて使う
	OptionAssistant = "アシスタント"
	// ScopeAllChannels は「範囲」オプションでサーバー横断を選んだときの値
	ScopeAllChannels = "all"
)

// checkboxPattern は Markdown のタスクリスト記法。
var checkboxPattern = regexp.MustCompile(`(?m)^(\s*)[-*]\s+\[( |x|X)\]\s*`)

// fixCheckboxes は `- [ ]` を `- ☐` に直す。
//
// ⚠️ **Discordはタスクリストを描画しない。** `- [ ]` と書くと、箱ではなく
// 「[ ]」という文字がそのまま出る（2026-09-13に実機で確認）。プロンプトでも
// 禁じているが、モデルは慣れた書き方へ戻りやすいので、ここでも直す。
func fixCheckboxes(body string) string {
	return checkboxPattern.ReplaceAllStringFunc(body, func(match string) string {
		indent := match[:len(match)-len(strings.TrimLeft(match, " \t"))]
		if strings.ContainsAny(match, "xX") {
			return indent + "- ☑ "
		}
		return indent + "- ☐ "
	})
}

// FollowUp は「考えています」を書き換えるための中身。
type FollowUp struct {
	Content string `json:"content"`
	// Flags に EphemeralFlag を立てると、本人だけに見える
	Flags int `json:"flags,omitempty"`
	// 誰かへの通知を発生させない。質問文に @everyone が入っていても波及させない
	AllowedMentions struct {
		Parse []string `json:"parse"`
	} `json:"allowed_mentions"`
}

func NewFollowUp(content string) FollowUp {
	follow := FollowUp{Content: content}
	follow.AllowedMentions.Parse = []string{}
	return follow
}

// EphemeralFlag は「本人だけに見える」メッセージの印。
//
// 追いかけて送るメッセージ（follow-up）は、最初の応答が公開でも
// **本人だけに見える形にできる**。回数切れや操作ミスの知らせを
// チャンネル全員に見せないために使う。
const EphemeralFlag = 64

// NewEphemeral は本人だけに見える知らせを作る。
func NewEphemeral(content string) FollowUp {
	follow := NewFollowUp(content)
	follow.Flags = EphemeralFlag
	return follow
}

// FollowUpURL は最初の応答（「考えています」）を書き換える先。
// トークンが認証を兼ねるので鍵は要らない。
func FollowUpURL(applicationID, token string) string {
	return fmt.Sprintf("%s/webhooks/%s/%s/messages/@original", apiBase, applicationID, token)
}

// ExtraMessageURL は2通目以降を投稿する先。回答が2000字に収まらないときに使う。
func ExtraMessageURL(applicationID, token string) string {
	return fmt.Sprintf("%s/webhooks/%s/%s", apiBase, applicationID, token)
}

// DeleteOriginalURL は「考えています」を消す先。
//
// **失敗を伝えるときに使う。** 置いたままにすると「考えています」が
// チャンネルに残り続ける。消してから、本人だけに見える形で理由を出す。
func DeleteOriginalURL(applicationID, token string) string {
	return FollowUpURL(applicationID, token)
}

// Deferred は「考えています」の応答。
func Deferred() []byte {
	body, _ := json.Marshal(map[string]any{"type": ResponseDeferred})
	return body
}

// Pong は疎通確認への応答。
func Pong() []byte {
	body, _ := json.Marshal(map[string]any{"type": ResponsePong})
	return body
}

// Choice は補完の候補1件。
type Choice struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Autocomplete は入力中の候補一覧を返す応答。
//
// **25件を超えるとDiscordに弾かれる**ので、ここで必ず切る。
func Autocomplete(choices []Choice) []byte {
	if len(choices) > AutocompleteLimit {
		choices = choices[:AutocompleteLimit]
	}
	if choices == nil {
		choices = []Choice{}
	}
	body, _ := json.Marshal(map[string]any{
		"type": ResponseAutocomplete,
		"data": map[string]any{"choices": choices},
	})
	return body
}
