package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ds-search/dsapi"

	"github.com/egoist/mygo/ui"
)

func call(id, query string) dsapi.ToolCall {
	var tc dsapi.ToolCall
	tc.ID, tc.Type = id, "function"
	tc.Function.Name = "web_search"
	tc.Function.Arguments = fmt.Sprintf(`{"query":%q}`, query)
	return tc
}

func results(prefix string, n int) []dsapi.Result {
	var rs []dsapi.Result
	for i := 0; i < n; i++ {
		rs = append(rs, dsapi.Result{Title: fmt.Sprintf("%s %d", prefix, i+1), URL: fmt.Sprintf("https://%s.example/%d", prefix, i+1), Snippet: "摘要"})
	}
	return rs
}

// expanded makes an app searched for q, with the conversation open and
// its summary done.
func expanded(t *testing.T, f *fake) (*app, *ui.Tester, *[]string) {
	t.Helper()
	a, opened := testApp(f)
	tt := ui.NewTester(a.view, expandedWidth, panelHeight)
	tt.Type("原始")
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyTab)
	if len(a.turns) != 1 || a.turns[0].busy || !tt.Focused("追问") {
		t.Fatalf("no summary: turns %d focused %v", len(a.turns), tt.Focused("追问"))
	}
	return a, tt, opened
}

func TestFollowUpWithoutSearch(t *testing.T) {
	f := &fake{results: results("orig", 10), answer: []string{"答案 [2]"}}
	a, tt, _ := expanded(t, f)
	tt.Type("再说说")
	tt.Key(0, ui.KeyEnter)
	if len(a.turns) != 2 || a.draft != "" {
		t.Fatalf("turns %d draft %q", len(a.turns), a.draft)
	}
	ft := a.turns[1]
	if ft.answer != "答案 [2]" || len(ft.searches) != 0 || ft.set != 0 || len(ft.sources) != 10 || ft.sources[1].URL != a.turns[0].sources[1].URL {
		t.Errorf("turn %+v", ft)
	}
	if fmt.Sprint(f.offered) != "[0 1]" { // the summary has no tool; follow-ups do
		t.Errorf("offered %v", f.offered)
	}
	// The follow-up carries the conversation: system, results, summary, question.
	last := f.chats[1]
	if len(last) != 4 || last[2].Role != "assistant" || last[3].Content != "再说说" {
		t.Errorf("messages %+v", last)
	}
	if len(a.msgs) != 5 || len(a.sets()) != 1 || tt.HasText("原始搜索") {
		t.Errorf("msgs %d sets %v", len(a.msgs), a.sets())
	}
}

func TestFollowUpSearchesAndNumbers(t *testing.T) {
	f := &fake{results: results("orig", 10), byQuery: map[string][]dsapi.Result{
		"甲": results("a", 3),
		"乙": append(results("a", 1), results("b", 2)...), // one already found
	}}
	a, tt, opened := expanded(t, f)
	f.replies = []dsapi.Reply{
		{ToolCalls: []dsapi.ToolCall{call("c1", "甲")}},
		{Content: "我再查一下", ToolCalls: []dsapi.ToolCall{call("c2", "乙")}},
		{Content: "甲说 [1]，乙说 [5]。"},
	}
	tt.Type("新问题")
	tt.Key(0, ui.KeyEnter)
	ft := a.turns[1]
	if len(ft.searches) != 2 || ft.searches[0].query != "甲" || ft.searches[1].n != 2 || ft.err != "" {
		t.Fatalf("searches %+v err %q", ft.searches, ft.err)
	}
	// The turn's sources: 甲's [1]–[3], then 乙's new [4]–[5].
	if len(ft.sources) != 5 || ft.sources[0].URL != "https://a.example/1" || ft.sources[4].URL != "https://b.example/2" {
		t.Errorf("sources %v", ft.sources)
	}
	tool2 := f.chats[3][len(f.chats[3])-1]
	if tool2.Role != "tool" || tool2.ToolCallID != "c2" || !strings.Contains(tool2.Content, "[4]") || strings.Contains(tool2.Content, "[1]") {
		t.Errorf("second tool message %q", tool2.Content)
	}
	if ft.answer != "甲说 [1]，乙说 [5]。" { // what came before the tool call is not the answer
		t.Errorf("answer %q", ft.answer)
	}
	if fmt.Sprint(f.offered) != "[0 1 1 0]" { // two searches: the third call may not search
		t.Errorf("offered %v", f.offered)
	}
	// The left list shows the turn's sources, with chips to switch.
	if a.srcSet != 1 || len(a.results) != 5 || !tt.HasText("原始搜索") || !tt.HasText("第2轮：甲") || !tt.HasText("又搜了：乙 · 2 条") {
		t.Errorf("set %d results %d texts %q", a.srcSet, len(a.results), tt.Texts())
	}
	// [5] selects b/2 on the left; again, it opens it.
	a.cite(ft, "cite:5")
	if a.results[a.chosen].URL != "https://b.example/2" || len(*opened) != 0 {
		t.Errorf("cite: chosen %d opened %v", a.chosen, *opened)
	}
	a.cite(ft, "cite:5")
	if len(*opened) != 1 || (*opened)[0] != "https://b.example/2" {
		t.Errorf("cite again opened %v", *opened)
	}
	// ⌥← ⌥→ on the results switch lists; the summary's [1] goes back to the search's.
	tt.Key(0, ui.KeyTab)
	tt.Key(ui.Alt, ui.KeyLeft)
	if a.srcSet != 0 || len(a.results) != 10 {
		t.Errorf("⌥←: set %d", a.srcSet)
	}
	tt.Key(ui.Alt, ui.KeyRight)
	if a.srcSet != 1 {
		t.Errorf("⌥→: set %d", a.srcSet)
	}
	a.cite(a.turns[0], "cite:3")
	if a.srcSet != 0 || a.chosen != 2 {
		t.Errorf("summary cite: set %d chosen %d", a.srcSet, a.chosen)
	}
	// Billing: every search (ours, the model's 2), and 1 summary + 3 calls of the turn.
	r := a.ledger.Report().Today
	if r.Search != len(f.queries) || len(f.queries) < 3 || r.Chat != 4 {
		t.Errorf("ledger %+v", r)
	}
	// A third question with no search reuses this turn's numbering.
	tt.Key(0, ui.KeyTab)
	tt.Type("还有呢")
	tt.Key(0, ui.KeyEnter)
	if t3 := a.turns[2]; t3.set != 1 || len(t3.sources) != 5 {
		t.Errorf("third turn set %d sources %d", t3.set, len(t3.sources))
	}
}

func TestToolCallCap(t *testing.T) {
	f := &fake{results: results("orig", 10), byQuery: map[string][]dsapi.Result{}}
	a, tt, _ := expanded(t, f)
	f.replies = []dsapi.Reply{
		{ToolCalls: []dsapi.ToolCall{call("1", "一"), call("2", "二")}},
		{ToolCalls: []dsapi.ToolCall{call("3", "三")}}, // past the cap: refused
		{ToolCalls: []dsapi.ToolCall{call("4", "四")}},
		{ToolCalls: []dsapi.ToolCall{call("5", "五")}},
		{Content: "never"},
	}
	before := len(f.queries)
	tt.Type("搜个没完")
	tt.Key(0, ui.KeyEnter)
	ft := a.turns[1]
	if n := len(f.queries) - before; n != maxSearchesPerTurn || len(ft.searches) != 2 {
		t.Errorf("searched %d times", n)
	}
	if calls := len(f.chats) - 1; calls != maxCallsPerTurn {
		t.Errorf("chat calls %d", calls)
	}
	refused := f.chats[3][len(f.chats[3])-1]
	if !strings.Contains(refused.Content, "上限") {
		t.Errorf("third search not refused: %q", refused.Content)
	}
	if ft.busy {
		t.Error("the turn never ended")
	}
}

func TestPaneKeys(t *testing.T) {
	f := &fake{results: results("orig", 10), answer: []string{"好"}}
	a, tt, _ := expanded(t, f)
	// Typing in the right pane is the follow-up, not the query.
	tt.Type("一")
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Type("二")
	if a.draft != "一\n二" || a.query != "原始" {
		t.Fatalf("draft %q query %q", a.draft, a.query)
	}
	tt.Key(0, ui.KeyEnter)
	if len(a.turns) != 2 || a.turns[1].question != "一\n二" || a.draft != "" {
		t.Fatalf("send: turns %d draft %q", len(a.turns), a.draft)
	}
	// ↑ in the empty box brings the question back.
	tt.Key(0, ui.KeyUp)
	if a.draft != "一\n二" {
		t.Errorf("↑ recalled %q", a.draft)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(0, ui.KeyBackspace)
	// The results: ↓ ↵ there; typing goes to the query.
	tt.Key(0, ui.KeyTab)
	tt.Key(0, ui.KeyDown)
	if a.chosen != 1 || !tt.Focused("搜索结果") {
		t.Errorf("results: chosen %d", a.chosen)
	}
	tt.TypeKey(0, ui.KeyX, "x")
	if a.query != "原始x" || a.draft != "" {
		t.Errorf("typing on the results: query %q draft %q", a.query, a.draft)
	}
	// ⌘N: a new conversation over the same results; the next question carries them.
	tt.Key(0, ui.KeyTab)
	tt.Key(ui.Cmd, ui.KeyN)
	if len(a.turns) != 0 || len(a.results) != 10 || len(a.msgs) != 0 || !tt.Focused("追问") {
		t.Fatalf("⌘N: turns %d results %d", len(a.turns), len(a.results))
	}
	tt.Type("新的")
	tt.Key(0, ui.KeyEnter)
	first := f.chats[len(f.chats)-1]
	if len(first) != 2 || !strings.Contains(first[1].Content, "orig 1") || !strings.Contains(first[1].Content, "新的") {
		t.Errorf("new conversation's first message %+v", first)
	}
	// Esc collapses; esc hides.
	hidden := 0
	a.hide = func() { hidden++ }
	tt.Key(0, ui.KeyEscape)
	if a.cfg.AI || hidden != 0 || !tt.Focused("搜索结果") {
		t.Errorf("esc: AI %v hidden %d", a.cfg.AI, hidden)
	}
	tt.Key(0, ui.KeyEscape)
	if hidden != 1 {
		t.Errorf("esc again hid %d", hidden)
	}
}

// A new search, or ⌘N, stops the stream that is out.
func TestChatCancel(t *testing.T) {
	for _, how := range []string{"⌘N", "search"} {
		f := &fake{results: results("orig", 10), answer: []string{"好"}}
		a, tt, _ := expanded(t, f)
		var mu sync.Mutex
		var wg sync.WaitGroup
		a.spawn = func(fn func()) { wg.Add(1); go func() { defer wg.Done(); fn() }() }
		a.post = func(fn func()) { mu.Lock(); defer mu.Unlock(); fn() }
		f.mu.Lock()
		f.block = make(chan struct{})
		f.mu.Unlock()
		mu.Lock()
		a.ask("慢问题")
		mu.Unlock()
		deadline := time.Now().Add(2 * time.Second)
		calls := func() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.offered) }
		for calls() < 3 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		mu.Lock()
		if how == "⌘N" {
			a.newChat()
		} else {
			f.mu.Lock()
			f.block = nil
			f.mu.Unlock()
			a.query = "另一个"
			a.search()
		}
		mu.Unlock()
		wg.Wait() // the cancelled call returned
		mu.Lock()
		if how == "⌘N" && len(a.turns) != 0 {
			t.Errorf("%s: turns %d", how, len(a.turns))
		}
		if how == "search" && (len(a.turns) != 1 || a.turns[0].question != "") {
			t.Errorf("%s: turns %d", how, len(a.turns))
		}
		mu.Unlock()
		_ = tt
	}
}
