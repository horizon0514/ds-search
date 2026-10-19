package dsapi

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests call the real API (a few searches, about ¥0.03) with
// DEEPSEEK_API_KEY; they are skipped without it.
func live(t *testing.T) *Client {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" || testing.Short() {
		t.Skip("DEEPSEEK_API_KEY not set")
	}
	return New(key)
}

func TestSearchLive(t *testing.T) {
	c := live(t)
	for _, q := range []string{"DeepSeek V4", "Go 1.27 release notes site:go.dev", "人工智能 新闻 after:2026-09-01", `"MyGo" egoist -site:x.com`} {
		start := time.Now()
		rs, err := c.Search(context.Background(), q, 8)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		t.Logf("%-45q %d results in %v", q, len(rs), time.Since(start).Round(time.Millisecond))
		if len(rs) == 0 {
			t.Errorf("%s: no results", q)
			continue
		}
		for _, r := range rs[:min(3, len(rs))] {
			t.Logf("   %.2f a%d %s | %s | %s", r.RerankScore, r.Authority, r.SiteName, trim(r.Title, 40), r.Time().In(Shanghai).Format("2006-01-02"))
		}
		if strings.Contains(q, "site:go.dev") {
			for _, r := range rs {
				if !strings.Contains(r.URL, "go.dev") {
					t.Errorf("site: not honored: %s", r.URL)
				}
			}
		}
		if strings.Contains(q, "after:") {
			for _, r := range rs {
				if tm := r.Time(); !tm.IsZero() && tm.Before(time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)) {
					t.Logf("   note: result dated %v before after:", tm)
				}
			}
		}
	}
}

func TestFetchLive(t *testing.T) {
	c := live(t)
	start := time.Now()
	p, err := c.Fetch(context.Background(), "https://go.dev/doc/")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fetch %q: %d chars in %v", p.Title, len(p.Content), time.Since(start).Round(time.Millisecond))
	if len(p.Content) < 200 {
		t.Errorf("content too short: %q", p.Content)
	}
}

func TestAnswerLive(t *testing.T) {
	c := live(t)
	rs, err := c.Search(context.Background(), "DeepSeek 搜索 API 价格", 6)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var first time.Duration
	var b strings.Builder
	n := 0
	usage, err := c.Answer(context.Background(), "DeepSeek 搜索 API 价格", rs, func(s string) {
		if n == 0 {
			first = time.Since(start)
		}
		n++
		b.WriteString(s)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("answer: %d chunks, first token %v, total %v\n%s", n, first.Round(time.Millisecond), time.Since(start).Round(time.Millisecond), b.String())
	if usage == nil || usage.Completion == 0 || usage.PromptCacheHit+usage.PromptCacheMiss == 0 {
		t.Errorf("no usage: %+v", usage)
	} else {
		t.Logf("usage %+v", *usage)
	}
	if n < 3 || !strings.Contains(b.String(), "[") {
		t.Errorf("expected a streamed answer with citations")
	}
}

func TestBalanceLive(t *testing.T) {
	c := live(t)
	b, err := c.GetBalance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Infos) == 0 {
		t.Fatalf("no balance infos: %+v", b)
	}
	t.Logf("available %v, currencies %d (amounts not logged)", b.Available, len(b.Infos))
}

func TestCount50Live(t *testing.T) {
	c := live(t)
	rs, err := c.Search(context.Background(), "DeepSeek 搜索 API", 50)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("count=50: %d results", len(rs))
	if len(rs) <= 10 {
		t.Errorf("count=50 returned only %d", len(rs))
	}
}

func TestTopForAnswer(t *testing.T) {
	var rs []Result
	for i := 0; i < 30; i++ {
		rs = append(rs, Result{URL: string(rune('a' + i)), RerankScore: float64(i % 7)})
	}
	top := TopForAnswer(rs)
	if len(top) != MaxAnswerSources {
		t.Fatalf("%d", len(top))
	}
	for i, r := range top {
		if r.RerankScore < 4 {
			t.Errorf("kept a low score %v", r.RerankScore)
		}
		if i > 0 && top[i-1].URL > r.URL {
			t.Errorf("order not kept")
		}
	}
}

func TestBadKey(t *testing.T) {
	live(t)
	c := New("sk-invalid")
	_, err := c.Search(context.Background(), "test", 1)
	e, ok := err.(*Error)
	if !ok || e.Status != 401 {
		t.Fatalf("want 401, got %v", err)
	}
	t.Log(e)
}

func TestValidation(t *testing.T) {
	c := New("x")
	if _, err := c.Search(context.Background(), strings.Repeat("字", 129), 1); err == nil {
		t.Error("129 chars accepted")
	}
	if _, err := c.Fetch(context.Background(), "file:///etc/passwd"); err == nil {
		t.Error("file URL accepted")
	}
	if _, err := New("").Search(context.Background(), "x", 1); err != ErrNoKey {
		t.Errorf("no key: %v", err)
	}
}
