// Package billing estimates what ds-search costs: a local ledger of the
// billable calls it made, priced with DeepSeek's and Tavily's published
// prices. The authoritative bills are on their platforms; these are
// estimates.
package billing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Prices, in yuan. Update here when DeepSeek's prices change:
// https://api-docs.deepseek.com/zh-cn/quick_start/pricing (checked 2026-10-19).
var (
	// SearchPrice and FetchPrice are per successful request: 5 元 / 1000.
	SearchPrice = 0.005
	FetchPrice  = 0.005
	// TavilySearchPrice is per search: about $8 / 1000, in yuan at 7.1.
	TavilySearchPrice = 0.057
	// Models are priced per million tokens at peak hours; off-peak is half.
	Models = map[string]ModelPrice{
		"deepseek-flash":  {CacheHit: 0.04, CacheMiss: 2, Output: 8},
		"deepseek-v4-pro": {CacheHit: 0.30, CacheMiss: 9, Output: 27},
	}
	// OffPeakFactor is off-peak price ÷ peak price.
	OffPeakFactor = 0.5
)

// ModelPrice is a model's peak price in yuan per million tokens.
type ModelPrice struct{ CacheHit, CacheMiss, Output float64 }

// Shanghai is Beijing time.
var Shanghai = time.FixedZone("CST", 8*3600)

// Peak tells whether t is in DeepSeek's peak hours: Monday to Friday,
// 9:00–12:00 and 14:00–18:00 Beijing time. Public holidays are off-peak
// too, which this doesn't know of: on those days it overestimates.
func Peak(t time.Time) bool {
	t = t.In(Shanghai)
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	h := t.Hour()
	return (h >= 9 && h < 12) || (h >= 14 && h < 18)
}

// Usage is the tokens of a chat completion, as its usage reports them.
type Usage struct {
	PromptCacheHit  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMiss int `json:"prompt_cache_miss_tokens"`
	Completion      int `json:"completion_tokens"`
}

// ChatCost prices a completion of model at time t. An unknown model is
// priced as deepseek-flash, which serves the old names.
func ChatCost(model string, u Usage, t time.Time) float64 {
	p, ok := Models[model]
	if !ok {
		p = Models["deepseek-flash"]
	}
	c := (float64(u.PromptCacheHit)*p.CacheHit + float64(u.PromptCacheMiss)*p.CacheMiss + float64(u.Completion)*p.Output) / 1e6
	if !Peak(t) {
		c *= OffPeakFactor
	}
	return c
}

// Kind is a kind of billable call.
type Kind string

const (
	Search       Kind = "search"        // DeepSeek 搜索
	TavilySearch Kind = "tavily-search" // Tavily 搜索
	Fetch        Kind = "fetch"         // 阅读
	Chat         Kind = "chat"          // AI 对话: each chat completion
)

// Day is a day's usage.
type Day struct {
	Date    string  `json:"date"` // 2006-01-02, Beijing time
	Search  int     `json:"search"`
	Tavily  int     `json:"tavily,omitempty"`
	Fetch   int     `json:"fetch"`
	Chat    int     `json:"chat"`
	Tokens  int     `json:"tokens,omitempty"`
	Cost    float64 `json:"cost"`               // yuan, all kinds
	ChatC   float64 `json:"chatCost,omitempty"` // yuan, of chats
	TavilyC float64 `json:"tavilyCost,omitempty"`
}

// Ledger keeps a Day per day in a JSON file. Safe for concurrent use.
type Ledger struct {
	mu   sync.Mutex
	path string
	days map[string]*Day
	now  func() time.Time
}

// Open reads the ledger at path, or starts an empty one.
func Open(path string) *Ledger {
	l := &Ledger{path: path, days: map[string]*Day{}, now: time.Now}
	if b, err := os.ReadFile(path); err == nil {
		var days []*Day
		if json.Unmarshal(b, &days) == nil {
			for _, d := range days {
				l.days[d.Date] = d
			}
		}
	}
	return l
}

func (l *Ledger) day(t time.Time) *Day {
	k := t.In(Shanghai).Format("2006-01-02")
	d := l.days[k]
	if d == nil {
		d = &Day{Date: k}
		l.days[k] = d
	}
	return d
}

// AddRequest records a successful Search or Fetch.
func (l *Ledger) AddRequest(k Kind) {
	l.mu.Lock()
	d := l.day(l.now())
	switch k {
	case Search:
		d.Search++
		d.Cost += SearchPrice
	case TavilySearch:
		d.Tavily++
		d.Cost += TavilySearchPrice
		d.TavilyC += TavilySearchPrice
	case Fetch:
		d.Fetch++
		d.Cost += FetchPrice
	}
	l.mu.Unlock()
	l.save()
}

// AddChat records a completion and returns its estimated cost.
func (l *Ledger) AddChat(model string, u Usage) float64 {
	l.mu.Lock()
	now := l.now()
	c := ChatCost(model, u, now)
	d := l.day(now)
	d.Chat++
	d.Tokens += u.PromptCacheHit + u.PromptCacheMiss + u.Completion
	d.Cost += c
	d.ChatC += c
	l.mu.Unlock()
	l.save()
	return c
}

func (l *Ledger) save() {
	if l.path == "" {
		return
	}
	l.mu.Lock()
	days := make([]*Day, 0, len(l.days))
	for _, d := range l.days {
		cp := *d
		days = append(days, &cp)
	}
	l.mu.Unlock()
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	b, _ := json.MarshalIndent(days, "", " ")
	_ = os.MkdirAll(filepath.Dir(l.path), 0o700)
	tmp := l.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, l.path)
	}
}

// Summary is the usage of a span of days.
type Summary struct {
	Search, Tavily, Fetch, Chat int
	Cost, ChatCost, TavilyCost  float64
}

func (s *Summary) add(d *Day) {
	s.Search += d.Search
	s.Tavily += d.Tavily
	s.Fetch += d.Fetch
	s.Chat += d.Chat
	s.Cost += d.Cost
	s.ChatCost += d.ChatC
	s.TavilyCost += d.TavilyC
}

// Calls is how many billable calls the summary counts.
func (s Summary) Calls() int { return s.Search + s.Tavily + s.Fetch + s.Chat }

// Report is today's, this month's and all usage, and the last 30 days.
type Report struct {
	Today, Month, Total Summary
	Last30              []Day // oldest first, one per day, today last
}

func (l *Ledger) Report() Report {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now().In(Shanghai)
	today, month := now.Format("2006-01-02"), now.Format("2006-01")
	var r Report
	for k, d := range l.days {
		r.Total.add(d)
		if k == today {
			r.Today.add(d)
		}
		if k[:7] == month {
			r.Month.add(d)
		}
	}
	for i := 29; i >= 0; i-- {
		k := now.AddDate(0, 0, -i).Format("2006-01-02")
		if d := l.days[k]; d != nil {
			r.Last30 = append(r.Last30, *d)
		} else {
			r.Last30 = append(r.Last30, Day{Date: k})
		}
	}
	return r
}

// SetClock makes the ledger take the time from now, for tests and demos.
func (l *Ledger) SetClock(now func() time.Time) {
	l.mu.Lock()
	l.now = now
	l.mu.Unlock()
}
