# CLAUDE.md

**このリポジトリの作業指示は [AGENTS.md](AGENTS.md) にある。作業前に必ず読むこと。**
設計判断の根拠は [docs/01-設計方針.md](docs/01-設計方針.md)、実測値は [docs/08-測定記録.md](docs/08-測定記録.md)。

## よく使うコマンド

```bash
source .venv/bin/activate
python ingest/dump_wiki.py             # Wiki取得   → dump/pages.jsonl
python ingest/build_index.py           # 整形       → data/index.json
python ingest/build_toc.py             # 目次生成   → data/toc.md
python eval/retrieval_eval.py   # 検索精度の測定

OLLAMA_FLASH_ATTENTION=1 OLLAMA_KV_CACHE_TYPE=q8_0 OLLAMA_CONTEXT_LENGTH=32768 ollama serve
```

Ollamaの `OLLAMA_CONTEXT_LENGTH` を省略しない。既定の4096では目次（1〜1.5万トークン）が
黙って切り捨てられる。

---

## 進め方の原則

- **測ってから足す。** 推測で層を増やさない。段階（Phase）は docs/01 §2 にある
- ドキュメント・コメント・コミットメッセージは**日本語**
- コメントには「なぜそうしたか」を、実測値とともに書く
- 測定したら docs/08 に「数字 / 読み取れたこと / 判断を変えた点」の3点セットで記録する
- 確定済みの設計判断（グラフRAG不採用、BLEU/ROUGE不採用など）は AGENTS.md にある。
  蒸し返さない。異論は根拠に反論する形で出す
