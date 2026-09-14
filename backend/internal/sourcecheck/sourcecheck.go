// Package sourcecheck は、現在の索引と公開元のメタデータだけを照合する。
// 本文の再取得や索引の自動反映は行わず、管理者が再構築の要否を判断する材料だけを返す。
package sourcecheck

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/97kuek/wasa-chat/backend/internal/index"
	"github.com/97kuek/wasa-chat/backend/internal/state"
)

const requestInterval = time.Second

var defaultSitemaps = []string{
	"https://wasa-birdman.com/page-sitemap.xml",
	"https://wasa-birdman.com/post-sitemap.xml",
}

type Checker struct {
	live        *index.Live
	wikiAPI     string
	wikiUser    string
	wikiPass    string
	sitemaps    []string
	client      *http.Client
	interval    time.Duration
	requestMu   sync.Mutex
	lastRequest time.Time
}

func New(live *index.Live, wikiAPI, wikiUser, wikiPass string) *Checker {
	jar, _ := cookiejar.New(nil)
	return &Checker{
		live: live, wikiAPI: wikiAPI, wikiUser: wikiUser, wikiPass: wikiPass,
		sitemaps: defaultSitemaps, interval: requestInterval,
		client: &http.Client{Timeout: 60 * time.Second, Jar: jar},
	}
}

func (c *Checker) Available() bool {
	return c != nil && c.live != nil && c.wikiAPI != "" && c.wikiUser != "" && c.wikiPass != ""
}

func (c *Checker) wait(ctx context.Context) error {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	if wait := c.interval - time.Since(c.lastRequest); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	c.lastRequest = time.Now()
	return nil
}

func (c *Checker) do(ctx context.Context, method, endpoint string, values url.Values) ([]byte, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	var body io.Reader
	if method == http.MethodGet {
		// 空のクエリでも末尾へ「?」を付けると、公式サイトのCloudflareが
		// 同じサイトマップを404として扱う。値があるときだけ追加する。
		if encoded := values.Encode(); encoded != "" {
			endpoint += "?" + encoded
		}
	} else {
		body = strings.NewReader(values.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "WASAChatUpdateCheck/0.2 (internal handover chatbot)")
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s がHTTP %dを返しました", endpoint, res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 4<<20))
}

func wikiValues(values url.Values) url.Values {
	values.Set("format", "json")
	values.Set("formatversion", "2")
	return values
}

func wikiError(payload map[string]any) error {
	raw, ok := payload["error"].(map[string]any)
	if !ok {
		return nil
	}
	if info, ok := raw["info"].(string); ok && info != "" {
		return errors.New(info)
	}
	return errors.New("Wiki APIがエラーを返しました")
}

func (c *Checker) wikiCall(ctx context.Context, method string, values url.Values, target any) error {
	raw, err := c.do(ctx, method, c.wikiAPI, wikiValues(values))
	if err != nil {
		return err
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("Wiki APIの応答が不正です: %w", err)
	}
	if err := wikiError(envelope); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("Wiki APIの応答を読めません: %w", err)
	}
	return nil
}

func (c *Checker) wikiLogin(ctx context.Context) error {
	var token struct {
		Query struct {
			Tokens struct {
				LoginToken string `json:"logintoken"`
			} `json:"tokens"`
		} `json:"query"`
	}
	if err := c.wikiCall(ctx, http.MethodGet, url.Values{
		"action": {"query"}, "meta": {"tokens"}, "type": {"login"},
	}, &token); err != nil {
		return err
	}
	var login struct {
		ClientLogin struct {
			Status string `json:"status"`
		} `json:"clientlogin"`
	}
	if err := c.wikiCall(ctx, http.MethodPost, url.Values{
		"action": {"clientlogin"}, "username": {c.wikiUser}, "password": {c.wikiPass},
		"loginreturnurl": {"https://example.org/"}, "logintoken": {token.Query.Tokens.LoginToken},
	}, &login); err != nil {
		return err
	}
	if login.ClientLogin.Status != "PASS" {
		return errors.New("更新確認用アカウントでWikiへログインできませんでした")
	}
	return nil
}

type wikiRevision struct {
	Title string
	Revid int
}

func (c *Checker) wikiRevisions(ctx context.Context) (map[string]wikiRevision, error) {
	if err := c.wikiLogin(ctx); err != nil {
		return nil, err
	}
	pages := map[string]wikiRevision{}
	continuation := url.Values{}
	for {
		values := url.Values{
			"action": {"query"}, "generator": {"allpages"}, "gapnamespace": {"0"},
			"gaplimit": {"max"}, "prop": {"revisions"}, "rvprop": {"ids"},
		}
		for key, items := range continuation {
			values[key] = items
		}
		var payload struct {
			Continue map[string]any `json:"continue"`
			Query    struct {
				Pages []struct {
					PageID    int    `json:"pageid"`
					Title     string `json:"title"`
					Revisions []struct {
						Revid int `json:"revid"`
					} `json:"revisions"`
				} `json:"pages"`
			} `json:"query"`
		}
		if err := c.wikiCall(ctx, http.MethodGet, values, &payload); err != nil {
			return nil, err
		}
		for _, page := range payload.Query.Pages {
			if len(page.Revisions) > 0 {
				pages[strconv.Itoa(page.PageID)] = wikiRevision{Title: page.Title, Revid: page.Revisions[0].Revid}
			}
		}
		if len(payload.Continue) == 0 {
			return pages, nil
		}
		continuation = url.Values{}
		for key, value := range payload.Continue {
			continuation.Set(key, fmt.Sprint(value))
		}
	}
}

type sitemapURLSet struct {
	URLs []struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod"`
	} `xml:"url"`
}

func (c *Checker) siteDates(ctx context.Context) (map[string]string, error) {
	dates := map[string]string{}
	for _, sitemap := range c.sitemaps {
		raw, err := c.do(ctx, http.MethodGet, sitemap, url.Values{})
		if err != nil {
			return nil, err
		}
		var parsed sitemapURLSet
		if err := xml.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("公式サイトのサイトマップを読めません: %w", err)
		}
		for _, item := range parsed.URLs {
			date := item.LastMod
			if len(date) > 10 {
				date = date[:10]
			}
			dates[strings.TrimSpace(item.Loc)] = date
		}
	}
	return dates, nil
}

func sorted(items []string) []string {
	if items == nil {
		return []string{}
	}
	sort.Strings(items)
	return items
}

func (c *Checker) compareWiki(remote map[string]wikiRevision) state.SourceDelta {
	local := map[string]wikiRevision{}
	for _, page := range c.live.Current().Pages {
		if page.Source == index.OriginWiki {
			local[page.ID] = wikiRevision{Title: page.Title, Revid: page.Revid}
		}
	}
	delta := state.SourceDelta{Source: "wiki"}
	for id, current := range remote {
		previous, ok := local[id]
		if !ok {
			delta.Added = append(delta.Added, current.Title)
		} else if previous.Revid != current.Revid {
			delta.Updated = append(delta.Updated, current.Title)
		}
	}
	for id, previous := range local {
		if _, ok := remote[id]; !ok {
			delta.Removed = append(delta.Removed, previous.Title)
		}
	}
	delta.Added, delta.Updated, delta.Removed = sorted(delta.Added), sorted(delta.Updated), sorted(delta.Removed)
	return delta
}

func (c *Checker) compareSite(remote map[string]string) state.SourceDelta {
	type sitePage struct{ Title, Date string }
	local := map[string]sitePage{}
	for _, page := range c.live.Current().Pages {
		if page.Source == index.OriginSite {
			local[page.URL] = sitePage{Title: page.Title, Date: page.LastEdited}
		}
	}
	delta := state.SourceDelta{Source: "site"}
	for pageURL, currentDate := range remote {
		previous, ok := local[pageURL]
		if !ok {
			delta.Added = append(delta.Added, pageURL)
		} else if previous.Date != currentDate {
			delta.Updated = append(delta.Updated, previous.Title)
		}
	}
	for pageURL, previous := range local {
		if _, ok := remote[pageURL]; !ok {
			delta.Removed = append(delta.Removed, previous.Title)
		}
	}
	delta.Added, delta.Updated, delta.Removed = sorted(delta.Added), sorted(delta.Updated), sorted(delta.Removed)
	return delta
}

// Check は索引と公開元を照合する。
//
// ⚠️ **見ているのは引き継ぎWikiと公式サイトだけである。**
// 索引には出所が4つある（wiki / site / fee / drive）が、フライトシミュレータの
// ガイドと共有ドライブはここでは照合しない。前者は更新が止まっており、後者は
// 更新Jobが取り込みのたびに差分を見ているため。**「変更なし」と出ても、
// 共有ドライブが変わっていないことの保証にはならない。**
func (c *Checker) Check(ctx context.Context) ([]state.SourceDelta, error) {
	if !c.Available() {
		return nil, errors.New("更新確認用のWikiアカウントが設定されていません")
	}
	wikiPages, err := c.wikiRevisions(ctx)
	if err != nil {
		return nil, fmt.Errorf("Wikiの更新確認: %w", err)
	}
	sitePages, err := c.siteDates(ctx)
	if err != nil {
		return nil, fmt.Errorf("公式サイトの更新確認: %w", err)
	}
	return []state.SourceDelta{c.compareWiki(wikiPages), c.compareSite(sitePages)}, nil
}
