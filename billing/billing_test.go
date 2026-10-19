package billing

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, Shanghai)
	if err != nil {
		panic(err)
	}
	return t
}

func TestPeak(t *testing.T) {
	for s, want := range map[string]bool{
		"2026-10-19 09:00": true, // Monday
		"2026-10-19 11:59": true,
		"2026-10-19 12:00": false, // lunch
		"2026-10-19 14:30": true,
		"2026-10-19 18:00": false,
		"2026-10-19 08:59": false,
		"2026-10-18 10:00": false, // Sunday
		"2026-10-24 15:00": false, // Saturday
	} {
		if got := Peak(at(s)); got != want {
			t.Errorf("%s: %v", s, got)
		}
	}
	// 02:00 UTC on Monday is 10:00 in Beijing.
	if !Peak(time.Date(2026, 10, 19, 2, 0, 0, 0, time.UTC)) {
		t.Error("UTC not converted")
	}
}

func TestChatCost(t *testing.T) {
	u := Usage{PromptCacheHit: 1_000_000, PromptCacheMiss: 1_000_000, Completion: 1_000_000}
	if c := ChatCost("deepseek-flash", u, at("2026-10-19 10:00")); !near(c, 0.04+2+8) {
		t.Errorf("flash peak %v", c)
	}
	if c := ChatCost("deepseek-flash", u, at("2026-10-19 20:00")); !near(c, (0.04+2+8)/2) {
		t.Errorf("flash off-peak %v", c)
	}
	if c := ChatCost("deepseek-v4-pro", u, at("2026-10-19 10:00")); !near(c, 0.3+9+27) {
		t.Errorf("pro peak %v", c)
	}
	if c := ChatCost("deepseek-v4-flash", u, at("2026-10-19 10:00")); !near(c, 10.04) {
		t.Errorf("old name priced as flash: %v", c)
	}
	// A typical summary: 3000 tokens in, 400 out, at peak = ¥0.0092.
	small := Usage{PromptCacheMiss: 3000, Completion: 400}
	if c := ChatCost("deepseek-flash", small, at("2026-10-19 10:00")); !near(c, 0.006+0.0032) {
		t.Errorf("typical %v", c)
	}
}

func TestLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	l := Open(path)
	now := at("2026-10-19 10:00")
	l.now = func() time.Time { return now }
	l.AddRequest(Search)
	l.AddRequest(Search)
	l.AddRequest(Fetch)
	l.AddChat("deepseek-flash", Usage{PromptCacheMiss: 3000, Completion: 400})
	now = at("2026-10-02 21:00")
	l.AddRequest(Search)
	now = at("2026-09-30 21:00")
	l.AddRequest(Search)
	now = at("2026-10-19 23:00")

	l2 := Open(path) // persisted
	l2.now = l.now
	r := l2.Report()
	if r.Today.Search != 2 || r.Today.Fetch != 1 || r.Today.Chat != 1 || !near(r.Today.Cost, 0.015+0.0092) {
		t.Errorf("today %+v", r.Today)
	}
	if r.Month.Search != 3 || !near(r.Month.Cost, 0.02+0.0092) {
		t.Errorf("month %+v", r.Month)
	}
	if r.Total.Search != 4 || r.Total.Calls() != 6 || !near(r.Total.Cost, 0.025+0.0092) {
		t.Errorf("total %+v", r.Total)
	}
	if len(r.Last30) != 30 || r.Last30[29].Date != "2026-10-19" || r.Last30[29].Search != 2 || r.Last30[0].Date != "2026-09-20" {
		t.Errorf("last30 %v … %v", r.Last30[0], r.Last30[29])
	}
}
