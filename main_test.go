package main

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ds-search/billing"
	"ds-search/dsapi"

	"github.com/egoist/mygo/ui"
)

type fake struct {
	results []dsapi.Result
	answer  []string      // the deltas of a reply that calls no tool
	replies []dsapi.Reply // scripted replies, first; then answer
	chats   [][]dsapi.Message
	offered []int         // tools offered, by call
	block   chan struct{} // when set, Chat waits on it (or its ctx)
	byQuery map[string][]dsapi.Result
	mu      sync.Mutex // for the tests that run goroutines
	page    dsapi.Page
	queries []string
	counts  []int
	fail50  error
	balance string
}

func (f *fake) Search(_ context.Context, q string, n int) ([]dsapi.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	f.counts = append(f.counts, n)
	if rs, ok := f.byQuery[q]; ok {
		return rs[:min(n, len(rs))], nil
	}
	if n > 10 && f.fail50 != nil {
		return nil, f.fail50
	}
	return f.results[:min(n, len(f.results))], nil
}
func (f *fake) Fetch(context.Context, string) (dsapi.Page, error) { return f.page, nil }
func (f *fake) Chat(ctx context.Context, msgs []dsapi.Message, tools []dsapi.Tool, d func(string)) (dsapi.Reply, error) {
	f.mu.Lock()
	f.chats = append(f.chats, append([]dsapi.Message(nil), msgs...))
	f.offered = append(f.offered, len(tools))
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return dsapi.Reply{}, ctx.Err()
		}
	}
	u := &dsapi.Usage{PromptCacheMiss: 3000, Completion: 400}
	if len(f.replies) > 0 {
		r := f.replies[0]
		f.replies = f.replies[1:]
		if r.Content != "" && d != nil {
			d(r.Content)
		}
		r.Usage = u
		return r, nil
	}
	var b strings.Builder
	for _, s := range f.answer {
		b.WriteString(s)
		if d != nil {
			d(s)
		}
	}
	return dsapi.Reply{Content: b.String(), Usage: u}, nil
}
func (f *fake) Check(context.Context) error { return nil }
func (f *fake) ModelName() string           { return "deepseek-flash" }
func (f *fake) GetBalance(context.Context) (dsapi.AccountBalance, error) {
	b := f.balance
	if b == "" {
		b = "98.30"
	}
	return dsapi.AccountBalance{Available: true, Infos: []dsapi.Balance{{Currency: "CNY", Total: b}}}, nil
}

func str(s string) *string { return &s }

func testApp(src source) (*app, *[]string) {
	a := newApp(defaultConfig(), "k", "keychain", func(string) source { return src })
	a.keys = keyStore{secrets: &memSecrets{}, dir: os.TempDir() + "/ds-search-test", env: func(string) string { return "" }}
	a.spawn = func(fn func()) { fn() }
	a.icons = map[string]*ui.Bitmap{}
	a.asked = map[string]bool{"": true}
	opened := &[]string{}
	a.open = func(u string) { *opened = append(*opened, u) }
	return a, opened
}

var canned = []dsapi.Result{
	{Title: "Go 1.27 Release Notes", URL: "https://go.dev/doc/go1.27", Snippet: "The latest Go release, version 1.27, arrives six months after Go 1.26.", SiteName: "The Go Programming Language", RerankScore: 2.5, Authority: 3, Date: str("2026-08-19T00:00:00Z")},
	{Title: "Go 1.27 新特性一览", URL: "https://example.cn/go127", Snippet: "Go 1.27 带来了新的迭代器、泛型改进……", SiteName: "示例博客", RerankScore: 1.1, Authority: 1},
}

// A search summarizes by default: Enter asks, and the answer arrives on
// the right over the sources on the left. ⇧⇥ closes that pane, and then a
// search costs nothing but the search.
func TestSearchSummarizesByDefault(t *testing.T) {
	f := &fake{results: canned, answer: []string{"Go 1.27 于 8 月发布", " [1]。"}}
	a, opened := testApp(f)
	var expanded []bool
	a.resize = func(e bool) { expanded = append(expanded, e) }
	tt := ui.NewTester(a.view, expandedWidth, panelHeight)
	tt.Type("go 1.27")
	tt.Key(0, ui.KeyEnter)
	if len(f.queries) != 1 || !tt.HasText("Go 1.27 Release Notes") {
		t.Fatalf("queries %v, texts %q", f.queries, tt.Texts())
	}
	if !tt.HasText("优质") || !tt.HasText("权威") {
		t.Errorf("no quality badges: %q", tt.Texts())
	}
	// The summary came with the search, and the sources show what it read.
	if len(f.chats) != 1 || len(a.turns) != 1 || a.turns[0].busy {
		t.Fatalf("searched without a summary: chats %d turns %d", len(f.chats), len(a.turns))
	}
	if a.turns[0].answer != "Go 1.27 于 8 月发布 [1]。" {
		t.Errorf("answer %q", a.turns[0].answer)
	}
	if got := citeLinks(a.turns[0].answer, len(a.turns[0].sources)); !strings.Contains(got, "[[1]](cite:1)") {
		t.Errorf("citations: %s", got)
	}
	if !tt.HasText("AI 总结") || !tt.HasText("来源 · 2 条") {
		t.Errorf("no summary and source headings: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyDown)
	if a.chosen != 1 {
		t.Errorf("chosen %d after Down", a.chosen)
	}
	tt.Key(0, ui.KeyEnter)
	if len(*opened) != 1 || (*opened)[0] != canned[1].URL || len(f.queries) != 1 {
		t.Errorf("opened %v, queries %v", *opened, f.queries)
	}
	// ⇥ is the conversation, ⇥ again the sources, with nothing spent twice.
	tt.Key(0, ui.KeyTab)
	if !tt.Focused("追问") {
		t.Fatalf("Tab did not focus the follow-up box")
	}
	tt.Key(0, ui.KeyTab)
	if !tt.Focused("搜索结果") {
		t.Error("Tab in the conversation did not go to the results")
	}
	tt.Key(0, ui.KeyTab)
	if !tt.Focused("追问") || len(f.chats) != 1 {
		t.Errorf("Tab on the results: follow-up focused %v, chats %d", tt.Focused("追问"), len(f.chats))
	}
	// ⇧⇥ closes the conversation: the results are all there is again.
	tt.Key(ui.Shift, ui.KeyTab)
	tt.SetSize(panelWidth, panelHeight)
	if a.cfg.AI || len(expanded) != 1 || expanded[0] {
		t.Errorf("⇧⇥: AI %v resized %v", a.cfg.AI, expanded)
	}
	chats := len(f.chats)
	tt.Type("2")
	tt.Key(0, ui.KeyEnter)
	if len(f.chats) != chats {
		t.Errorf("searched with the conversation closed, yet chats %d", len(f.chats))
	}
	// Esc hides at once: no pane to collapse first.
	hidden := 0
	a.hide = func() { hidden++ }
	tt.Key(0, ui.KeyEscape)
	if hidden != 1 {
		t.Errorf("esc hid %d times", hidden)
	}
	if !slices.Contains(a.cfg.History, "go 1.27") {
		t.Errorf("history %v", a.cfg.History)
	}
}

func TestCitations(t *testing.T) {
	rs := canned
	for in, want := range map[string]string{
		"a [1][2] b": "a [[1]](https://go.dev/doc/go1.27)[[2]](https://example.cn/go127) b",
		"a [3] b":    "a [3] b",
		"[1](x) [1":  "[1](x) [1",
		"数组 [0] 不变":  "数组 [0] 不变",
	} {
		if got := citations(in, rs); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestUnescape(t *testing.T) {
	in := "\\# 标题\n\n\\- a\n\\- b\n\nhttps\\:\\/\\/x\\.com \\| \\*星\\*"
	want := "# 标题\n\n- a\n- b\n\nhttps://x.com | \\*星\\*"
	if got := unescapeMD(in); got != want {
		t.Errorf("%q", got)
	}
}

func TestWhen(t *testing.T) {
	now := time.Date(2026, 10, 19, 11, 0, 0, 0, dsapi.Shanghai)
	r := dsapi.Result{Date: str("2026-10-19T01:30:00Z")} // 09:30 Beijing
	if got := when(r, now); got != "1 小时前" {
		t.Errorf("%q", got)
	}
	r.Date = str("2026-10-18T17:00:00Z") // 01:00 on the 19th in Beijing
	if got := when(r, now); got != "10 小时前" {
		t.Errorf("%q", got)
	}
	r.Date = str("2025-03-01T00:00:00Z")
	if got := when(r, now); got != "2025年3月1日" {
		t.Errorf("%q", got)
	}
}

// TestScreenshots renders the panel with live results into shots/, light
// and dark, when DEEPSEEK_API_KEY is set.
func TestScreenshots(t *testing.T) {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" || testing.Short() {
		t.Skip("DEEPSEEK_API_KEY not set")
	}
	src := dsapi.New(key)
	macHints = true // the Mac's ⌘ in the hints
	defer func() { macHints = runtime.GOOS == "darwin" }()
	shots := func(name string, setup func(a *app, tt *ui.Tester)) {
		for _, dark := range []bool{false, true} {
			a := newApp(defaultConfig(), key, "keychain", func(string) source { return src })
			a.spawn = func(fn func()) { fn() }
			a.balBusy = true // no real balance in screenshots
			a.cfg.History = []string{"DeepSeek V4 site:deepseek.com", "Go 1.27 release notes", "杭州 天气 after:2026-10-01", "mygo egoist"}
			// The panel is as wide as the app opens it: the conversation
			// is open by default, so most shots are the wide ones.
			w := panelWidth
			if a.cfg.AI {
				w = expandedWidth
			}
			tt := ui.NewTester(a.view, w, panelHeight)
			tt.SetScale(2)
			tt.SetDark(dark)
			setup(a, tt)
			tt.Advance(5 * time.Second)
			tt.Frame()
			suffix := "light"
			if dark {
				suffix = "dark"
			}
			save(t, filepath.Join("shots", name+"-"+suffix+".png"), tt)
		}
	}
	var cachedRS []dsapi.Result
	var cachedAns string
	search := func(q string, ai bool) func(a *app, tt *ui.Tester) {
		return func(a *app, tt *ui.Tester) {
			if cachedRS != nil { // reuse: one paid search per screenshot pair
				a.query, a.searched, a.took = q, q, 620*time.Millisecond
				a.all, a.page1, a.results, a.expanded = cachedRS, cachedRS, cachedRS, true // no paid search of 50
				a.choose(0)
				a.listFocus.Focus()
				if ai {
					a.cfg.AI = true
					a.chatFor = a.searchKey()
					tr := &turn{sources: dsapi.TopForAnswer(cachedRS), answer: cachedAns}
					a.addTurn(tr)
					a.msgs = append(dsapi.FirstMessages(q, tr.sources, "", time.Now()), dsapi.Message{Role: "assistant", Content: cachedAns})
					a.focusChat = true
					tt.SetSize(expandedWidth, panelHeight)
				} else {
					a.cfg.AI = false
					tt.SetSize(panelWidth, panelHeight)
				}
				tt.Frame()
				return
			}
			tt.Type(q)
			tt.Key(0, ui.KeyEnter)
			if ai {
				tt.Key(0, ui.KeyTab) // the keyboard to the follow-up box
				tt.SetSize(expandedWidth, panelHeight)
			} else {
				a.cfg.AI = false
				tt.SetSize(panelWidth, panelHeight)
			}
			if a.searchErr != "" || (ai && a.turns[0].err != "") {
				t.Fatalf("search %q: %s", q, a.searchErr)
			}
			cachedRS = a.page1
			if ai {
				cachedAns = a.turns[0].answer
			}
			t.Logf("%q: %d results in %v", q, len(a.results), a.took)
		}
	}
	shots("1-home", func(a *app, tt *ui.Tester) {})
	shots("2-expanded-ai", search("DeepSeek 搜索 API 怎么收费", true))
	shots("3-collapsed", func(a *app, tt *ui.Tester) {
		search("DeepSeek 搜索 API 怎么收费", true)(a, tt)
		a.expand(false)
		tt.SetSize(panelWidth, panelHeight)
		a.choose(2)
		tt.Frame()
	})
	var followTurn *turn
	var followMsgs []dsapi.Message
	shots("10-follow-up-search", func(a *app, tt *ui.Tester) {
		search("DeepSeek 搜索 API 怎么收费", true)(a, tt)
		if followTurn == nil {
			// The real API: a follow-up far outside what a pricing search
			// returned, so the model has to search again. Whether it does is
			// its call, so try these in order and keep the first that searched.
			// Questions about other vendors' prices are not enough: the coverage
			// articles already answer those, so ask about something off-topic.
			a.chatInput.Focus() // typed here, not in the query box
			tt.Frame()
			if !tt.Focused("追问") {
				t.Fatalf("the follow-up box is not focused: %q", tt.Texts())
			}
			turns, msgs := len(a.turns), len(a.msgs)
			questions := []string{
				"换个话题：上海明天天气怎么样？",
				"DeepSeek V4 什么时候正式发布？",
			}
			var ft *turn
			for _, q := range questions {
				tt.Type(q)
				tt.Key(0, ui.KeyEnter)
				if len(a.turns) != turns+1 {
					t.Fatalf("the follow-up did not add a turn: turns %d", len(a.turns))
				}
				ft = a.turns[len(a.turns)-1]
				if ft.err == "" && len(ft.searches) > 0 && ft.answer != "" {
					break
				}
				t.Logf("no new search for %q (err %q), undoing the turn", q, ft.err)
				a.turns, a.msgs = a.turns[:turns], a.msgs[:msgs]
				ft = nil
			}
			if ft == nil {
				t.Fatal("no follow-up made the model search again")
			}
			for _, s := range ft.searches {
				t.Logf("又搜了：%s · %d 条", s.query, s.n)
			}
			followTurn, followMsgs = ft, a.msgs
		} else {
			ft := turn{question: followTurn.question, searches: followTurn.searches, sources: followTurn.sources, answer: followTurn.answer}
			a.addTurn(&ft)
			ft.set = len(a.turns) - 1
			a.msgs = followMsgs
			a.showSet(ft.set)
		}
		tt.Frame()
		tt.Advance(time.Second)
	})
	shots("4-reader", func(a *app, tt *ui.Tester) {
		search("DeepSeek 搜索 API 怎么收费", true)(a, tt)
		a.page = pageReader
		a.readerURL, a.readerTitle, a.readerBusy = "https://api-docs.deepseek.com/zh-cn/guides/search", "网页搜索与抓取 | DeepSeek API Docs", false
		if readerCache == "" {
			p, err := src.Fetch(context.Background(), a.readerURL)
			if err != nil {
				t.Fatal(err)
			}
			readerCache = unescapeMD(p.Content)
		}
		a.readerText = readerCache
		tt.Frame()
	})
	for i, name := range []string{"general", "key", "usage", "shortcuts", "about"} {
		if i == tabUsage {
			continue // 7-usage, with demo data
		}
		shots("5-settings-"+name, func(a *app, tt *ui.Tester) {
			a.dataDir = "/Users/you/Library/Application Support/DS Search"
			a.openTab(i)
			tt.Frame()
		})
	}
	shots("11-history-delete", func(a *app, tt *ui.Tester) {
		tt.Key(0, ui.KeyDown)
		tt.Key(0, ui.KeyDown)
		tt.Frame()
	})
	shots("11-history-clear", func(a *app, tt *ui.Tester) {
		a.confirmClear = true
		r, _ := tt.Find("删除 mygo egoist")
		tt.Move(r.X-200, r.Y+r.H/2) // hovered: its ×
		tt.Frame()
	})
	shots("12-split", func(a *app, tt *ui.Tester) {
		search("DeepSeek 搜索 API 怎么收费", true)(a, tt)
		tt.Frame()
		r, _ := tt.Find("分隔条")
		x, y := r.X+r.W/2, r.Y+r.H/2
		tt.Press(x, y)
		for i := 1; i <= 8; i++ {
			tt.Move(x-float32(i)*15, y)
		}
		tt.Frame() // still held: the divider lit
	})
	var all50 []dsapi.Result
	shots("6-long-list-loading", func(a *app, tt *ui.Tester) {
		search("DeepSeek 搜索 API 怎么收费", false)(a, tt)
		a.all, a.results, a.expanded, a.expanding = cachedRS, cachedRS, false, true // as the search of 50 is out
		a.choose(len(a.results) - 2)
		tt.Frame()
		a.list.ScrollIntoView(len(a.results))
		tt.Advance(time.Second)
	})
	shots("6-long-list-end", func(a *app, tt *ui.Tester) {
		search("DeepSeek 搜索 API 怎么收费", false)(a, tt)
		a.all, a.results, a.expanded = cachedRS, cachedRS, false
		if all50 == nil {
			start := time.Now()
			a.loadMore() // the one search of 50
			if a.expandErr != "" {
				t.Fatal(a.expandErr)
			}
			all50 = a.all
			t.Logf("search of 50: %d unique results in %v", len(a.all), time.Since(start))
		} else {
			a.all, a.results, a.expanded = all50, all50, true
		}
		a.choose(len(a.results) - 1)
		tt.Frame()
		a.list.ScrollIntoView(len(a.results))
		tt.Advance(time.Second)
	})
	shots("7-usage", func(a *app, tt *ui.Tester) {
		// Demo data: 30 days of made-up usage, and a made-up balance.
		l := billing.Open("")
		day := time.Date(2026, 9, 20, 10, 0, 0, 0, dsapi.Shanghai)
		now := day
		l.SetClock(func() time.Time { return now })
		for i := 0; i < 30; i++ {
			now = day.AddDate(0, 0, i)
			for j := 0; j < (i*7)%23+3; j++ {
				l.AddRequest(billing.Search)
			}
			for j := 0; j < (i*3)%5; j++ {
				l.AddRequest(billing.Fetch)
			}
			for j := 0; j < (i*5)%11; j++ {
				l.AddChat("deepseek-flash", billing.Usage{PromptCacheHit: 1900, PromptCacheMiss: 900, Completion: 380})
			}
		}
		a.ledger = l
		a.balance = &dsapi.AccountBalance{Available: true, Infos: []dsapi.Balance{{Currency: "CNY", Total: "98.30"}}}
		a.balAt = time.Now()
		a.openUsage()
		a.balBusy = false
		a.editBudget = "30"
		tt.Frame()
	})
	shots("9-cheatsheet", func(a *app, tt *ui.Tester) {
		search("DeepSeek 搜索 API 怎么收费", true)(a, tt)
		tt.Key(ui.Cmd, ui.KeySlash)
	})
	shots("8-low-balance", func(a *app, tt *ui.Tester) {
		search("DeepSeek 搜索 API 怎么收费", true)(a, tt)
		a.balance = &dsapi.AccountBalance{Available: true, Infos: []dsapi.Balance{{Currency: "CNY", Total: "3.20"}}}
		tt.Frame()
	})
}

var readerCache string

func save(t *testing.T, path string, tt *ui.Tester) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestReader(t *testing.T) {
	f := &fake{results: canned, page: dsapi.Page{Title: "Go 1.27", Content: "# Go 1.27\n\n正文 **加粗**。"}}
	a, _ := testApp(f)
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	tt.Type("go")
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeySpace)
	if a.page != pageReader || !tt.HasText("在浏览器打开") {
		t.Fatalf("page %v, texts %q", a.page, tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	if a.page != pageSearch {
		t.Errorf("Escape left the reader open")
	}
}

// many makes n results, with a duplicate URL every 7th.
func many(n int) []dsapi.Result {
	var rs []dsapi.Result
	for i := 0; i < n; i++ {
		u := fmt.Sprintf("https://example.com/%d", i)
		if i%7 == 6 {
			u = fmt.Sprintf("https://example.com/%d", i-1)
		}
		rs = append(rs, dsapi.Result{Title: fmt.Sprintf("结果 %d", i+1), URL: u, Snippet: "摘要", RerankScore: float64(i%4) / 2})
	}
	return rs
}

func TestInfiniteScroll(t *testing.T) {
	f := &fake{results: many(48), answer: []string{"总结 [1]"}}
	a, _ := testApp(f)
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	tt.Type("无限")
	tt.Key(0, ui.KeyEnter)
	if len(f.counts) != 1 || f.counts[0] != 10 || len(a.results) != 9 { // 10 asked, one duplicate among them
		t.Fatalf("counts %v results %d", f.counts, len(a.results))
	}
	if !tt.Focused("搜索结果") {
		t.Fatal("the results did not take the keyboard")
	}
	// ↓ down the rows: nearing the end, one search of 50, appended.
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Advance(time.Second)
	scroll, _ := a.list.Visible()
	chosen, first := a.chosen, a.results[0].URL
	if len(f.counts) != 1 {
		t.Fatalf("loaded early: %v (chosen %d)", f.counts, a.chosen)
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown) // the 8th of 9: within the last 2
	unique := 48 - 48/7
	if len(f.counts) != 2 || f.counts[1] != 50 || !a.expanded || len(a.results) != unique {
		t.Fatalf("counts %v expanded %v results %d", f.counts, a.expanded, len(a.results))
	}
	if a.chosen != chosen+2 || a.results[0].URL != first {
		t.Errorf("chosen %d (want %d), first %s", a.chosen, chosen+2, a.results[0].URL)
	}
	tt.Frame()
	if now, _ := a.list.Visible(); now < scroll {
		t.Errorf("appending scrolled back: first visible %d → %d", scroll, now)
	}
	seen := map[string]bool{}
	for _, r := range a.results {
		if seen[r.URL] {
			t.Errorf("duplicate %s", r.URL)
		}
		seen[r.URL] = true
	}
	// To the end: one request only, the end row, ↓ stays on the last.
	tt.Key(ui.Cmd, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Advance(time.Second)
	if a.chosen != len(a.results)-1 || len(f.counts) != 2 {
		t.Errorf("chosen %d of %d, counts %v", a.chosen, len(a.results), f.counts)
	}
	if !tt.HasText(fmt.Sprintf("已显示全部 %d 条", unique)) {
		t.Errorf("no end row: %q", tt.Texts())
	}
	tt.Key(ui.Cmd, ui.KeyUp)
	if a.chosen != 0 {
		t.Errorf("⌘↑: chosen %d", a.chosen)
	}
	// Expanded, the AI summary reads the first 10 only.
	a.expand(true)
	if len(a.turns) != 1 || len(a.turns[0].sources) != 9 || len(f.chats) != 1 {
		t.Errorf("summary read %d", len(a.turns[0].sources))
	}
	r := a.ledger.Report()
	if r.Today.Search != 2 || r.Today.Chat != 1 {
		t.Errorf("ledger %+v", r.Today)
	}
}

// Scrolling near the end, not only choosing, loads the rest, and keeps
// the scroll where it was.
func TestInfiniteScrollByScrolling(t *testing.T) {
	f := &fake{results: many(48)}
	a, _ := testApp(f)
	a.cfg.AI = false
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	tt.Type("滚动")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(f.counts) != 1 {
		t.Fatalf("loaded before scrolling: %v", f.counts)
	}
	tt.Scroll(panelWidth/2, panelHeight/2, 0, 2000)
	tt.Advance(time.Second)
	at := a.listScroll.Y
	tt.Frame()
	tt.Advance(time.Second)
	if len(f.counts) != 2 || !a.expanded {
		t.Fatalf("counts %v (scroll %v of %v)", f.counts, a.listScroll.Y, a.listScroll.MaxY)
	}
	if d := a.listScroll.Y - at; d < -1 || d > 1 {
		t.Errorf("appending moved the scroll %v → %v", at, a.listScroll.Y)
	}
	if a.chosen != 0 {
		t.Errorf("appending changed the choice: %d", a.chosen)
	}
}

func TestInfiniteScrollFewer(t *testing.T) {
	// A first search that isn't full is all there is: no more asked, the end row.
	f := &fake{results: many(6)}
	a, _ := testApp(f)
	a.cfg.AI = false
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	tt.Type("更少")
	tt.Key(0, ui.KeyEnter)
	tt.Key(ui.Cmd, ui.KeyDown)
	tt.Frame()
	if len(f.counts) != 1 || !tt.HasText("已显示全部 6 条") {
		t.Errorf("counts %v texts %q", f.counts, tt.Texts())
	}
}

func TestInfiniteScrollError(t *testing.T) {
	f := &fake{results: many(30), fail50: &dsapi.Error{Status: 402}}
	a, opened := testApp(f)
	a.cfg.AI = false
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	tt.Type("失败")
	tt.Key(0, ui.KeyEnter)
	tt.Key(ui.Cmd, ui.KeyDown)
	tt.Frame()
	if !strings.Contains(a.expandErr, "余额不足") || !tt.HasText("加载失败，↵ 重试") || len(a.results) != 9 {
		t.Fatalf("err %q results %d texts %q", a.expandErr, len(a.results), tt.Texts())
	}
	if a.ledger.Report().Today.Search != 1 {
		t.Errorf("a failed search was billed: %+v", a.ledger.Report().Today)
	}
	// ↓ reaches the retry row; ↵ retries, and now it works.
	tt.Key(0, ui.KeyDown)
	if a.chosen != len(a.results) {
		t.Fatalf("chosen %d, not the retry row %d", a.chosen, len(a.results))
	}
	f.fail50 = nil
	tt.Key(0, ui.KeyEnter)
	if len(*opened) != 0 || len(f.counts) != 3 || !a.expanded || len(a.results) != 30-30/7 {
		t.Errorf("retry: opened %v counts %v results %d", *opened, f.counts, len(a.results))
	}
}

func TestKeyboardKeepsChosenVisible(t *testing.T) {
	f := &fake{results: many(10), answer: []string{strings.Repeat("很长的总结。", 120)}}
	a, _ := testApp(f)
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	tt.Type("长")
	tt.Key(0, ui.KeyEnter)
	for i := 0; i < 9; i++ {
		tt.Key(0, ui.KeyDown)
		tt.Advance(time.Second)
		title := a.results[a.chosen].Title
		r, ok := tt.Find(title)
		if !ok || r.Y < 60 || r.Y+r.H > panelHeight-30 {
			t.Fatalf("row %d (%s) not in view: %+v %v", a.chosen, title, r, ok)
		}
	}
}

func TestWarning(t *testing.T) {
	f := &fake{results: canned, balance: "3.20"}
	a, _ := testApp(f)
	a.refreshBalance(true)
	if w := a.warning(); !strings.Contains(w, "¥3.20") {
		t.Errorf("warning %q", w)
	}
	if !strings.Contains(a.trayLine(), "余额 ¥3.20") {
		t.Errorf("tray %q", a.trayLine())
	}
	f.balance = "50"
	a.refreshBalance(true)
	a.cfg.MonthlyBudget = 0.001
	a.bill("search")
	if w := a.warning(); !strings.Contains(w, "预算") {
		t.Errorf("budget warning %q", w)
	}
	for in, want := range map[float64]string{0: "¥0", 0.0046: "¥0.0046", 0.125: "¥0.12", 12.5: "¥12.50", 250: "¥250.0"} {
		if got := yuan(in); got != want {
			t.Errorf("yuan(%v) = %q, want %q", in, got, want)
		}
	}
}

// 0.1.0 could not be quit: the panel refused to close, which cancels
// App.Quit. Closing hides it only while the app isn't quitting.
func TestQuit(t *testing.T) {
	if !hideOnClose(false) || hideOnClose(true) {
		t.Error("the panel must hide on close, but close while quitting")
	}
	for _, p := range []page{pageSearch, pageSettings, pageReader} {
		f := &fake{results: canned}
		a, _ := testApp(f)
		quits := 0
		a.quit = func() { quits++ }
		a.page = p
		tt := ui.NewTester(a.view, panelWidth, panelHeight)
		tt.Key(ui.Cmd, ui.KeyQ)
		if quits != 1 {
			t.Errorf("page %d: ⌘Q quit %d times", p, quits)
		}
	}
	// With a query typed in the field, too.
	a, _ := testApp(&fake{results: canned})
	quits := 0
	a.quit = func() { quits++ }
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	tt.Type("abc")
	tt.Key(ui.Cmd, ui.KeyQ)
	a.openTab(tabAbout)
	tt.Frame()
	if err := tt.Click("退出 DS 搜索  " + cmdKey() + "Q"); err != nil {
		t.Fatal(err)
	}
	if quits != 2 {
		t.Errorf("⌘Q in the field and the settings' 退出: %d quits", quits)
	}
}

// Keys reach the app with the search field focused, as on the Mac.
func TestKeyRouting(t *testing.T) {
	f := &fake{results: many(48), answer: []string{"总结"}}
	a, opened := testApp(f)
	var copied []string
	a.copy = func(s string) { copied = append(copied, s) }
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	tt.Type("键盘")
	// In the field, ← → move the caret, as anywhere.
	tt.Key(0, ui.KeyLeft)
	tt.Type("X")
	if a.query != "键X盘" {
		t.Errorf("← in the field: %q", a.query)
	}
	tt.Key(0, ui.KeyRight)
	tt.Key(0, ui.KeyBackspace)
	tt.Key(0, ui.KeyBackspace)
	tt.Type("盘")
	tt.Key(0, ui.KeyEnter)
	if a.searched != "键盘" || !tt.Focused("搜索结果") {
		t.Fatalf("searched %q; the results have the keyboard: %v", a.searched, tt.Focused("搜索结果"))
	}
	// ↑ above the first goes up to the field, ↓ back down.
	tt.Key(0, ui.KeyUp)
	if !tt.Focused("搜索") {
		t.Error("↑ on the first row did not go to the field")
	}
	tt.Key(0, ui.KeyDown)
	if !tt.Focused("搜索结果") || a.chosen != 0 {
		t.Errorf("↓ in the field: focused %v chosen %d", tt.Focused("搜索结果"), a.chosen)
	}
	// ← → on the results do nothing now: no pages.
	tt.Key(0, ui.KeyRight)
	tt.Key(0, ui.KeyLeft)
	if a.chosen != 0 || a.page != pageSearch || len(f.counts) != 1 {
		t.Errorf("←→: chosen %d page %d counts %v", a.chosen, a.page, f.counts)
	}
	// ⌘C copies the chosen link.
	tt.Key(0, ui.KeyDown)
	tt.Key(ui.Cmd, ui.KeyC)
	if len(copied) != 1 || copied[0] != a.results[1].URL || !tt.HasText("已复制链接") {
		t.Errorf("⌘C copied %v", copied)
	}
	// ↵ opens it; ⌘3 the third.
	tt.Key(0, ui.KeyEnter)
	tt.Key(ui.Cmd, ui.Key3)
	if len(*opened) != 2 || (*opened)[0] != a.results[1].URL || (*opened)[1] != a.results[2].URL {
		t.Errorf("opened %v", *opened)
	}
	// Space previews, as Quick Look; it types nothing.
	tt.TypeKey(0, ui.KeySpace, " ")
	if a.page != pageReader || a.query != "键盘" {
		t.Errorf("space on the results: page %d query %q", a.page, a.query)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.page != pageSearch || !tt.Focused("搜索结果") {
		t.Errorf("esc in the preview: page %d, results focused %v", a.page, tt.Focused("搜索结果"))
	}
	// Typing on the results goes on in the field; ⌫ too.
	tt.TypeKey(0, ui.Key2, "2")
	if a.query != "键盘2" || !tt.Focused("搜索") {
		t.Errorf("typing: %q, field focused %v", a.query, tt.Focused("搜索"))
	}
	tt.Type("x")
	tt.Type("y")
	if a.query != "键盘2xy" {
		t.Errorf("typing on: %q", a.query)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyBackspace)
	if a.query != "键盘2x" || !tt.Focused("搜索") {
		t.Errorf("⌫ on the results: %q", a.query)
	}
	// An input method composing on the results: the commit goes to the query.
	tt.Key(0, ui.KeyDown)
	tt.Compose("ni", 2)
	if a.composing != "ni" || !tt.HasText("ni") {
		t.Errorf("composing %q", a.composing)
	}
	tt.Type("你")
	if a.query != "键盘2x你" || a.composing != "" || !tt.Focused("搜索") {
		t.Errorf("commit: %q composing %q", a.query, a.composing)
	}
	// ⌘L: the field, the query all selected.
	tt.Key(0, ui.KeyEnter)
	tt.Key(ui.Cmd, ui.KeyL)
	tt.Type("新")
	if a.query != "新" {
		t.Errorf("⌘L then typing: %q", a.query)
	}
	// Tab from the field opens the conversation; ⇧⇥ closes it, esc hides.
	tt.Key(0, ui.KeyTab)
	if !a.cfg.AI || !tt.Focused("追问") {
		t.Errorf("Tab: AI %v", a.cfg.AI)
	}
	tt.Key(ui.Shift, ui.KeyTab)
	if a.cfg.AI {
		t.Error("⇧⇥ did not collapse the conversation")
	}
	tt.Key(ui.Cmd, ui.KeySlash)
	if !a.cheat || !tt.HasText("键盘快捷键") {
		t.Fatalf("⌘/: %q", tt.Texts())
	}
	hidden := 0
	a.hide = func() { hidden++ }
	tt.Key(0, ui.KeyEscape)
	if a.cheat || hidden != 0 {
		t.Errorf("Escape: cheat %v hidden %d", a.cheat, hidden)
	}
	tt.Key(ui.Cmd, ui.KeyU)
	tt.Key(0, ui.KeyEscape)
	tt.Key(ui.Cmd, ui.KeyComma)
	tt.Key(0, ui.KeyEscape)
	if a.page != pageSearch || hidden != 0 {
		t.Errorf("Escape in usage/settings: page %d hidden %d", a.page, hidden)
	}
	tt.Key(0, ui.KeyEscape)
	if hidden != 1 {
		t.Errorf("Escape on the results hid %d times", hidden)
	}
}

func TestReaderKeys(t *testing.T) {
	long := "# 标题\n\n" + strings.Repeat("一段很长的正文，用来测试键盘滚动。\n\n", 200)
	f := &fake{results: canned, page: dsapi.Page{Title: "长文", Content: long}}
	a, opened := testApp(f)
	a.cfg.AI = false
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	tt.Type("x")
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeySpace)
	tt.Advance(time.Second)
	y := func() float32 { return a.readerScroll.Y }
	tt.Key(0, ui.KeyDown)
	tt.Advance(time.Second)
	if y() <= 0 {
		t.Fatalf("↓ did not scroll: %v (max %v)", y(), a.readerScroll.MaxY)
	}
	before := y()
	tt.Key(0, ui.KeySpace)
	tt.Advance(time.Second)
	if y()-before < 300 {
		t.Errorf("space scrolled %v", y()-before)
	}
	tt.Key(ui.Shift, ui.KeySpace)
	tt.Advance(time.Second)
	if d := y() - before; d > 1 || d < -1 {
		t.Errorf("⇧space: %v, want %v", y(), before)
	}
	tt.Key(ui.Cmd, ui.KeyDown)
	tt.Advance(time.Second)
	if a.readerScroll.MaxY-y() > 1 {
		t.Errorf("⌘↓: %v of %v", y(), a.readerScroll.MaxY)
	}
	tt.Key(0, ui.KeyEnter)
	if len(*opened) != 1 || (*opened)[0] != canned[0].URL {
		t.Errorf("↵ opened %v", *opened)
	}
	tt.Key(0, ui.KeyLeft)
	if a.page != pageSearch {
		t.Errorf("← did not go back")
	}
}

func TestSettingsKeyboard(t *testing.T) {
	a, _ := testApp(&fake{results: canned})
	a.openSettings()
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	var seen []string
	for i := 0; i < 10; i++ {
		tt.Key(0, ui.KeyTab)
		for _, name := range tabNames {
			if tt.Focused(name) && !slices.Contains(seen, name) {
				seen = append(seen, name)
			}
		}
	}
	for _, want := range tabNames {
		if !slices.Contains(seen, want) {
			t.Errorf("Tab never reached %q (reached %v)", want, seen)
		}
	}
	// Space presses the focused tab.
	for i := 0; i < 12 && !tt.Focused("用量与费用"); i++ {
		tt.Key(0, ui.KeyTab)
	}
	tt.Key(0, ui.KeySpace)
	if a.page != pageSettings || a.tab != tabUsage {
		t.Errorf("space on 用量与费用: page %d tab %d", a.page, a.tab)
	}
	tt.Key(ui.Cmd, ui.Key5)
	if a.tab != tabAbout || !tt.HasText("数据保存在哪") {
		t.Errorf("⌘5: tab %d", a.tab)
	}
	tt.Key(ui.Cmd, ui.Key4)
	if a.tab != tabKeys {
		t.Errorf("⌘4: tab %d", a.tab)
	}
	tt.Key(0, ui.KeyEscape)
	if a.page != pageSearch {
		t.Errorf("esc: page %d", a.page)
	}
	tt.Key(ui.Cmd, ui.KeyU)
	if a.page != pageSettings || a.tab != tabUsage || !tt.HasText("账户余额") {
		t.Errorf("⌘U: page %d tab %d", a.page, a.tab)
	}
}
