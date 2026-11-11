// Package websearch adapts web search providers to the app's results.
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"ds-search/dsapi"
)

const DefaultTavilyURL = "https://api.tavily.com"

// ErrNoKey is returned when Tavily has no API key.
var ErrNoKey = errors.New("还没有设置 Tavily API Key")

// Error is an error Tavily answered with.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	human := map[int]string{
		400: "搜索词无效", 401: "Tavily API Key 无效，请在设置里重新填写",
		429: "Tavily 请求太频繁，稍后再试", 432: "Tavily 套餐额度已用完",
		433: "Tavily 按量付费额度已用完",
	}[e.Status]
	if human == "" {
		human = fmt.Sprintf("Tavily 错误 %d", e.Status)
	}
	if e.Message != "" {
		return human + "（" + e.Message + "）"
	}
	return human
}

// Tavily searches the web with Tavily's Search API.
// https://docs.tavily.com/documentation/api-reference/endpoint/search
type Tavily struct {
	Key     string
	BaseURL string
	HTTP    *http.Client
}

func NewTavily(key string) *Tavily {
	return &Tavily{Key: strings.TrimSpace(key), BaseURL: DefaultTavilyURL, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (t *Tavily) Name() string { return "tavily" }

// Check verifies the key with one minimal search.
func (t *Tavily) Check(ctx context.Context) error {
	_, err := t.Search(ctx, "web", 1)
	return err
}

func (t *Tavily) base() string {
	if t.BaseURL == "" {
		return DefaultTavilyURL
	}
	return strings.TrimRight(t.BaseURL, "/")
}

// Search searches the web. Tavily returns at most 20 results per request.
func (t *Tavily) Search(ctx context.Context, query string, count int) ([]dsapi.Result, error) {
	if t.Key == "" {
		return nil, ErrNoKey
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, &Error{Status: 400, Message: "搜索词不能为空"}
	}
	if count <= 0 {
		count = 10
	}
	q := translate(query)
	body := map[string]any{
		"query": q.text, "max_results": min(count, 20),
		"search_depth": "basic", "include_answer": false, "include_raw_content": false,
	}
	if len(q.include) > 0 {
		body["include_domains"] = q.include
	}
	if len(q.exclude) > 0 {
		body["exclude_domains"] = q.exclude
	}
	if q.start != "" {
		body["start_date"] = q.start
	}
	if q.end != "" {
		body["end_date"] = q.end
	}
	var out struct {
		Results []struct {
			Title         string  `json:"title"`
			URL           string  `json:"url"`
			Content       string  `json:"content"`
			Score         float64 `json:"score"`
			PublishedDate string  `json:"published_date"`
		} `json:"results"`
	}
	if err := t.post(ctx, "/search", body, &out); err != nil {
		return nil, err
	}
	rs := make([]dsapi.Result, 0, len(out.Results))
	for _, r := range out.Results {
		u, err := url.Parse(r.URL)
		if err != nil || u.Host == "" {
			continue
		}
		rs = append(rs, dsapi.Result{
			Title: r.Title, URL: u.String(), Snippet: strings.TrimSpace(r.Content),
			SiteName: u.Hostname(), Date: published(r.PublishedDate),
			// Tavily's score is relevance, not DeepSeek's rerank/authority:
			// leave those unknown so the UI adds no badges it cannot back.
			Authority: 0,
		})
	}
	return rs, nil
}

func published(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC().Format(time.RFC3339)
			return &u
		}
	}
	return nil
}

func (t *Tavily) post(ctx context.Context, path string, body any, out any) error {
	b, _ := json.Marshal(body)
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(400<<attempt) * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base()+path, bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+t.Key)
		req.Header.Set("Content-Type", "application/json")
		hc := t.HTTP
		if hc == nil {
			hc = http.DefaultClient
		}
		res, err := hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			last = fmt.Errorf("网络错误：%w", err)
			continue
		}
		if res.StatusCode == http.StatusOK {
			err := json.NewDecoder(res.Body).Decode(out)
			res.Body.Close()
			return err
		}
		e := apiError(res)
		res.Body.Close()
		if e.Status != 429 && e.Status < 500 {
			return e
		}
		last = e
	}
	return last
}

func apiError(res *http.Response) *Error {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var e struct {
		Detail  any    `json:"detail"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(b, &e)
	msg := e.Message
	if msg == "" {
		msg = fmt.Sprint(e.Detail)
	}
	if msg == "<nil>" {
		msg = ""
	}
	return &Error{Status: res.StatusCode, Message: msg}
}

// query is a Tavily search: its natural-language text, plus the filters
// that DeepSeek's query syntax expresses and Tavily's JSON fields do.
type query struct {
	text       string
	include    []string
	exclude    []string
	start, end string
}

func translate(in string) query {
	var q query
	var words []string
	for _, word := range splitQuery(in) {
		lower := strings.ToLower(word)
		switch {
		case strings.HasPrefix(lower, "-site:"):
			q.exclude = appendDomain(q.exclude, word[len("-site:"):])
		case strings.HasPrefix(lower, "site:"):
			q.include = appendDomain(q.include, word[len("site:"):])
		case strings.HasPrefix(lower, "after:"):
			q.start = date(word[len("after:"):], false)
		case strings.HasPrefix(lower, "before:"):
			q.end = date(word[len("before:"):], true)
		case strings.HasPrefix(lower, "lang:"):
			// Tavily has no language filter; the model still answers in Chinese.
		default:
			words = append(words, word)
		}
	}
	q.text = strings.TrimSpace(strings.Join(words, " "))
	if q.text == "" {
		q.text = "web"
	}
	return q
}

func appendDomain(ds []string, d string) []string {
	d = strings.Trim(strings.TrimSpace(d), `"`)
	d = strings.TrimPrefix(d, "www.")
	if d == "" || strings.ContainsAny(d, " /\\") {
		return ds
	}
	for _, have := range ds {
		if have == d {
			return ds
		}
	}
	return append(ds, d)
}

func date(s string, end bool) string {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			if end {
				switch layout {
				case "2006":
					t = time.Date(t.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
				case "2006-01":
					t = time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC)
				}
			}
			return t.Format("2006-01-02")
		}
	}
	return ""
}

func splitQuery(s string) []string {
	var words []string
	var b strings.Builder
	quoted := false
	flush := func() {
		if b.Len() > 0 {
			words = append(words, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			b.WriteRune(r)
		case unicode.IsSpace(r) && !quoted:
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return words
}
