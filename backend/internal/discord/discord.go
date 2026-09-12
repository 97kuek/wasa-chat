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
	"strings"
)

// 対話の種類。Discordの Interaction Type と対応する。
const (
	TypePing               = 1
	TypeApplicationCommand = 2
)

// 応答の種類。
const (
	// ResponsePong は疎通確認への返事。**これを返せないとエンドポイントを登録できない**
	ResponsePong = 1
	// ResponseDeferred は「考えています」。3秒の制約をこれで越える
	ResponseDeferred = 5
)

// MessageLimit は1メッセージの上限。超えると Discord 側で弾かれる。
const MessageLimit = 2000

type Interaction struct {
	Type int    `json:"type"`
	ID   string `json:"id"`
	// Token は追いかけて書き換えるときに使う。**15分で失効する**
	Token string `json:"token"`
	Data  struct {
		Name    string `json:"name"`
		Options []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"options"`
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

// Question は最初の文字列オプションを返す。
func (i *Interaction) Question() string {
	for _, option := range i.Data.Options {
		if value := strings.TrimSpace(option.Value); value != "" {
			return value
		}
	}
	return ""
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

// Source は回答に添える出典。サーバーが索引から組み立てたものだけを渡す。
type Source struct {
	Title string
	URL   string
}

// FormatAnswer は Discord へ出す本文を組み立てる。
//
// **出典は必ず添える。** 画面にはカードがあるが、Discordには無い。
// 「どこを開けば確かめられるか」が無い回答は、引き継ぎ資料の道具として使えない。
//
// 2000字を超えるときは**本文を削り、出典は残す**。出典を落とすと、
// 長い回答ほど根拠が分からなくなるという逆の挙動になる。
func FormatAnswer(question, answer string, sources []Source) string {
	var tail strings.Builder
	if len(sources) > 0 {
		tail.WriteString("\n\n**参照**\n")
		for _, source := range sources {
			if source.URL != "" {
				fmt.Fprintf(&tail, "・[%s](%s)\n", source.Title, source.URL)
			} else {
				fmt.Fprintf(&tail, "・%s\n", source.Title)
			}
		}
	}
	head := fmt.Sprintf("> %s\n\n", strings.TrimSpace(question))
	suffix := tail.String()

	room := MessageLimit - len([]rune(head)) - len([]rune(suffix)) - len([]rune(truncatedMark))
	body := strings.TrimSpace(answer)
	if room > 0 && len([]rune(body)) > room {
		body = string([]rune(body)[:room]) + truncatedMark
	}
	return head + body + suffix
}

const truncatedMark = "…（長いため省略しました）"

// FollowUp は「考えています」を書き換えるための中身。
type FollowUp struct {
	Content string `json:"content"`
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

// FollowUpURL は最初の応答を書き換える先。トークンが認証を兼ねるので鍵は要らない。
func FollowUpURL(applicationID, token string) string {
	return fmt.Sprintf("https://discord.com/api/v10/webhooks/%s/%s/messages/@original",
		applicationID, token)
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
