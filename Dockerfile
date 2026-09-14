# SPAとGoバイナリを1つのイメージにまとめる。
#
# フロントを Cloudflare Pages に置く構成でも、このイメージ単体で動く形を
# 残しておく。ローカルで通しの動作確認ができ、デプロイ先を1つに減らす選択も
# 取れるようにするため（docs/01-設計方針.md §7）。
#
# Wikiのデータ（data/index.json, data/toc.md）はリポジトリに含まれない。
# ビルド前にローカルで python ingest/build_index.py && python ingest/build_toc.py を実行すること。

# ---- 1. フロントエンドをビルド ----
FROM node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
# lockfileと一致しない依存関係を本番イメージへ混ぜない。
RUN npm ci --silent
COPY web/ ./
RUN npm run build

# ---- 2. Goバイナリをビルド ----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# CGO無効の静的バイナリにして、実行イメージを最小に保つ
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server .

# ---- 3. 実行イメージ ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
COPY --from=web /web/dist /app/web
# Wikiのインデックス。ビルドコンテキストに data/ が無いとここで失敗する
COPY data/index.json data/toc.md /app/data/
# 初期アシスタント。同じIDが登録済みなら上書きしないので、毎回入れて問題ない
COPY assistants/ /app/assistants/

ENV DATA_DIR=/app/data \
    SPA_DIR=/app/web \
    ASSISTANT_SEED_DIR=/app/assistants \
    PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
