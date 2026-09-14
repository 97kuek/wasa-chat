"""LLMクライアント。パイプライン側（pipeline.py）は具象実装に依存しない。

環境変数 LLM_PROVIDER で切り替える。

  ollama    ローカル（既定）。データを外部に出さない
  gemini    Google Gemini。無料枠あり
  compat    OpenAI互換API。Grok(xAI) / Groq / Mistral / OpenRouter などが該当

⚠️ gemini / compat の無料枠は、送信内容がモデルの学習に使われる場合がある。
   対象は非公開Wikiの本文である。個人情報（メール・生年月日・電話番号）は
   ingest/build_index.py でマスク済みだが、氏名・役職・契約情報は本文に残っている。
"""

from __future__ import annotations

import json
import os
import re
import time
import urllib.error
import urllib.request
from typing import Any

NUM_CTX = 32768

# think=false を指定しても qwen3 は思考を本文に混ぜてくることがある。
# Gemini や Claude では思考が構造として分離されるため、これはローカル向けの処置。
THINK_BLOCK = re.compile(r"(?is)<think>.*?</think>")
THINK_TAIL = re.compile(r"(?is)^.*?</think>\s*")


def strip_thinking(text: str) -> str:
    text = THINK_BLOCK.sub("", text)
    if "</think>" in text.lower():  # 開始タグが欠けた壊れ方をすることがある
        text = THINK_TAIL.sub("", text)
    return text.strip()


# GeminiはAPIキーをクエリ文字列で渡す。例外メッセージにURLをそのまま載せると
# 測定ログやスタックトレースに鍵が残り、それを共有した時点で漏れる。
API_KEY_IN_URL = re.compile(r"([?&]key=)[^&\s]+")


def redact(text: str) -> str:
    return API_KEY_IN_URL.sub(r"\1［伏字］", text)


# 「待てば直る429」と「待っても直らない429」を見分ける印。
#
# Geminiは**日次上限でも `retryDelay: 39s` を返してくる。** 素直に従うと、
# 回復しないものを何度も待ち直すことになる。2026-08-10の測定では1問あたり
# 4回×約59秒を空費し、8問のA/Bが30分かかったうえ完走しなかった。
# 本番のGo側（internal/llm/http_clients.go の isDailyQuota）と同じ印で判定する。
NO_RETRY_MARKERS = (
    "prepayment credits are depleted",  # 残高枯渇（M9で実際に踏んだ）
    "perday",                            # quotaId: GenerateRequestsPerDayPerProjectPerModel-FreeTier
    "per_day",
    "requests per day",
    "rpd",
)


def post(url: str, payload: dict, headers: dict | None = None, timeout: int = 300,
         retries: int = 5) -> dict:
    """POSTしてJSONを返す。429（レート制限）は待って再試行する。

    無料枠は毎分数リクエストしか通らないため、31問の測定を素直に回すと
    途中で必ず 429 に当たる。ここで吸収しないと測定が完走しない。

    ただし日次上限と残高枯渇は待っても回復しないので、その場で諦める。
    """
    body = json.dumps(payload).encode()
    for attempt in range(retries):
        req = urllib.request.Request(url, body,
                                     {"Content-Type": "application/json", **(headers or {})})
        try:
            with urllib.request.urlopen(req, timeout=timeout) as response:
                return json.load(response)
        except urllib.error.HTTPError as err:
            detail = err.read().decode("utf-8", "replace")
            lower = detail.lower()
            depleted = any(marker in lower for marker in NO_RETRY_MARKERS)
            if depleted:
                print("    上限に達しました。待っても回復しないので中止します"
                      "（無料枠の日次上限は太平洋時間0時＝日本時間16時に戻ります）", flush=True)
            if err.code in (429, 503) and not depleted and attempt < retries - 1:
                # サーバーが待ち時間を指定してくればそれに従う
                match = re.search(r'"retryDelay"\s*:\s*"(\d+)s"', detail)
                wait = int(match.group(1)) if match else min(60, 5 * 2**attempt)
                print(f"    レート制限。{wait}秒待って再試行 ({attempt + 1}/{retries - 1})", flush=True)
                time.sleep(wait + 1)
                continue
            raise RuntimeError(f"{redact(url)} が {err.code}: {redact(detail)[:400]}") from err
    raise RuntimeError("再試行の上限に達しました")


class Base:
    """呼び出し回数と所要時間を数える共通部分。"""

    def __init__(self) -> None:
        self.calls = 0
        self.seconds = 0.0

    def _record(self, started: float) -> None:
        self.calls += 1
        self.seconds += time.time() - started

    def _parse(self, text: str, schema: dict | None) -> Any:
        text = strip_thinking(text)
        if not schema:
            return text
        # コードフェンスや前置きが付くことがあるのでJSON本体を取り出す
        start, end = text.find("{"), text.rfind("}")
        if start >= 0 and end > start:
            text = text[start : end + 1]
        try:
            return json.loads(text)
        except json.JSONDecodeError:
            return {}  # 呼び出し側が既定値で処理する


# ---------------------------------------------------------------- Ollama

class OllamaLLM(Base):
    """ローカル測定用。データを外部に出さない。

    プロンプト先頭の固定部分（目次）はKVキャッシュで再利用され、
    プロンプト処理が 80秒 → 0.8秒 になる（M2aで実測）。
    """

    def __init__(self, model: str | None = None, timeout: int = 900):
        super().__init__()
        self.model = model or os.environ.get("OLLAMA_MODEL", "qwen3:30b-a3b")
        self.endpoint = os.environ.get("OLLAMA_ENDPOINT", "http://localhost:11434")
        self.timeout = timeout

    def name(self) -> str:
        return f"ollama/{self.model}"

    def __call__(self, prompt: str, schema: dict | None = None, max_tokens: int = 800,
                 profile: str = "fast") -> Any:
        payload: dict[str, Any] = {
            "model": self.model,
            "prompt": prompt,
            "stream": False,
            "think": False,
            "options": {"num_ctx": NUM_CTX, "temperature": 0, "num_predict": max_tokens},
        }
        if schema:
            payload["format"] = schema
        started = time.time()
        result = post(f"{self.endpoint}/api/generate", payload, timeout=self.timeout)
        self._record(started)
        return self._parse(result.get("response", ""), schema)


# ---------------------------------------------------------------- Gemini

GEMINI_TYPES = {"object": "OBJECT", "array": "ARRAY", "string": "STRING",
                "boolean": "BOOLEAN", "integer": "INTEGER", "number": "NUMBER"}


def to_gemini_schema(schema: dict) -> dict:
    """JSON Schema を Gemini の responseSchema（OpenAPI 3.0 のサブセット）へ変換する。

    型名が大文字である点と、maxItems などの制約が通らない点が主な差分。
    """
    out: dict[str, Any] = {}
    if "type" in schema:
        out["type"] = GEMINI_TYPES.get(schema["type"], "STRING")
    if "properties" in schema:
        out["properties"] = {k: to_gemini_schema(v) for k, v in schema["properties"].items()}
    if "items" in schema:
        out["items"] = to_gemini_schema(schema["items"])
    if "required" in schema:
        out["required"] = schema["required"]
    return out


class GeminiLLM(Base):
    """Google Gemini。

    モデル名は変わりやすく、情報源によって食い違うため固定しない。
    キーで models API に問い合わせて、実際に使えるものから選ぶ。
    一覧は `python -m rag.llm` で確認できる。
    """

    BASE = "https://generativelanguage.googleapis.com/v1beta"

    def __init__(self, model: str | None = None, timeout: int = 300):
        super().__init__()
        self.key = os.environ.get("GEMINI_API_KEY") or os.environ.get("GOOGLE_API_KEY", "")
        if not self.key:
            raise RuntimeError("GEMINI_API_KEY を .env に設定してください")
        self.timeout = timeout
        self.model = model or os.environ.get("GEMINI_MODEL") or self.pick_model()
        self.models = {
            "fast": os.environ.get("GEMINI_FAST_MODEL") or self.model,
            "standard": os.environ.get("GEMINI_STANDARD_MODEL") or self.model,
            "deep": os.environ.get("GEMINI_DEEP_MODEL") or self.model,
        }
        # 無料枠は毎分のリクエスト数が絞られている。事前に間隔を空けておくと
        # 429 での待ち直しより結果的に速い
        self.interval = float(os.environ.get("GEMINI_MIN_INTERVAL", "4"))
        self._last = 0.0

    def name(self) -> str:
        return f"gemini/{self.model}"

    def list_models(self) -> list[str]:
        req = urllib.request.Request(f"{self.BASE}/models?pageSize=200",
                                     headers={"x-goog-api-key": self.key})
        with urllib.request.urlopen(req, timeout=30) as r:
            data = json.load(r)
        return [
            m["name"].removeprefix("models/")
            for m in data.get("models", [])
            if "generateContent" in m.get("supportedGenerationMethods", [])
        ]

    def pick_model(self) -> str:
        """無料枠の対象である Flash 系のうち、最も新しいものを選ぶ。

        一覧はバージョン順に並んでいないため（gemini-2.5-flash が
        gemini-3.5-flash より先に来る）、名前からバージョンを読んで比較する。
        """
        available = self.list_models()
        if not available:
            raise RuntimeError("利用可能なモデルを取得できませんでした")

        def version(name: str) -> float:
            m = re.search(r"(\d+(?:\.\d+)?)", name)
            return float(m.group(1)) if m else 0.0

        # 画像・音声用や lite / preview は除いた素の Flash を優先する
        plain = [n for n in available
                 if "flash" in n and not any(x in n for x in ("lite", "preview", "image", "tts"))]
        if plain:
            return max(plain, key=version)
        flash = [n for n in available if "flash" in n]
        return max(flash, key=version) if flash else available[0]

    def __call__(self, prompt: str, schema: dict | None = None, max_tokens: int = 800,
                 profile: str = "fast") -> Any:
        if profile not in ("fast", "standard", "deep"):
            raise ValueError(f"不正なLLMプロファイルです: {profile}")
        model = self.models[profile]
        config: dict[str, Any] = {"maxOutputTokens": max_tokens}
        if model.startswith(("gemini-3.", "gemini-3-")):
            # Gemini 3.x は思考トークンが maxOutputTokens を食う。実測では
            # 「1+1は？」でも47トークン使い、予算50だと本文がゼロ件で返ってきた。
            config["maxOutputTokens"] = max_tokens + 2000
            config["thinkingConfig"] = {
                "thinkingLevel": {"fast": "minimal", "standard": "medium", "deep": "high"}[profile]
            }
        else:
            # Gemini 3.5以降はsampling parameterが非推奨なので、旧モデルだけに送る。
            config["temperature"] = 0
        if schema:
            config["responseMimeType"] = "application/json"
            config["responseSchema"] = to_gemini_schema(schema)

        if (gap := self.interval - (time.time() - self._last)) > 0:
            time.sleep(gap)
        self._last = time.time()

        started = time.time()
        result = post(
            # APIキーはクエリではなくヘッダで渡す。クエリに載せると、429などの
            # 例外メッセージにURLごと鍵が入り、測定ログを共有した時点で漏れる
            f"{self.BASE}/models/{model}:generateContent",
            {"contents": [{"parts": [{"text": prompt}]}], "generationConfig": config},
            headers={"x-goog-api-key": self.key},
            timeout=self.timeout,
        )
        self._record(started)

        candidates = result.get("candidates") or []
        if not candidates:
            return {} if schema else ""
        parts = candidates[0].get("content", {}).get("parts") or []
        return self._parse("".join(p.get("text", "") for p in parts), schema)


# ---------------------------------------------------------------- OpenAI互換

class CompatLLM(Base):
    """OpenAI互換API。Grok(xAI) / Groq / Mistral / OpenRouter などが同じ形式で使える。

    LLM_BASE_URL / LLM_API_KEY / LLM_MODEL で指定する。例:
      xAI Grok  : https://api.x.ai/v1
      Groq      : https://api.groq.com/openai/v1
      OpenRouter: https://openrouter.ai/api/v1
    """

    def __init__(self, model: str | None = None, timeout: int = 300):
        super().__init__()
        self.base = os.environ.get("LLM_BASE_URL", "").rstrip("/")
        self.key = os.environ.get("LLM_API_KEY", "")
        self.model = model or os.environ.get("LLM_MODEL", "")
        if not (self.base and self.key and self.model):
            raise RuntimeError("LLM_BASE_URL / LLM_API_KEY / LLM_MODEL を .env に設定してください")
        self.timeout = timeout

    def name(self) -> str:
        return f"compat/{self.model}"

    def __call__(self, prompt: str, schema: dict | None = None, max_tokens: int = 800,
                 profile: str = "fast") -> Any:
        if schema:
            # json_schema モードは提供元によって対応が割れる。
            # 広く通る json_object と、プロンプトでのスキーマ指示を併用する
            prompt += ("\n\n次のJSONスキーマに厳密に従うJSONだけを出力してください。"
                       "前置き・説明・コードフェンスは書かないこと。\n"
                       + json.dumps(schema, ensure_ascii=False))

        payload: dict[str, Any] = {
            "model": self.model,
            "messages": [{"role": "user", "content": prompt}],
            "temperature": 0,
            "max_tokens": max_tokens,
        }
        if schema:
            payload["response_format"] = {"type": "json_object"}

        started = time.time()
        result = post(f"{self.base}/chat/completions", payload,
                      {"Authorization": f"Bearer {self.key}"}, timeout=self.timeout)
        self._record(started)
        choices = result.get("choices") or []
        text = choices[0].get("message", {}).get("content", "") if choices else ""
        return self._parse(text, schema)


# ----------------------------------------------------------------

def make_llm() -> Any:
    """LLM_PROVIDER に従ってクライアントを組み立てる。"""
    provider = os.environ.get("LLM_PROVIDER", "ollama").lower()
    if provider == "gemini":
        return GeminiLLM()
    if provider in ("compat", "grok", "groq", "openrouter", "mistral"):
        return CompatLLM()
    return OllamaLLM()


if __name__ == "__main__":
    # 使えるモデル名を確認する: python -m rag.llm
    from dotenv import load_dotenv

    load_dotenv()
    key = os.environ.get("GEMINI_API_KEY") or os.environ.get("GOOGLE_API_KEY")
    if not key:
        raise SystemExit("GEMINI_API_KEY が未設定です。.env に追記してください")

    probe = GeminiLLM.__new__(GeminiLLM)
    Base.__init__(probe)
    probe.key, probe.timeout = key, 30
    names = probe.list_models()
    print(f"generateContent が使えるモデル {len(names)}件:")
    for n in names:
        print(f"  {n}")
    print(f"\n自動選択されるモデル: {probe.pick_model()}")
