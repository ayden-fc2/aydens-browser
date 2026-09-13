package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReportedChineseQueryRegression(t *testing.T) {
	q := "2026年国庆假期 热门旅游目的地 推荐"
	for _, title := range []string{"华硕 Z170 PRO GAMING 主板全面详测", "在线秒表2026", "中华人民共和国国庆节 假期安排", "2026日历", "春节旅游目的地 超长假期国庆预订", "端午国内旅游推荐 假期目的地"} {
		if score := relevance(q, SearchResult{Title: title}); score != 0 {
			t.Errorf("accepted unrelated %q: %d", title, score)
		}
	}
	for _, title := range []string{"2026 国庆旅游热门目的地：青岛、重庆预订升温", "中秋国庆假期机票预订：热门旅游城市"} {
		if relevance(q, SearchResult{Title: title}) == 0 {
			t.Errorf("rejected relevant %q", title)
		}
	}
	if relevance("国庆 旅游 热门城市 排行", SearchResult{Title: "国庆中秋放假安排、购票日历来了"}) != 0 {
		t.Fatal("holiday notice accepted as travel ranking")
	}
}
func TestTimeFiltering(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name, title, date string
		o                 SearchOptions
		want              bool
	}{
		{"last year", "2025年国庆热门旅游城市", "2025-09-01T00:00:00Z", SearchOptions{}, false},
		{"fresh", "2026年国庆热门城市", "2026-09-10T00:00:00Z", SearchOptions{TimeRange: "week"}, true},
		{"unknown date", "2026年国庆热门城市", "", SearchOptions{TimeRange: "week"}, false},
		{"old", "国庆热门旅游城市", "2022-10-01T00:00:00Z", SearchOptions{}, false},
		{"future", "国庆热门旅游城市", "2027-01-01T00:00:00Z", SearchOptions{}, false},
		{"timeless", "2026年国庆热门城市", "", SearchOptions{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inSearchTime("2026年国庆 旅游", SearchResult{Title: c.title, PublishedAt: c.date}, c.o, now); got != c.want {
				t.Fatal(got)
			}
		})
	}
}
func TestIndependentSourcesFallbackAndIsolation(t *testing.T) {
	s := NewSearch("")
	s.engines = []searchEngine{
		{"broken", func(context.Context, string, SearchOptions) ([]SearchResult, error) {
			return nil, errors.New("captcha")
		}},
		{"unrelated", func(context.Context, string, SearchOptions) ([]SearchResult, error) {
			return []SearchResult{{Title: "华硕主板 BIOS", URL: "https://example.com/bios"}}, nil
		}},
		{"working", func(ctx context.Context, q string, o SearchOptions) ([]SearchResult, error) {
			return []SearchResult{{Title: q, URL: "https://example.org/a?utm_source=x"}, {Title: q, URL: "https://example.org/a"}, {Title: q, URL: "javascript:alert(1)"}}, nil
		}},
	}
	for _, q := range []string{"2026 国庆 旅游", "PostgreSQL Docker documentation", "中文 & C++?"} {
		t.Run(q, func(t *testing.T) {
			t.Parallel()
			out, e := s.Run(context.Background(), q)
			if e != nil || len(out.Results) != 1 || out.Results[0].Title != q || out.Status != "partial" || out.Provider != "working" {
				t.Fatal(out, e)
			}
			if out.Attempts[1].Status != "no_relevant_results" {
				t.Fatal(out.Attempts)
			}
		})
	}
}
func TestNoRelevantResultsIsExplicit(t *testing.T) {
	s := NewSearch("")
	s.engines = []searchEngine{{"junk", func(context.Context, string, SearchOptions) ([]SearchResult, error) {
		return []SearchResult{{Title: "主板 BIOS", URL: "https://example.com"}}, nil
	}}}
	out, e := s.Run(context.Background(), "国庆 旅游 热门城市")
	if e != nil || out.Status != "no_relevant_results" || len(out.Results) != 0 || out.Notice == "" {
		t.Fatal(out, e)
	}
	s.engines[0].run = func(context.Context, string, SearchOptions) ([]SearchResult, error) {
		return nil, errors.New("timeout")
	}
	out, e = s.Run(context.Background(), "国庆 旅游")
	if e != nil || out.Status != "unavailable" {
		t.Fatal(out, e)
	}
}
func TestParsers(t *testing.T) {
	duck := `<div class="result result--ad"><a class="result__a" href="https://ads.example.com">advert</a></div><div class="result web-result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Farticle%3Fa%3D1%26b%3D2">国庆<b>旅游</b></a><a class="result__snippet">热门 <b>城市</b>报道</a></div>`
	r, e := parseDuck([]byte(duck))
	if e != nil || len(r) != 1 || r[0].URL != "https://example.com/article?a=1&b=2" || strings.Contains(r[0].Snippet, "<b>") {
		t.Fatal(r, e)
	}
	if _, e = parseDuck([]byte(`<title>Captcha</title>`)); e == nil {
		t.Fatal("captcha accepted")
	}
	baidu := `<div class="result c-container" mu="https://example.com/article"><h3>国庆旅游</h3><p>旅游预订</p><script>bad()</script></div><div class="result c-container" mu="https://ad.com"><h3 class="ec_title">广告</h3></div>`
	r, e = parseBaidu([]byte(baidu))
	if e != nil || len(r) != 1 || strings.Contains(r[0].Snippet, "bad()") {
		t.Fatal(r, e)
	}
	rss := `<rss><channel><item><title>国庆旅游 - 某报</title><link>https://news.google.com/article/a</link><description>&lt;a href="https://example.com"&gt;正文摘要&lt;/a&gt;</description><pubDate>Thu, 10 Sep 2026 08:00:00 GMT</pubDate><source url="https://example.com">某报</source></item></channel></rss>`
	r, e = parseNews([]byte(rss))
	if e != nil || len(r) != 1 || r[0].PublishedAt != "2026-09-10T08:00:00Z" || r[0].Source != "某报" || strings.Contains(r[0].Snippet, "<a") {
		t.Fatal(r, e)
	}
}
func TestAPIAuthenticationAndEncoding(t *testing.T) {
	s := NewSearch("")
	s.Token = strings.Repeat("x", 48)
	q := "国庆旅游 & C++?"
	s.engines = []searchEngine{{"fake", func(ctx context.Context, got string, o SearchOptions) ([]SearchResult, error) {
		if got != q || o.Topic != "news" {
			t.Error(got, o)
		}
		return []SearchResult{{Title: q, URL: "https://example.com"}}, nil
	}}}
	h := s.Handler()
	for _, auth := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/v1/search?q=%E5%9B%BD%E5%BA%86%E6%97%85%E6%B8%B8+%26+C%2B%2B%3F&topic=news", nil)
		if auth {
			r.Header.Set("Authorization", "Bearer "+s.Token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !auth && w.Code != 401 {
			t.Fatal(w.Code)
		}
		if auth {
			var out SearchResponse
			json.Unmarshal(w.Body.Bytes(), &out)
			if w.Code != 200 || len(out.Results) != 1 {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
	for _, o := range []SearchOptions{{Topic: "invalid"}, {TimeRange: "century"}} {
		if _, e := s.RunWithOptions(context.Background(), q, o); e == nil {
			t.Fatal("bad options accepted")
		}
	}
}
func TestBrowserForwardingPreservesIdentityAndErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 48) {
			t.Error("missing key")
		}
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		if b["user_id"] != "daily:123" || r.URL.Path != "/v1/actions" {
			t.Error(b, r.URL.Path)
		}
		w.Header().Set("Retry-After", "15")
		w.WriteHeader(429)
		w.Write([]byte(`{"error":"busy"}`))
	}))
	defer upstream.Close()
	s := NewSearch("")
	s.Token = strings.Repeat("x", 48)
	s.BrowserURL = upstream.URL
	r := httptest.NewRequest("POST", "/v1/browser/actions", strings.NewReader(`{"user_id":"daily:123","action":"open","url":"https://example.com"}`))
	r.Header.Set("Authorization", "Bearer "+s.Token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 429 || w.Header().Get("Retry-After") != "15" {
		t.Fatal(w.Code)
	}
}
