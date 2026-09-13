package index

import (
	"strings"
	"testing"
)

// ⚠️ **共有ドライブの節を固定プレフィックスに入れない。** 目次はプロンプトの
// 先頭に固定してキャッシュを効かせている。Driveの節を混ぜると、ファイルが
// 1つ増減するたびに前半のバイト列まで変わってキャッシュが外れ、
// Driveをオフにしている会話へファイル名も漏れる（2026-09-13のCodex指摘）
func TestSplitDriveTOC(t *testing.T) {
	toc := "# WASA 資料の目次\n\n## 引き継ぎWiki（部内限定）全2ページ\n\n- 荷重試験\n" +
		"\n## 共有ドライブ（部内限定）全3ファイル\n\n- 40代 総会議事録\n"

	fixed, drive := splitDriveTOC(toc)
	if strings.Contains(fixed, "共有ドライブ") || strings.Contains(fixed, "総会議事録") {
		t.Fatalf("固定部分に共有ドライブが残っている:\n%s", fixed)
	}
	if !strings.Contains(fixed, "荷重試験") {
		t.Fatalf("固定部分を削りすぎている:\n%s", fixed)
	}
	if !strings.Contains(drive, "総会議事録") {
		t.Fatalf("共有ドライブの節を取り出せていない:\n%s", drive)
	}

	// 共有ドライブが無い索引では、目次をそのまま使う
	plain := "# WASA 資料の目次\n\n## 引き継ぎWiki（部内限定）全2ページ\n"
	if fixed, drive := splitDriveTOC(plain); fixed != plain || drive != "" {
		t.Fatalf("共有ドライブが無いのに切っている: %q / %q", fixed, drive)
	}
}
