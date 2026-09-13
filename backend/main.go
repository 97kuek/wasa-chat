// WASA ChatのAPIサーバー。
//
//	go run ./backend            # ローカル（Ollama）
//	LLM_PROVIDER=claude go run ./backend
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/97kuek/wasa-chat/backend/internal/assistant"
	"github.com/97kuek/wasa-chat/backend/internal/index"
	"github.com/97kuek/wasa-chat/backend/internal/llm"
	"github.com/97kuek/wasa-chat/backend/internal/pipeline"
	"github.com/97kuek/wasa-chat/backend/internal/recap"
	"github.com/97kuek/wasa-chat/backend/internal/server"
	"github.com/97kuek/wasa-chat/backend/internal/sourcecheck"
	appstate "github.com/97kuek/wasa-chat/backend/internal/state"
	"github.com/97kuek/wasa-chat/backend/internal/wiki"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}

func envNonNegativeInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v >= 0 {
		return v
	}
	return fallback
}

// splitList はカンマ区切りの設定値を分解する。Wikiの利用者名には空白が
// 入る（「42 Wasa Taro」）ため、区切りは空白ではなくカンマにしてある。
func splitList(raw string) []string {
	var list []string
	for _, item := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			list = append(list, trimmed)
		}
	}
	return list
}

func envSeconds(key string, fallback float64) time.Duration {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil && v >= 0 {
		return time.Duration(v * float64(time.Second))
	}
	return time.Duration(fallback * float64(time.Second))
}

// loadIndex は索引を読み込み、どこから読んだかを併せて返す。
//
// `INDEX_GCS`（例: `gs://wasa-chat-index`）があればそちらを正本とし、
// 失敗したら起動を止める。イメージに焼いた古い索引で黙って動き続けると、
// 「更新したのに反映されない」という一番分かりにくい壊れ方をするためである。
// 設定が無ければ従来どおり `DATA_DIR` のファイルを読む（手元とdocker compose）。
func loadIndex() (*index.Index, string, error) {
	if location := os.Getenv("INDEX_GCS"); location != "" {
		// 起動を待たせ続けないため上限を切る。同一リージョンなら3MBは1秒未満
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ix, err := index.LoadGCS(ctx, location)
		return ix, location, err
	}
	dir := env("DATA_DIR", "data")
	ix, err := index.Load(dir)
	return ix, dir, err
}

// seedAssistants は assistants/*.json のうち未登録のものを登録する。
//
// ⚠️ **ADMIN_USERS が未設定でもアプリの起動は止めない。** 初期アシスタントは
// 作成者を決められないので見送るが、アシスタント以外はすべて正常に動く。
// ここで落とすと、設定を1つ書き忘れただけでチャットごと使えなくなる。
func seedAssistants(ctx context.Context, store appstate.Store, dir string, admins []string) error {
	seeds, err := assistant.LoadSeeds(dir)
	if err != nil {
		return err
	}
	if len(seeds) == 0 {
		return nil
	}
	if len(admins) == 0 {
		// 作成者不明のまま共有すると、誰に聞けばよいか分からないアシスタントになる
		log.Printf("初期アシスタント%d件は登録しません（ADMIN_USERSが未設定で作成者を決められません）", len(seeds))
		return nil
	}
	// **1件でもあれば何もしない。** 既存を上書きしない仕組みだけでは足りなかった。
	//
	// min-instances=0 なので、使われない時間が続くとコンテナは止まり、次の
	// アクセスで再起動する。つまりこの関数は1日に何度も走る。画面から消した
	// アシスタントが数分後に復活する状態になっていた（2026-08-10に判明）。
	//
	// これ以降、**正本はFirestoreであり assistants/*.json ではない。**
	// あちらは「空のときに入れる初期値」で、運用中の内容とは食い違ってよい。
	existing, err := store.ListAssistants(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		log.Printf("初期アシスタントは登録しません（既に%d件あります。正本はFirestoreです）", len(existing))
		return nil
	}
	added, err := assistant.Apply(ctx, store, seeds, admins[0])
	if err != nil {
		return err
	}
	log.Printf("初期アシスタント: %d件中 %d件を新規登録（既存は上書きしない）", len(seeds), added)
	return nil
}

func geminiDataUseApproved() bool {
	return os.Getenv("GEMINI_PAID_TIER") == "true" ||
		os.Getenv("GEMINI_FREE_TIER_APPROVED") == "true"
}

func pacificDay(at time.Time) string {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		location = time.FixedZone("PST", -8*60*60)
	}
	return at.In(location).Format("2006-01-02")
}

// newLLMClient は LLM_PROVIDER に応じたクライアントを作る。
//
// Gemini のときだけ具象型も返すのは、**無料枠の残量推定と停止状態が
// Gemini固有**だからである（管理画面の表示と、送信前の記録に使う）。
// ここで nil を返す構成では、それらの機能が静かに無効になる。
func newLLMClient() (llm.Client, *llm.Gemini) {
	provider := env("LLM_PROVIDER", "ollama")
	var client llm.Client
	var geminiClient *llm.Gemini
	switch provider {
	case "claude":
		client = llm.NewClaude(os.Getenv("CLAUDE_MODEL"))
	case "gemini":
		// 無料枠への非公開Wiki送信はデータ取扱いの判断を伴う。2026-08-08の
		// WASA会議で代表・PMが許可したため、有料枠とは別の明示フラグで起動を認める。
		if os.Getenv("K_SERVICE") != "" && !geminiDataUseApproved() {
			log.Fatal("Cloud RunでGeminiを使うには、GEMINI_PAID_TIER=true または GEMINI_FREE_TIER_APPROVED=true を設定してください")
		}
		key := env("GEMINI_API_KEY", os.Getenv("GOOGLE_API_KEY"))
		if key == "" {
			log.Fatal("GEMINI_API_KEY が未設定です")
		}
		// 強いモデルは、比較評価で有効性を確認してから環境変数へ設定する。
		// 未設定時は全モードで同じモデルを使い、推論量だけを変えるため、
		// UIを追加しただけで費用構造が黙って変わることはない。
		baseModel := env("GEMINI_MODEL", "gemini-3.5-flash-lite")
		models := llm.ModelProfiles{
			Default:  baseModel,
			Fast:     env("GEMINI_FAST_MODEL", baseModel),
			Standard: env("GEMINI_STANDARD_MODEL", baseModel),
			Deep:     env("GEMINI_DEEP_MODEL", baseModel),
		}
		geminiClient = llm.NewGeminiProfiles(
			key,
			models,
			envSeconds("GEMINI_MIN_INTERVAL", 4),
			envNonNegativeInt("GEMINI_MAX_RETRIES", 2),
		)
		client = geminiClient
		log.Printf("Gemini回答モード: 高速=%s / 標準=%s / じっくり=%s", models.Fast, models.Standard, models.Deep)
	case "compat", "grok", "groq", "openrouter", "mistral":
		client = llm.NewCompat(os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_API_KEY"), os.Getenv("LLM_MODEL"))
	default:
		client = llm.NewOllama(
			env("OLLAMA_ENDPOINT", "http://localhost:11434"),
			env("OLLAMA_MODEL", "qwen3:30b-a3b"),
		)
	}
	return client, geminiClient
}

// sessionSecret はCookie署名の鍵を用意する。
//
// ⚠️ **本番では固定値が必須。** 変えると全員がログアウトするだけでなく、
// HMAC利用者キーも変わるため、当日の利用回数とチャット履歴を参照できなくなる
// （docs/09 D-2）。手元では未設定でも起動するが、再起動のたびに別人になる。
func sessionSecret() string {
	secret := os.Getenv("SESSION_SECRET")
	if secret == "" {
		if os.Getenv("K_SERVICE") != "" {
			log.Fatal("Cloud RunではSESSION_SECRETの固定値が必須です")
		}
		// 未設定でも起動はする。ただし再起動で全員ログアウトになる点は警告する
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			log.Fatalf("セッション鍵の生成に失敗: %v", err)
		}
		secret = base64.RawStdEncoding.EncodeToString(buf)
		log.Println("警告: SESSION_SECRET が未設定のため一時鍵を生成しました。再起動で全員ログアウトし、当日の質問回数も復元できません")
	} else if len(secret) < 32 {
		log.Fatal("SESSION_SECRETは32文字以上で設定してください")
	}
	return secret
}

// newStateStore は履歴・利用回数・アシスタントの保存先を用意する。
//
// 手元ではメモリ、本番ではFirestore。**Cloud Runでメモリに落ちないようにする。**
// 落ちると、再起動のたびに履歴と当日の回数が消える状態で動き続けてしまう。
// 3つ目の戻り値は後片付け（メモリのときは何もしない）。
func newStateStore() (appstate.Store, string, func()) {
	closer := func() error { return nil }
	sharedState := appstate.Store(appstate.NewMemory())
	storeName := "メモリ（ローカル）"
	if projectID := os.Getenv("FIRESTORE_PROJECT_ID"); projectID != "" {
		firestoreState, err := appstate.NewFirestore(context.Background(), projectID)
		if err != nil {
			log.Fatalf("Firestoreへ接続できません: %v", err)
		}
		closer = firestoreState.Close
		sharedState = firestoreState
		storeName = "Firestore"
		log.Printf("共有状態: Firestore（project=%s）", projectID)
	} else if os.Getenv("K_SERVICE") != "" {
		log.Fatal("Cloud RunではFIRESTORE_PROJECT_IDが必須です")
	} else {
		log.Println("共有状態: メモリ（ローカル開発用。再起動で履歴と利用回数が消えます）")
	}
	return sharedState, storeName, func() {
		if err := closer(); err != nil {
			log.Printf("保存先の後片付けに失敗: %v", err)
		}
	}
}

func main() {
	// リポジトリ直下の .env を読む。測定用のPython側と同じ設定を使えるようにするため。
	// 既に環境変数が設定されていればそちらが優先される
	for _, path := range []string{".env", "../.env"} {
		if err := godotenv.Load(path); err == nil {
			log.Printf("設定を読み込み: %s", path)
			break
		}
	}

	ix, source, err := loadIndex()
	if err != nil {
		log.Fatalf("インデックスを読み込めません（%s）: %v\n"+
			"先に python build_index.py && python build_toc.py を実行してください", source, err)
	}
	pages, chunks := ix.Stats()
	log.Printf("インデックス読み込み完了（%s）: %dページ / %dチャンク / 目次%d字",
		source, pages, chunks, len([]rune(ix.TOC)))

	client, geminiClient := newLLMClient()
	log.Printf("モデル: %s", client.Name())

	// 認証はWikiのアカウントに委ねる。共有パスワードを配らずに済み、
	// 利用者名が取れるのでレート制限を個人ごとに分けられる（docs/01-設計方針.md §8-1）
	wikiAPI := env("WIKI_API", "https://wasabirdman.sakura.ne.jp/wbwiki/w/api.php")

	secret := sessionSecret()
	allowOrigin := os.Getenv("ALLOW_ORIGIN")
	if os.Getenv("K_SERVICE") != "" && allowOrigin == "" {
		log.Fatal("Cloud RunではCloudflare PagesのURLをALLOW_ORIGINに設定してください")
	}

	sharedState, storeName, closeState := newStateStore()
	defer closeState()
	rpdLimit := envInt("GEMINI_RPD_LIMIT", 500)
	if geminiClient != nil {
		// **送る前に止める。** これまで GEMINI_RPD_LIMIT は管理画面へ残量を出すだけで、
		// 上限に達しても止まるのは「429が返ってから」だった。日次上限の429は待っても
		// 回復しない（太平洋時間0時まで）ので、そこまで全部の質問が失敗し続ける。
		//
		// 数え方は「実際にGeminiへ送った回数」で、質問数ではない。1質問で2〜3回送る。
		// 記録が読めないときは**通す**。数えられないことを理由に全部止めると、
		// Firestoreの一時的な不調で丸一日使えなくなる
		geminiClient.SetAttemptGuard(func(ctx context.Context, model string) error {
			if rpdLimit <= 0 {
				return nil
			}
			day := pacificDay(time.Now().UTC())
			usage, err := sharedState.ListAPIUsage(ctx, day)
			if err != nil {
				log.Printf("Gemini利用回数を読めません（送信は通します）: %v", err)
				return nil
			}
			for _, row := range usage {
				if row.Model == model && row.Requests >= rpdLimit {
					return fmt.Errorf("%w（%s は本日%d回に達しました）",
						llm.ErrQuotaGuard, model, row.Requests)
				}
			}
			return nil
		})
		geminiClient.SetAttemptObserver(func(_ context.Context, attempt llm.APIAttempt) {
			// 利用者の接続が切れても、すでにGeminiへ送った1回は無料枠から減る。
			// 元リクエストのcontextから切り離し、短い上限だけ付けて確実に数える。
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := sharedState.RecordAPIRequest(ctx, pacificDay(attempt.At), attempt.Model, attempt.At); err != nil {
				log.Printf("Gemini利用回数の記録に失敗: %v", err)
			}
		})
	}

	// 管理画面と、他人のアシスタントを消す権限を持つ個人Wiki利用者。
	// 共有管理者アカウントは監査と個別失効ができないため使わない。
	// 索引を差し替えたら、再デプロイせずに取り込む。
	//
	// 以前は起動時に1回読むだけで、差し替え後は環境変数を動かして新しい
	// リビジョンへ入れ替える必要があった（docs/07）。**資料を直してから
	// 反映されるまでの待ちは、ここが作っていた。**
	//
	// 確認は世代番号だけを見るので1リクエストで済み、変わっていなければ
	// 3.4MBは読まない。min-instances=0 なので使われていない間はこのループごと
	// 止まるが、次のアクセスで起動したインスタンスは最新を読むため取りこぼさない。
	live := index.NewLive(ix, source)
	// envInt は正の値しか受けないので、無効化は別に読む。
	// 0 を渡せないと、止めたいときに止められない（2026-09-12にCodexが指摘）
	watchSeconds := 60
	if raw := os.Getenv("INDEX_WATCH_SECONDS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			watchSeconds = parsed
		}
	}
	if every := time.Duration(watchSeconds) * time.Second; every > 0 {
		go live.Watch(context.Background(), every, nil)
		log.Printf("索引の更新確認: %v ごと（%s）", every, source)
	} else {
		log.Println("索引の更新確認は無効です（INDEX_WATCH_SECONDS=0）")
	}

	admins := splitList(os.Getenv("ADMIN_USERS"))
	if len(admins) == 0 {
		log.Println("警告: ADMIN_USERS が未設定です。アシスタントは作成者本人しか削除できず、初期アシスタントも登録されません")
	}
	if err := seedAssistants(context.Background(), sharedState, env("ASSISTANT_SEED_DIR", "assistants"), admins); err != nil {
		log.Fatalf("初期アシスタントの登録に失敗: %v", err)
	}

	serverConfig := server.Config{
		SessionSecret:    secret,
		DailyLimit:       envInt("DAILY_LIMIT", 30),
		APIDailyLimit:    rpdLimit,
		AllowOrigin:      allowOrigin,
		SPADir:           os.Getenv("SPA_DIR"),
		StoreName:        storeName,
		IndexSource:      source,
		Revision:         env("K_REVISION", "local"),
		CodeVersion:      env("APP_VERSION", "local"),
		IndexPublishedAt: os.Getenv("INDEX_PUBLISHED_AT"),
		LLMName:          client.Name(),
		TasksQueue:       os.Getenv("TASKS_QUEUE"),
		PublicURL:        os.Getenv("PUBLIC_URL"),
		DiscordPublicKey: os.Getenv("DISCORD_PUBLIC_KEY"),
		DiscordAppID:     os.Getenv("DISCORD_APP_ID"),
		DiscordBotToken:  os.Getenv("DISCORD_BOT_TOKEN"),
		// どちらも未設定でよい。絞りたいときだけ指定する（docs/09 A-12）
		DiscordGuildIDs:        splitList(os.Getenv("DISCORD_GUILD_IDS")),
		DriveServiceAccount:    os.Getenv("DRIVE_SERVICE_ACCOUNT"),
		CalendarIDs:            splitList(os.Getenv("CALENDAR_IDS")),
		CalendarServiceAccount: os.Getenv("CALENDAR_SERVICE_ACCOUNT"),
		DiscordSearchChannels:  splitList(os.Getenv("DISCORD_SEARCH_CHANNELS")),
		AdminUsers:             admins,
	}
	updateChecker := sourcecheck.New(
		live, wikiAPI,
		env("WIKI_UPDATE_USER", os.Getenv("WIKI_USER")),
		env("WIKI_UPDATE_PASS", os.Getenv("WIKI_PASS")),
	)
	serverConfig.SourceCheckAvailable = updateChecker.Available()
	if updateChecker.Available() {
		serverConfig.SourceCheck = updateChecker.Check
	} else {
		log.Println("管理画面の更新確認は無効です（WIKI_UPDATE_USER / WIKI_UPDATE_PASSが未設定）")
	}
	if geminiClient != nil {
		serverConfig.LLMStatus = geminiClient.RuntimeStatus
	}
	srv := server.New(serverConfig, live, pipeline.New(live, client), wiki.New(wikiAPI), sharedState, recap.New(client))

	addr := ":" + env("PORT", "8080") // Cloud Run は PORT を渡してくる
	log.Printf("起動: http://localhost%s", addr)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout は設定しない。SSEで回答を流し続けるため、
		// 書き込みに期限を設けると長い回答の途中で接続が切れる。
		IdleTimeout: 120 * time.Second,
	}
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("サーバーが停止しました: %v", err)
	}
}
