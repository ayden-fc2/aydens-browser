package search

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var searchYear = regexp.MustCompile(`\b(20[0-9]{2})年?|(20[0-9]{2})年`)
var queryNoise = strings.NewReplacer("请帮我", " ", "帮我", " ", "搜索一下", " ", "搜一下", " ", "看看", " ", "今年", " ", "最新", " ", "推荐", " ", "有哪些", " ", "怎么样", " ")

// Conservative lexical screening removes completely off-topic engine responses.
// It is not a semantic fact checker; the agent must still read and compare sources.
func queryGroups(q string) [][]string {
	q = strings.ToLower(q)
	q = searchYear.ReplaceAllString(q, " ")
	q = queryNoise.Replace(q)
	fields := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	groups := [][]string{}
	for _, f := range fields {
		if f == "and" || f == "for" || f == "a" || f == "is" {
			continue
		}
		tokens := []string{}
		run := []rune{}
		flush := func() {
			if len(run) > 1 {
				for i := 0; i < len(run)-1; i++ {
					tokens = append(tokens, string(run[i:i+2]))
				}
			} else if len(run) == 1 {
				tokens = append(tokens, string(run))
			}
			run = nil
		}
		latin := ""
		for _, r := range f {
			if unicode.Is(unicode.Han, r) {
				if latin != "" {
					tokens = append(tokens, latin)
					latin = ""
				}
				run = append(run, r)
			} else {
				flush()
				latin += string(r)
			}
		}
		flush()
		if latin != "" {
			tokens = append(tokens, latin)
		}
		if len(tokens) > 0 {
			groups = append(groups, tokens)
		}
	}
	return groups
}
func relevance(q string, r SearchResult) int {
	body := strings.ToLower(r.Title + " " + r.Snippet)
	groups := queryGroups(q)
	matched, score := 0, 0
	for _, g := range groups {
		hits := 0
		for _, token := range g {
			if strings.Contains(body, token) {
				hits++
				score++
				if strings.Contains(strings.ToLower(r.Title), token) {
					score++
				}
			}
		}
		if hits > 0 {
			matched++
		}
	}
	required := min(2, len(groups))
	if required == 0 || matched < required {
		return 0
	}
	// A long phrase without whitespace must share more than a single common bigram.
	if len(groups) == 1 && len(groups[0]) >= 4 && score < 4 {
		return 0
	}
	return score
}
func inSearchTime(q string, r SearchResult, o SearchOptions, now time.Time) bool {
	var published time.Time
	if r.PublishedAt != "" {
		published, _ = time.Parse(time.RFC3339, r.PublishedAt)
		if published.After(now.Add(24 * time.Hour)) {
			return false
		}
	}
	if o.TimeRange != "any" && o.TimeRange != "" {
		days := map[string]int{"day": 1, "week": 7, "month": 30, "year": 365}[o.TimeRange]
		// A strict recency request cannot be satisfied by a page with an unknown date.
		if published.IsZero() || published.Before(now.AddDate(0, 0, -days)) {
			return false
		}
	}
	match := searchYear.FindString(q)
	if match != "" {
		year, _ := strconv.Atoi(strings.TrimSuffix(match, "年"))
		target := strconv.Itoa(year)
		// A previous year's holiday/ranking is not evidence for this year's request.
		titleYear := searchYear.FindString(r.Title)
		if titleYear != "" && !strings.Contains(titleYear, target) {
			return false
		}
		if !published.IsZero() && published.Year() < year && !strings.Contains(r.Title, target) {
			return false
		}
	}
	return true
}
