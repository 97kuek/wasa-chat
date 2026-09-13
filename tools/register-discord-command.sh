#!/bin/sh
#
# Discord へスラッシュコマンドを登録する。
#
#   DISCORD_APP_ID=... DISCORD_BOT_TOKEN=... sh tools/register-discord-command.sh
#
# **1回やれば済む。** コマンドの名前や説明を変えたときだけ実行し直す。
# 回答そのものは Cloud Run が受けるので、このスクリプトは運用に要らない。
#
# 登録するのは3つ。
#
#   /wasa <質問> [アシスタント]  引き継ぎ資料に質問する（索引を読む。出典が付く）
#
# メッセージを右クリック →「アプリ」から呼ぶものも2つ登録する。
#
#   資料に聞く       その発言の内容で引き継ぎ資料を調べる
#   ここまでを要約    その発言までの流れを要約する
#
# ⚠️ **リアクションでは作れない。** リアクション（MESSAGE_REACTION_ADD）は
# Gateway（WebSocketの常時接続）でしか届かず、常時接続は無料枠の約7%しか
# カバーできない（月8,000円規模）。右クリックのメッセージコマンドはHTTPで届くので、
# ゼロスケールのまま同じことができる。type: 3 がその指定。
# **description は付けられない**（メッセージコマンドは名前だけ）。
#   /要約 [期間] [範囲]  最近の会話を要約する（索引を読まない）
#   /todo [期間] [範囲]  最近の会話からToDoを抜き出す（同上）
#
# 期間は日数（既定7日・最大365日）。範囲は入力中に候補が出ます
# （「公開チャンネル全部」＋そのサーバーの公開チャンネル。打つと絞られる）。
#
# ⚠️ **非公開チャンネルは候補に出さず、IDを手で打たれても読みません。** ボットが
# 見えるチャンネルと、コマンドを打った人が見えるチャンネルは同じではないためです
# （docs/09 A-9）。
#
# ⚠️ **範囲の補完は autocomplete です。** 固定の choices と違い、打つたびに
# Cloud Run へ問い合わせが来ます。3秒以内に返せないと候補が出ないため、
# サーバー側はチャンネル一覧を1分だけ覚えています。
#
# ⚠️ **サブコマンドにしていない理由。** Discordは「サブコマンドを持つコマンド」に
# 普通のオプションを混ぜられない。`/wasa 要約` の形にすると、質問のときも
# `/wasa 質問 <text>` と打つ必要が出る。**普段の質問を一番短く打てること**を
# 優先して、別コマンドとして登録する（2026-09-13にPMが判断）。
#
# ⚠️ **/要約 と /todo はBOTトークンを本番でも要求する。** 過去ログの取得
# （GET /channels/{id}/messages）はBOTトークンでしか認証できない。質問だけなら
# 不要なので、要約を使わない運用なら Cloud Run に DISCORD_BOT_TOKEN を置かなくてよい。
# 置く場合は Secret Manager 経由にすること（docs/09 A-8）。
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

# PUT で一括登録する。POST を繰り返すと、名前を変えたときに古いコマンドが残る
curl -fsS -X PUT "https://discord.com/api/v10/${scope}" \
  -H "Authorization: Bot ${DISCORD_BOT_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '[
    {
      "name": "wasa",
      "description": "WASAの引き継ぎ資料に質問します",
      "options": [
        {
          "type": 3,
          "name": "質問",
          "description": "例: 荷重試験の申請方法を教えてください",
          "required": true
        },
        {
          "type": 3,
          "name": "アシスタント",
          "description": "口調と参照範囲を変えます。打つと候補が絞られます",
          "required": false,
          "autocomplete": true
        }
      ]
    },
    {
      "name": "要約",
      "description": "最近の会話を要約します",
      "options": [
        {
          "type": 4,
          "name": "期間",
          "description": "何日前まで遡るか（既定7日・最大365日）",
          "required": false,
          "min_value": 1,
          "max_value": 365
        },
        {
          "type": 3,
          "name": "範囲",
          "description": "チャンネル名を打つと候補が絞られます。既定はこのチャンネル",
          "required": false,
          "autocomplete": true
        }
      ]
    },
    {
      "name": "資料に聞く",
      "type": 3
    },
    {
      "name": "ここまでを要約",
      "type": 3
    },
    {
      "name": "todo",
      "description": "最近の会話からToDoを抜き出します",
      "options": [
        {
          "type": 4,
          "name": "期間",
          "description": "何日前まで遡るか（既定7日・最大365日）",
          "required": false,
          "min_value": 1,
          "max_value": 365
        },
        {
          "type": 3,
          "name": "範囲",
          "description": "チャンネル名を打つと候補が絞られます。既定はこのチャンネル",
          "required": false,
          "autocomplete": true
        }
      ]
    }
  ]' >/dev/null
echo
echo "登録しました。Discordで /wasa /要約 /todo と打つと出てきます。"
