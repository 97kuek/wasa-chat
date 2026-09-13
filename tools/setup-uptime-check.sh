#!/bin/sh
#
# `/health` の外形監視を作る（docs/09 B-1・B-2）。
#
#   ALERT_EMAIL=you@example.com sh tools/setup-uptime-check.sh
#
# **1回やれば済む。** 監視の設定を変えたいときだけ実行し直す。
#
# なぜ要るか
# ----------
# 索引が読めないとサーバーは起動しない（意図的な設計）。索引の読み直しに失敗すると
# `/health` は 503 を返す。どちらも**誰も見ていなければ気づけない**。自動の検知手段が
# 利用者からの報告だけ、という状態を終わらせる。
#
# ⚠️ **変化があったときだけ通知が出る形にする。** 毎回通知が来ると、そのうち誰も
# 見なくなる（週次の更新確認と同じ方針。docs/08 M32）。
#
# 費用
# ----
# Cloud Monitoring の外形監視は**1プロジェクトあたり月100万回まで無料**。
# 5分間隔なら月約8,600回で、無料枠の1%も使わない。「API以外は¥0」と両立する。
set -eu

: "${ALERT_EMAIL:?ALERT_EMAIL を設定してください（異常時の通知先）}"
PROJECT="${PROJECT:-gen-lang-client-0700551520}"
HOST="${HOST:-wasa-chat-api-lbd2gmlkxq-an.a.run.app}"

gcloud services enable monitoring.googleapis.com --project "$PROJECT"

echo "--- 外形監視を作る ---"
# ⚠️ **200以外を異常とみなす。** /health は索引の読み直しに失敗すると 503 を返す。
# 「つながるか」だけを見ると、古い索引で答え続けている状態を見逃す（docs/09 B-2）。
# 地点は3つ以上を求められる（1つだけでは作れない）
gcloud monitoring uptime create "WASA Chat /health" \
  --project "$PROJECT" --resource-type=uptime-url \
  --resource-labels="host=$HOST,project_id=$PROJECT" \
  --path="/health" --port=443 --protocol=https \
  --period=5 --timeout=10 --status-classes=2xx \
  --regions=asia-pacific,usa-oregon,europe || true

CHECK_ID=$(gcloud monitoring uptime list-configs --project "$PROJECT" \
  --filter="displayName='WASA Chat /health'" --format="value(name)" | head -1 | xargs basename)
echo "  外形監視ID: $CHECK_ID"

# ⚠️ **通知先とアラートは gcloud の alpha/beta が要る。** 入っていない環境でも
# 動くよう、REST APIを直接叩く（gcloud のコンポーネント導入を運用条件にしない）
echo "--- 通知先とアラートを作る ---"
TOKEN=$(gcloud auth print-access-token)
CHANNEL=$(curl -s -X POST \
  "https://monitoring.googleapis.com/v3/projects/$PROJECT/notificationChannels" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"type\":\"email\",\"displayName\":\"WASA Chat 異常通知\",\"labels\":{\"email_address\":\"$ALERT_EMAIL\"},\"enabled\":true}" \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['name'])")
echo "  通知先: $CHANNEL"

# ⚠️ **1回の失敗で鳴らさない。** ゼロスケールからの起動待ちで誤報が出る。
# 10分続けて失敗したときだけ。直ったら自動で閉じる（鳴りっぱなしにしない）
curl -s -X POST "https://monitoring.googleapis.com/v3/projects/$PROJECT/alertPolicies" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{
    \"displayName\": \"WASA Chat が応答しない\",
    \"documentation\": {\"content\": \"/health が続けて失敗しました。索引が読めず起動に失敗したか、索引の読み直しに失敗して 503 を返している可能性があります。docs/09 B-1・B-2 を参照してください。\", \"mimeType\": \"text/markdown\"},
    \"conditions\": [{
      \"displayName\": \"/health が10分間失敗し続けている\",
      \"conditionThreshold\": {
        \"filter\": \"resource.type=\\\"uptime_url\\\" AND metric.type=\\\"monitoring.googleapis.com/uptime_check/check_passed\\\" AND metric.labels.check_id=\\\"$CHECK_ID\\\"\",
        \"aggregations\": [{\"alignmentPeriod\": \"300s\", \"perSeriesAligner\": \"ALIGN_FRACTION_TRUE\", \"crossSeriesReducer\": \"REDUCE_MEAN\", \"groupByFields\": [\"resource.label.host\"]}],
        \"comparison\": \"COMPARISON_LT\", \"thresholdValue\": 0.3,
        \"duration\": \"600s\", \"trigger\": {\"count\": 1}
      }
    }],
    \"combiner\": \"OR\",
    \"notificationChannels\": [\"$CHANNEL\"],
    \"alertStrategy\": {\"autoClose\": \"1800s\"}
  }" | python3 -c "import json,sys; d=json.load(sys.stdin); print('  アラート:', d.get('name', d))"

echo
echo "できました。通知先のメールに確認リンクが届くので、開いて有効化してください。"
