package search

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type SearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	Source      string `json:"source,omitempty"`
	Engine      string `json:"engine,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}
type SearchAttempt struct {
	Engine   string `json:"engine"`
	Status   string `json:"status"`
	Accepted int    `json:"accepted"`
}
type SearchResponse struct {
	Provider    string          `json:"provider"`
	Query       string          `json:"query"`
	Status      string          `json:"status"`
	RetrievedAt string          `json:"retrieved_at"`
	Results     []SearchResult  `json:"results"`
	Attempts    []SearchAttempt `json:"attempts,omitempty"`
	Notice      string          `json:"notice,omitempty"`
}
type SearchOptions struct {
	Topic     string `json:"topic,omitempty"`
	TimeRange string `json:"time_range,omitempty"`
}
type searchEngine struct {
	name string
	run  func(context.Context, string, SearchOptions) ([]SearchResult, error)
}
type Search struct {
	BrowserURL string
	Token      string
	Client     *http.Client
	engines    []searchEngine
	now        func() time.Time
}

func NewSearch(base string) *Search {
	return &Search{Client: &http.Client{Timeout: 38 * time.Second}, now: time.Now}
}

// The API delegates to the worker. Only the worker accesses search engines.
func NewSearchWorker(proxy, searx, browser, token string) (*Search, error) {
	s := NewSearch("")
	s.BrowserURL, s.Token = strings.TrimRight(browser, "/"), token
	direct := &http.Client{Timeout: 14 * time.Second, Transport: &http.Transport{Proxy: nil}}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if proxy != "" {
		u, e := url.Parse(proxy)
		if e != nil || u.Scheme != "http" || u.Host == "" {
			return nil, errors.New("invalid SEARCH_PROXY_URL")
		}
		transport.Proxy = http.ProxyURL(u)
	}
	outbound := &http.Client{Timeout: 14 * time.Second, Transport: transport}
	s.engines = []searchEngine{
		{"DuckDuckGo", func(ctx context.Context, q string, o SearchOptions) ([]SearchResult, error) {
			return duckSearch(ctx, outbound, q, o)
		}},
		{"Google News", func(ctx context.Context, q string, o SearchOptions) ([]SearchResult, error) {
			return newsSearch(ctx, outbound, q, o)
		}},
		{"Baidu", func(ctx context.Context, q string, o SearchOptions) ([]SearchResult, error) {
			return baiduSearch(ctx, direct, q, o)
		}},
	}
	if searx != "" {
		s.engines = append(s.engines, searchEngine{"SearXNG", func(ctx context.Context, q string, o SearchOptions) ([]SearchResult, error) {
			return searxSearch(ctx, direct, searx, q, o)
		}})
	}
	return s, nil
}
func validLink(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}
func fetchSearch(ctx context.Context, c *http.Client, target, token string) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", target, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.7")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r, e := c.Do(req)
	if e != nil {
		return nil, errors.New("search network unavailable")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, errors.New("search upstream unavailable")
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, (3<<20)+1))
	if len(b) > 3<<20 {
		return nil, errors.New("search response too large")
	}
	return b, e
}
func (s *Search) Run(ctx context.Context, q string) (SearchResponse, error) {
	return s.RunWithOptions(ctx, q, SearchOptions{})
}
func (s *Search) RunWithOptions(ctx context.Context, q string, o SearchOptions) (SearchResponse, error) {
	q = strings.TrimSpace(q)
	now := s.now().UTC()
	q = strings.ReplaceAll(q, "今年", strconv.Itoa(now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Year())+"年")
	out := SearchResponse{Query: q, Results: []SearchResult{}, RetrievedAt: now.Format(time.RFC3339)}
	if q == "" || len(q) > 500 {
		return out, errors.New("搜索词不能为空或过长")
	}
	if o.Topic == "" {
		o.Topic = "general"
	}
	if o.TimeRange == "" {
		o.TimeRange = "any"
	}
	if o.Topic != "general" && o.Topic != "news" {
		return out, errors.New("topic 仅支持 general/news")
	}
	switch o.TimeRange {
	case "any", "day", "week", "month", "year":
	default:
		return out, errors.New("time_range 仅支持 any/day/week/month/year")
	}

	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 16*time.Second)
	defer cancel()
	type reply struct {
		index   int
		results []SearchResult
		err     error
	}
	replies := make(chan reply, len(s.engines))
	for i, engine := range s.engines {
		go func() { r, e := engine.run(ctx, q, o); replies <- reply{i, r, e} }()
	}
	batches := make([]reply, len(s.engines))
	for i := range batches {
		batches[i].err = errors.New("source timeout")
	}
collect:
	for range s.engines {
		select {
		case r := <-replies:
			batches[r.index] = r
		case <-ctx.Done():
			if parent.Err() != nil {
				return out, parent.Err()
			}
			break collect
		}
	}
	type ranked struct {
		result SearchResult
		score  int
	}
	candidates := []ranked{}
	successful := 0
	for i, b := range batches {
		a := SearchAttempt{Engine: s.engines[i].name, Status: "unavailable"}
		if b.err == nil {
			successful++
			a.Status = "no_relevant_results"
			for _, r := range b.results {
				score := relevance(q, r)
				if !validLink(r.URL) || score == 0 || !inSearchTime(q, r, o, now) {
					continue
				}
				r.Engine = a.Engine
				r.Title = clip(r.Title, 240)
				r.Snippet = clip(r.Snippet, 1200)
				if r.Source == "" {
					u, _ := url.Parse(r.URL)
					r.Source = u.Hostname()
				}
				if published, e := time.Parse(time.RFC3339, r.PublishedAt); e == nil {
					if published.After(now.AddDate(0, 0, -30)) {
						score += 4
					} else if published.After(now.AddDate(-1, 0, 0)) {
						score++
					}
				}
				if o.Topic == "news" && a.Engine == "Google News" {
					score += 3
				}
				candidates = append(candidates, ranked{r, score})
				a.Accepted++
			}
			if a.Accepted > 0 {
				a.Status = "ok"
			}
		}
		out.Attempts = append(out.Attempts, a)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	seen := map[string]bool{}
	domains := map[string]int{}
	providers := []string{}
	unknownDate := false
	for _, c := range candidates {
		r := c.result
		key := canonicalSearchURL(r.URL)
		titleKey := "title:" + strings.ToLower(strings.Join(strings.Fields(r.Title), ""))
		if seen[key] || seen[titleKey] || domains[r.Source] >= 2 {
			continue
		}
		seen[key] = true
		seen[titleKey] = true
		domains[r.Source]++
		if !seen["engine:"+r.Engine] {
			providers = append(providers, r.Engine)
			seen["engine:"+r.Engine] = true
		}
		if r.PublishedAt == "" {
			unknownDate = true
		}
		out.Results = append(out.Results, r)
		if len(out.Results) == 8 {
			break
		}
	}
	out.Provider = strings.Join(providers, " + ")
	out.Status = "ok"
	if len(out.Results) == 0 {
		out.Status = "no_relevant_results"
		out.Notice = "未找到同时符合主题和时间要求的结果。请保留核心主题和年份换词重试，或放宽时间范围；不要用无关内容回答。"
		if successful == 0 {
			out.Status = "unavailable"
			out.Notice = "所有搜索源暂不可用；请稍后重试，不要编造来源。"
		}
	} else {
		if successful < len(s.engines) {
			out.Status = "partial"
			out.Notice = "部分搜索源不可用，已使用其他独立来源。"
		}
		if unknownDate {
			out.Notice += "部分结果没有可确认的发布时间，不能据此认定为最新资料。"
		}
		out.Notice += "搜索摘要不等于已核实事实；涉及排名、日期或数据请用 browser_open 阅读原文并交叉核对。"
	}
	return out, nil
}
func canonicalSearchURL(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return raw
	}
	u.Fragment = ""
	v := u.Query()
	for k := range v {
		if strings.HasPrefix(k, "utm_") || k == "spm" || k == "fbclid" {
			v.Del(k)
		}
	}
	u.RawQuery = v.Encode()
	return u.String()
}
func clip(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}
