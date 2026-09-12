package sourcecheck

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/97kuek/wasa-chat/backend/internal/index"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func testResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestCheckComparesWikiRevisionsAndSiteDates(t *testing.T) {
	const base = "https://source.example"
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/wiki":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			switch {
			case r.Form.Get("meta") == "tokens":
				return testResponse(`{"query":{"tokens":{"logintoken":"token"}}}`), nil
			case r.Form.Get("action") == "clientlogin":
				return testResponse(`{"clientlogin":{"status":"PASS"}}`), nil
			default:
				return testResponse(`{"query":{"pages":[{"pageid":1,"title":"同じWiki","revisions":[{"revid":10}]},{"pageid":2,"title":"更新Wiki","revisions":[{"revid":21}]},{"pageid":3,"title":"追加Wiki","revisions":[{"revid":1}]}]}}`), nil
			}
		case "/page.xml":
			if r.URL.RawQuery != "" {
				return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			return testResponse(`<urlset><url><loc>` + base + `/same</loc><lastmod>2026-08-01</lastmod></url><url><loc>` + base + `/updated</loc><lastmod>2026-08-11</lastmod></url></urlset>`), nil
		case "/post.xml":
			if r.URL.RawQuery != "" {
				return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			return testResponse(`<urlset><url><loc>` + base + `/added</loc><lastmod>2026-08-11</lastmod></url></urlset>`), nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
	})

	ix := &index.Index{Pages: []index.Page{
		{ID: "1", Source: "wiki", Title: "同じWiki", Revid: 10},
		{ID: "2", Source: "wiki", Title: "更新Wiki", Revid: 20},
		{ID: "4", Source: "wiki", Title: "削除Wiki", Revid: 1},
		{ID: "s1", Source: "site", Title: "同じ記事", URL: base + "/same", LastEdited: "2026-08-01"},
		{ID: "s2", Source: "site", Title: "更新記事", URL: base + "/updated", LastEdited: "2026-08-01"},
		{ID: "s3", Source: "site", Title: "削除記事", URL: base + "/removed", LastEdited: "2026-08-01"},
	}}
	checker := New(index.NewLive(ix, "test"), base+"/wiki", "reader", "password")
	checker.interval = 0
	checker.sitemaps = []string{base + "/page.xml", base + "/post.xml"}
	checker.client.Transport = transport

	deltas, err := checker.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 2 {
		t.Fatalf("差分の出所数が不正: %+v", deltas)
	}
	if got := deltas[0]; len(got.Added) != 1 || got.Added[0] != "追加Wiki" ||
		len(got.Updated) != 1 || got.Updated[0] != "更新Wiki" ||
		len(got.Removed) != 1 || got.Removed[0] != "削除Wiki" {
		t.Errorf("Wiki差分が不正: %+v", got)
	}
	if got := deltas[1]; len(got.Added) != 1 || got.Added[0] != base+"/added" ||
		len(got.Updated) != 1 || got.Updated[0] != "更新記事" ||
		len(got.Removed) != 1 || got.Removed[0] != "削除記事" {
		t.Errorf("公式サイト差分が不正: %+v", got)
	}
}
