package search

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

func attr(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}
func hasClass(n *html.Node, c string) bool {
	for _, v := range strings.Fields(attr(n, "class")) {
		if v == c {
			return true
		}
	}
	return false
}
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "noscript") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
func descendant(n *html.Node, match func(*html.Node) bool) *html.Node {
	for c := range n.Descendants() {
		if match(c) {
			return c
		}
	}
	return nil
}
func textOf(n *html.Node) string {
	if n == nil {
		return ""
	}
	return nodeText(n)
}
func duckSearch(ctx context.Context, c *http.Client, q string, o SearchOptions) ([]SearchResult, error) {
	v := url.Values{"q": {q}, "kl": {"cn-zh"}}
	if d := map[string]string{"day": "d", "week": "w", "month": "m", "year": "y"}[o.TimeRange]; d != "" {
		v.Set("df", d)
	}
	b, e := fetchSearch(ctx, c, "https://html.duckduckgo.com/html/?"+v.Encode(), "")
	if e != nil {
		return nil, e
	}
	return parseDuck(b)
}
func parseDuck(b []byte) ([]SearchResult, error) {
	doc, e := html.Parse(strings.NewReader(string(b)))
	if e != nil {
		return nil, e
	}
	out := []SearchResult{}
	for n := range doc.Descendants() {
		if !hasClass(n, "result") || hasClass(n, "result--ad") {
			continue
		}
		a := descendant(n, func(n *html.Node) bool { return hasClass(n, "result__a") })
		if a == nil {
			continue
		}
		raw := attr(a, "href")
		u, e := url.Parse(raw)
		if e != nil {
			continue
		}
		if target := u.Query().Get("uddg"); target != "" {
			raw = target
		}
		if strings.Contains(raw, "duckduckgo.com/y.js") || !validLink(raw) {
			continue
		}
		snippet := textOf(descendant(n, func(n *html.Node) bool { return hasClass(n, "result__snippet") }))
		out = append(out, SearchResult{Title: nodeText(a), URL: raw, Snippet: snippet})
	}
	if len(out) == 0 && !strings.Contains(string(b), "No results") {
		return nil, errors.New("DuckDuckGo blocked or changed format")
	}
	return out, nil
}
func newsSearch(ctx context.Context, c *http.Client, q string, o SearchOptions) ([]SearchResult, error) {
	if d := map[string]string{"day": "1d", "week": "7d", "month": "30d", "year": "365d"}[o.TimeRange]; d != "" {
		q += " when:" + d
	}
	v := url.Values{"q": {q}, "hl": {"zh-CN"}, "gl": {"CN"}, "ceid": {"CN:zh-Hans"}}
	b, e := fetchSearch(ctx, c, "https://news.google.com/rss/search?"+v.Encode(), "")
	if e != nil {
		return nil, e
	}
	return parseNews(b)
}
func parseNews(b []byte) ([]SearchResult, error) {
	var feed struct {
		XMLName xml.Name `xml:"rss"`
		Items   []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
			Source      string `xml:"source"`
		} `xml:"channel>item"`
	}
	if e := xml.Unmarshal(b, &feed); e != nil {
		return nil, e
	}
	out := []SearchResult{}
	for _, r := range feed.Items {
		// RSS descriptions may contain HTML. Only return text, never executable markup.
		doc, _ := html.Parse(strings.NewReader(r.Description))
		snippet := textOf(doc)
		published := ""
		if t, e := http.ParseTime(r.PubDate); e == nil {
			published = t.UTC().Format(time.RFC3339)
		}
		out = append(out, SearchResult{Title: r.Title, URL: r.Link, Snippet: snippet, Source: r.Source, PublishedAt: published})
		if len(out) == 30 {
			break
		}
	}
	return out, nil
}
func baiduSearch(ctx context.Context, c *http.Client, q string, o SearchOptions) ([]SearchResult, error) {
	b, e := fetchSearch(ctx, c, "https://www.baidu.com/s?"+url.Values{"wd": {q}, "rn": {"10"}}.Encode(), "")
	if e != nil {
		return nil, e
	}
	return parseBaidu(b)
}
func parseBaidu(b []byte) ([]SearchResult, error) {
	doc, e := html.Parse(strings.NewReader(string(b)))
	if e != nil {
		return nil, e
	}
	out := []SearchResult{}
	for n := range doc.Descendants() {
		// Organic cards expose their canonical destination in mu; ads have other containers.
		if !hasClass(n, "result") || !hasClass(n, "c-container") {
			continue
		}
		raw := attr(n, "mu")
		if !validLink(raw) {
			continue
		}
		h := descendant(n, func(n *html.Node) bool { return n.Type == html.ElementNode && n.Data == "h3" })
		if h == nil || hasClass(h, "ec_title") {
			continue
		}
		out = append(out, SearchResult{Title: nodeText(h), URL: raw, Snippet: clip(nodeText(n), 1200)})
	}
	if len(out) == 0 {
		return nil, errors.New("Baidu blocked or changed format")
	}
	return out, nil
}
func searxSearch(ctx context.Context, c *http.Client, base, q string, o SearchOptions) ([]SearchResult, error) {
	v := url.Values{"q": {q}, "format": {"json"}, "language": {"zh-CN"}, "categories": {o.Topic}}
	if o.TimeRange != "any" {
		v.Set("time_range", o.TimeRange)
	}
	b, e := fetchSearch(ctx, c, strings.TrimRight(base, "/")+"/search?"+v.Encode(), "")
	if e != nil {
		return nil, e
	}
	var data struct {
		Results []struct {
			Title     string `json:"title"`
			URL       string `json:"url"`
			Content   string `json:"content"`
			Published string `json:"publishedDate"`
		} `json:"results"`
	}
	if e = json.Unmarshal(b, &data); e != nil {
		return nil, e
	}
	out := []SearchResult{}
	for _, r := range data.Results {
		date := ""
		if t, e := time.Parse(time.RFC3339, r.Published); e == nil {
			date = t.UTC().Format(time.RFC3339)
		}
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content, PublishedAt: date})
	}
	return out, nil
}
