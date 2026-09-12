#!/bin/sh
#
# 索引（data/index.json と data/toc.md）を Cloud Storage へ差し替える。
#
#   sh tools/publish-index.sh            差し替えて、差し替え日時も記録する
#   sh tools/publish-index.sh --no-apply 差し替えるだけ
#
# どちらでも本番への反映は同じ。動いているインスタンスがGCSの世代を見て
# 自分で読み直すため、再デプロイもリビジョンの入れ替えも要らない。
#
# 索引をコンテナイメージへ焼き込んでいた頃は、資料を1文字直すだけでも
# イメージの再ビルドとpushとデプロイが必要だった。ここを分けたので、
# **コードを変えていないなら、この1コマンドだけで済む。**
#
# 逆に、コードを変えたときは従来どおりイメージのデプロイが要る（docs/07）。
#
set -eu

bucket="gs://wasa-chat-index"
service="wasa-chat-api"
region="asia-northeast1"

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"

for file in data/index.json data/toc.md; do
  [ -f "$file" ] || { echo "$file がありません。python rebuild.py を先に実行してください" >&2; exit 1; }
done

# 壊れた索引を本番へ上げると、全部の質問に「資料が見つかりません」と
# 答え続ける状態になる。上げる前に、読める形かどうかを確認する
python3 - <<'PY' || exit 1
import json, sys
from pathlib import Path
try:
    pages = json.loads(Path("data/index.json").read_text(encoding="utf-8"))["pages"]
except Exception as error:
    sys.exit(f"index.json を読めません: {error}")
if len(pages) < 100:
    sys.exit(f"ページ数が{len(pages)}件しかありません。取得が途中で失敗していないか確認してください")
chunks = sum(len(p["chunks"]) for p in pages)
print(f"確認: {len(pages)}ページ / {chunks}チャンク")
PY

echo "差し替え先: $bucket"
gcloud storage cp data/index.json data/toc.md "$bucket/"

# 動いているインスタンスは INDEX_WATCH_SECONDS ごとにGCSの世代を見ており、
# 変わっていれば**再デプロイなしで読み直す**。差し替えたらそれで終わり。
#
# 以前はここで環境変数を1つ動かして新しいリビジョンへ入れ替えていた。
# 資料を直してから反映されるまでの待ちは、その手順が作っていた。
echo
echo "差し替えました。動いているインスタンスは次の更新確認で取り込みます。"

if [ "${1:-}" = "--no-apply" ]; then
  exit 0
fi

# 反映を待たずに確かめたいときのために、記録用の日時だけ更新する。
# **索引そのものの反映には要らない。** 管理画面へ「いつ差し替えたか」を出すため
echo
gcloud run services update "$service" \
  --region "$region" \
  --update-env-vars "INDEX_PUBLISHED_AT=$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
  --quiet

echo
curl -fsS "$(gcloud run services describe "$service" --region "$region" --format='value(status.url)')/health"
echo
