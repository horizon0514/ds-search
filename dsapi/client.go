// Package dsapi calls DeepSeek's Search, Fetch and chat APIs directly.
// https://api-docs.deepseek.com/zh-cn/guides/search
package dsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const DefaultBaseURL = "https://api.deepseek.com"
const DefaultModel = "deepseek-flash"

// Result is one search result.
type Result struct {
	RerankScore float64 `json:"rerank_score"` // 0–3, ≥1.5 is good
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Content     string  `json:"content"`
	SiteName    string  `json:"site_name"`
	SiteIcon    string  `json:"site_icon"`
	Date        *string `json:"date"`      // UTC RFC 3339, or nil
	Authority   int     `json:"authority"` // 0–4, -1 unknown, ≥3 very authoritative
}

// Time is the result's date, zero when unknown.
func (r Result) Time() time.Time {
	if r.Date == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, *r.Date)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Good tells whether the result is of high quality.
func (r Result) Good() bool { return r.RerankScore >= 1.5 }

// Authoritative tells whether the source is very authoritative.
func (r Result) Authoritative() bool { return r.Authority >= 3 }

// Page is a fetched page.
type Page struct {
	Title   string `json:"title"`
	Content string `json:"content"` // Markdown
}

// Error is an error the API answered with.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	human := map[int]string{
		400: "请求无效", 401: "API Key 无效，请在设置里重新填写", 402: "DeepSeek 账号余额不足，请充值",
		403: "该网页被安全策略拒绝抓取", 404: "无法获取该网页的正文", 408: "请求超时",
		429: "请求太频繁，稍后再试", 504: "上游超时",
	}[e.Status]
	if human == "" {
		human = fmt.Sprintf("DeepSeek 错误 %d", e.Status)
	}
	if e.Message != "" {
		return human + "（" + e.Message + "）"
	}
	return human
}

// ErrNoKey is returned when no API key is set.
var ErrNoKey = errors.New("还没有设置 DeepSeek API Key")

// Client calls the APIs. Safe for concurrent use.
type Client struct {
	Key     string
	BaseURL string
	Model   string
	HTTP    *http.Client

	mu   sync.Mutex
	next map[string]time.Time // rate limit per tool
}

func New(key string) *Client {
	return &Client{Key: key, BaseURL: DefaultBaseURL, Model: DefaultModel, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// wait keeps each tool under 5 requests a second (an account's limit).
func (c *Client) wait(ctx context.Context, tool string) error {
	c.mu.Lock()
	if c.next == nil {
		c.next = map[string]time.Time{}
	}
	now := time.Now()
	at := c.next[tool]
	if at.Before(now) {
		at = now
	}
	c.next[tool] = at.Add(220 * time.Millisecond)
	c.mu.Unlock()
	if d := time.Until(at); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	if c.Key == "" {
		return nil, ErrNoKey
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	return hc.Do(req)
}

func apiError(res *http.Response) *Error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	_ = json.Unmarshal(b, &e)
	return &Error{Status: res.StatusCode, Message: e.Error.Message}
}

func retryable(status int) bool { return status == 408 || status == 429 || status >= 500 }

// tool calls a tool, retrying on 408, 429 and 5xx.
func (c *Client) tool(ctx context.Context, name string, body, out any) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(400<<attempt) * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err := c.wait(ctx, name); err != nil {
			return err
		}
		res, err := c.post(ctx, "/v1/tools/"+name, body)
		if err != nil {
			if errors.Is(err, ErrNoKey) || ctx.Err() != nil {
				return err
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
		if !retryable(e.Status) {
			return e
		}
		last = e
	}
	return last
}

// Search searches the web. count is 1–50 (0 for 10).
func (c *Client) Search(ctx context.Context, query string, count int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, &Error{Status: 400, Message: "搜索词不能为空"}
	}
	if utf8.RuneCountInString(query) > 128 {
		return nil, &Error{Status: 400, Message: "搜索词最多 128 个字符"}
	}
	if count <= 0 {
		count = 10
	}
	var out struct {
		Results []Result `json:"results"`
	}
	err := c.tool(ctx, "search", map[string]any{"query": query, "count": min(count, 50), "need_content": false}, &out)
	return out.Results, err
}

// Fetch reads a page as Markdown.
func (c *Client) Fetch(ctx context.Context, raw string) (Page, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return Page{}, &Error{Status: 400, Message: "只支持 http/https 链接"}
	}
	var p Page
	err = c.tool(ctx, "fetch", map[string]any{"url": u.String()}, &p)
	return p, err
}

// MaxAnswerSources is how many results the summary reads at most, so that
// 50 results cost no more, nor take longer, than 10.
const MaxAnswerSources = 10

// TopForAnswer picks the results a summary reads: the MaxAnswerSources
// with the best rerank_score, in the order the search returned them. The
// summary's [n] refers to the n-th of these.
func TopForAnswer(rs []Result) []Result {
	if len(rs) <= MaxAnswerSources {
		return rs
	}
	idx := make([]int, len(rs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return rs[idx[a]].RerankScore > rs[idx[b]].RerankScore })
	keep := idx[:MaxAnswerSources]
	sort.Ints(keep)
	out := make([]Result, 0, MaxAnswerSources)
	for _, i := range keep {
		out = append(out, rs[i])
	}
	return out
}

// Shanghai is UTC+8, China Standard Time (no daylight saving).
var Shanghai = time.FixedZone("CST", 8*3600)

func trim(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// Usage is the tokens a chat completion used.
type Usage struct {
	PromptCacheHit  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMiss int `json:"prompt_cache_miss_tokens"`
	Completion      int `json:"completion_tokens"`
}

// ModelName is the chat model the client uses.
func (c *Client) ModelName() string {
	if c.Model == "" {
		return DefaultModel
	}
	return c.Model
}

// Answer streams a concise Chinese answer to query from results' summaries,
// calling onDelta with each piece of text as it comes. It returns the
// tokens used, for billing, when the API reported them.
func (c *Client) Answer(ctx context.Context, query string, results []Result, onDelta func(string)) (*Usage, error) {
	r, err := c.Chat(ctx, FirstMessages(query, TopForAnswer(results), "", time.Now()), nil, onDelta)
	return r.Usage, err
}

// Balance is the account's balance in one currency.
type Balance struct {
	Currency string `json:"currency"` // CNY or USD
	Total    string `json:"total_balance"`
	Granted  string `json:"granted_balance"`
	ToppedUp string `json:"topped_up_balance"`
}

// AccountBalance is what GET /user/balance answers.
type AccountBalance struct {
	Available bool      `json:"is_available"`
	Infos     []Balance `json:"balance_infos"`
}

// GetBalance reads the account's balance.
func (c *Client) GetBalance(ctx context.Context) (AccountBalance, error) {
	var b AccountBalance
	if c.Key == "" {
		return b, ErrNoKey
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/user/balance", nil)
	if err != nil {
		return b, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Accept", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return b, fmt.Errorf("网络错误：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return b, apiError(res)
	}
	err = json.NewDecoder(res.Body).Decode(&b)
	return b, err
}

// Check verifies a key cheaply, with a one-token chat (not billed as a search).
func (c *Client) Check(ctx context.Context) error {
	res, err := c.post(ctx, "/chat/completions", map[string]any{
		"model": c.Model, "messages": []map[string]string{{"role": "user", "content": "1"}},
		"max_tokens": 1, "thinking": map[string]string{"type": "disabled"},
	})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return apiError(res)
	}
	return nil
}
