package server

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/97kuek/wasa-chat/backend/internal/llm"
)

// 添付画像の受け入れ条件。
//
// 画面側は長辺768pxのJPEGへ落としてから送るが、**その申告は信じない。**
// 上限も形式も、届いたバイト列を見てサーバーが決める。
const (
	maxAttachments    = 1          // 当面1枚。複数枚の使い道が実際に出てから増やす
	maxAttachmentSize = 400 * 1024 // 縮小後のJPEGは実測で100KB前後に収まる
	dataURIPrefix     = "data:"
)

var errBadAttachment = errors.New("添付が不正です")

// 先頭バイトで実際の形式を見分ける。拡張子もMIMEの申告も当てにしない。
var imageSignatures = []struct {
	mediaType string
	prefix    []byte
	// WebPは "RIFF????WEBP" なので、先頭一致だけでは足りない
	at12 []byte
}{
	{mediaType: "image/jpeg", prefix: []byte{0xFF, 0xD8, 0xFF}},
	{mediaType: "image/png", prefix: []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}},
	{mediaType: "image/webp", prefix: []byte("RIFF"), at12: []byte("WEBP")},
}

func sniffImage(data []byte) (string, bool) {
	for _, signature := range imageSignatures {
		if len(data) < len(signature.prefix) {
			continue
		}
		if string(data[:len(signature.prefix)]) != string(signature.prefix) {
			continue
		}
		if signature.at12 != nil {
			if len(data) < 12+len(signature.at12) {
				continue
			}
			if string(data[8:12]) != string(signature.at12) {
				continue
			}
		}
		return signature.mediaType, true
	}
	return "", false
}

// parseAttachments は data URI の配列を検証して画像へ変換する。
//
// 判定の順序に意味がある。**大きさを先に見てから復号する。** 逆にすると、
// 巨大なbase64を一度メモリへ展開してから捨てることになる。
func parseAttachments(values []string) ([]llm.Image, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > maxAttachments {
		return nil, fmt.Errorf("%w: 添付できるのは%d枚までです", errBadAttachment, maxAttachments)
	}

	images := make([]llm.Image, 0, len(values))
	for _, value := range values {
		if !strings.HasPrefix(value, dataURIPrefix) {
			return nil, fmt.Errorf("%w: 形式が不正です", errBadAttachment)
		}
		_, encoded, found := strings.Cut(value, ",")
		if !found {
			return nil, fmt.Errorf("%w: 形式が不正です", errBadAttachment)
		}
		// base64は元の4/3倍になる。復号前にここで弾く
		if len(encoded) > maxAttachmentSize*4/3+8 {
			return nil, fmt.Errorf("%w: 画像が大きすぎます", errBadAttachment)
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("%w: 画像を読み取れませんでした", errBadAttachment)
		}
		if len(data) > maxAttachmentSize {
			return nil, fmt.Errorf("%w: 画像が大きすぎます", errBadAttachment)
		}
		// 申告されたMIMEではなく、中身から判定した形式を使う
		mediaType, ok := sniffImage(data)
		if !ok {
			return nil, fmt.Errorf("%w: 画像はJPEG・PNG・WebPだけ添付できます", errBadAttachment)
		}
		images = append(images, llm.Image{MediaType: mediaType, Data: data})
	}
	return images, nil
}
