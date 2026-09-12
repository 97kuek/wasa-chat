#!/bin/sh
#
# Discord へスラッシュコマンド `/wasa` を登録する。
#
#   DISCORD_APP_ID=... DISCORD_BOT_TOKEN=... sh tools/register-discord-command.sh
#
# **1回やれば済む。** コマンドの名前や説明を変えたときだけ実行し直す。
# 回答そのものは Cloud Run が受けるので、このスクリプトは運用に要らない。
#
# ⚠️ **BOTトークンはここでしか使わない。** 回答を書き換えるときは、対話ごとの
# トークン（15分で失効）が認証を兼ねるので、本番にBOTトークンを置く必要はない。
# 置かなければ漏れない。
set -eu

: "${DISCORD_APP_ID:?DISCORD_APP_ID を設定してください}"
: "${DISCORD_BOT_TOKEN:?DISCORD_BOT_TOKEN を設定してください}"

# guild を指定すると、そのサーバーだけへ即時反映される。
# 省略すると全体へ登録され、反映に最大1時間かかる
scope="applications/${DISCORD_APP_ID}/commands"
if [ -n "${DISCORD_GUILD_ID:-}" ]; then
  scope="applications/${DISCORD_APP_ID}/guilds/${DISCORD_GUILD_ID}/commands"
  echo "登録先: サーバー ${DISCORD_GUILD_ID}（即時反映）"
else
  echo "登録先: アプリ全体（反映に最大1時間）"
fi

curl -fsS -X POST "https://discord.com/api/v10/${scope}" \
  -H "Authorization: Bot ${DISCORD_BOT_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "wasa",
    "description": "WASAの引き継ぎ資料に質問します",
    "options": [
      {
        "type": 3,
        "name": "質問",
        "description": "例: 荷重試験の申請方法を教えてください",
        "required": true
      }
    ]
  }'
echo
echo "登録しました。Discordで /wasa と打つと出てきます。"
