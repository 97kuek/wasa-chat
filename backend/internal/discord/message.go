package discord

import (
	"fmt"
	"strings"
	"unicode/utf16"
)

// Source は回答に添える出典。サーバーが索引から組み立てたものだけを渡す。
type Source struct {
	Title string
	URL   string
}

// MessageLimit は1メッセージの上限。超えると Discord 側で弾かれる。
//
// ⚠️ **数え方は UTF-16 の符号単位である。** Discord は JavaScript の
// `String.length` で数えており、絵文字などBMP外の文字は**1文字で2単位**になる。
// Goの rune 数で見積もると、境界ぎりぎりの回答が弾かれる（Length を使うこと）。
const MessageLimit = 2000

// MaxMessagesPerAnswer は1つの回答を何通まで分けて送るか。
//
// **切り詰めずに続きを送る。** 出典URLは日本語タイトルのパーセントエンコードで
// 最長214文字あり、4件付くと出典欄だけで約1050文字になる。2000字から質問文を
// 引くと本文に440字しか残らず、**長い回答が毎回途中で切れていた**
// （2026-09-13に索引の実データで確認）。
//
// とはいえ無制限に流すとチャンネルが埋まるので、3通で止めて残りは省略する。
const MaxMessagesPerAnswer = 3

// Length は Discord の数え方（UTF-16の符号単位）で文字数を返す。
func Length(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// truncateTo は Discord の数え方で limit に収まるよう切る。
// **サロゲートペアの途中では切らない**（切ると壊れた文字が残る）。
func truncateTo(s string, limit int) string {
	if Length(s) <= limit {
		return s
	}
	used := 0
	for index, r := range s {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if used+width > limit {
			return s[:index]
		}
		used += width
	}
	return s
}

// splitMessages は本文を、Discordが受け取れる長さのメッセージへ分ける。
//
// **段落・行の切れ目で分ける。** 文字数だけで切ると、箇条書きの途中や
// Markdownの記号の途中で割れて、後ろのメッセージの描画まで崩れる。
//
// head は1通目の先頭（質問の引用など）、tail は最後の1通の末尾（出典）。
// どちらも分割しない。**出典は必ず最後まで残す**。
func splitMessages(head, body, tail string, limit, maxCount int) []string {
	body = strings.TrimSpace(body)
	// 1通に収まるなら分けない
	if Length(head)+Length(body)+Length(tail) <= limit {
		return []string{head + body + tail}
	}

	messages := make([]string, 0, maxCount)
	remaining := body
	for part := 0; part < maxCount && remaining != ""; part++ {
		prefix := ""
		if part == 0 {
			prefix = head
		}
		// 最後の1通には出典が入る。その分だけ本文を狭める
		suffix := ""
		last := part == maxCount-1
		room := limit - Length(prefix)
		if last {
			suffix = tail
			room -= Length(tail) + Length(truncatedMark)
		}
		if room <= 0 {
			break
		}

		chunk, rest := cutAtBoundary(remaining, room)
		remaining = rest
		if last && remaining != "" {
			chunk += truncatedMark
		}
		messages = append(messages, prefix+chunk+suffix)
	}
	// 途中で本文が尽きたら、出典はそのまま最後の1通へ足す
	if remaining == "" && tail != "" && !strings.HasSuffix(messages[len(messages)-1], tail) {
		if index := len(messages) - 1; Length(messages[index])+Length(tail) <= limit {
			messages[index] += tail
		} else {
			messages = append(messages, strings.TrimPrefix(tail, "\n\n"))
		}
	}
	return messages
}

// cutAtBoundary は limit に収まるところまでを、段落→行→文字の順で切る。
func cutAtBoundary(text string, limit int) (chunk, rest string) {
	if Length(text) <= limit {
		return text, ""
	}
	head := truncateTo(text, limit)
	// 段落の切れ目を優先する。無ければ行、それも無ければそのまま
	for _, separator := range []string{"\n\n", "\n"} {
		if index := strings.LastIndex(head, separator); index > 0 {
			return strings.TrimRight(head[:index], " \t\n"), strings.TrimLeft(text[index:], " \t\n")
		}
	}
	return head, text[len(head):]
}

// FormatAnswer は Discord へ出すメッセージを組み立てる。**複数通になり得る。**
//
// **出典は必ず添える。** 画面にはカードがあるが、Discordには無い。
// 「どこを開けば確かめられるか」が無い回答は、引き継ぎ資料の道具として使えない。
func FormatAnswer(question, answer string, sources []Source) []string {
	var tail strings.Builder
	if len(sources) > 0 {
		// 行頭の `- ` でDiscordが実際の箇条書きとして描画する。
		// 中点（・）はただの文字で、字下げも点も付かない
		tail.WriteString("\n\n**参照**\n")
		for _, source := range sources {
			if source.URL != "" {
				fmt.Fprintf(&tail, "- [%s](%s)\n", source.Title, source.URL)
			} else {
				fmt.Fprintf(&tail, "- %s\n", source.Title)
			}
		}
	}
	head := fmt.Sprintf("> %s\n\n", truncateTo(strings.TrimSpace(question), questionQuoteLimit))
	return splitMessages(head, stripCitations(answer), tail.String(), MessageLimit, MaxMessagesPerAnswer)
}

// questionQuoteLimit は引用する質問文の長さ。質問が長いほど本文が狭くなるので、
// 引用のほうを先に諦める。
const questionQuoteLimit = 300

// FormatRecap は要約・ToDoの本文を組み立てる。**複数通になり得る。**
//
// **読んだ範囲を先頭に書く。** 会話ログを上流へ送る機能なので、「何が送られたか」が
// 後から誰にでも分かるようにしておく。チャンネルに残る文言そのものが説明になる
// （docs/09 A-9）。出典は無い（根拠は会話ログそのもの）。
func FormatRecap(scope, body string) []string {
	head := fmt.Sprintf("-# %s\n\n", scope)
	return splitMessages(head, fixCheckboxes(body), "", MessageLimit, MaxMessagesPerAnswer)
}

// RecapScope は読んだ範囲の説明文。
//
// **どこを何件読んだかを必ず書く。** 会話ログを上流へ送る機能なので、
// チャンネルに残るこの1行が、そのまま「何が送られたか」の説明になる。
func RecapScope(where string, days, messages, speakers int) string {
	return fmt.Sprintf("%sの過去%d日ぶん・%d件の発言（%d人）を読みました。WASAの引き継ぎ資料は参照していません",
		where, days, messages, speakers)
}

const truncatedMark = "…（長いため省略しました）"
