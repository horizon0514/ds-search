package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"ds-search/billing"
	"ds-search/dsapi"

	"github.com/egoist/mygo/ui/markdown"
)

const (
	maxSearchesPerTurn = 2 // web_search calls the model may make in a turn
	maxCallsPerTurn    = 4 // chat completions in a turn, tool rounds included
	toolResults        = 8 // results a web_search gives, by default
)

// turn is one question and its answer in the AI conversation. The first
// turn summarizes the search, with no question.
type turn struct {
	question string
	searches []*toolSearch
	sources  []dsapi.Result // what this turn's [n] refer to
	set      int            // the source list these are of: 0 the search's, or this turn's index when it searched
	answer   string
	busy     bool
	err      string
	md       markdown.Options
}

// toolSearch is a web_search the model made.
type toolSearch struct {
	query string
	n     int
	busy  bool
	err   string
}

// chatBackend is what a turn needs, captured for its goroutine.
type chatBackend struct {
	src  source
	ctx  context.Context
	seq  int
	post func(func())
}

// resetChat ends the conversation: its stream and searches stop.
func (a *app) resetChat() {
	if a.chatCancel != nil {
		a.chatCancel()
		a.chatCancel = nil
	}
	a.chatSeq++
	a.turns, a.msgs, a.chatFor = nil, nil, ""
	a.showSet(0)
}

// startChat starts a conversation about the search with its summary: the
// first answer, over its 10 best results.
func (a *app) startChat() {
	a.resetChat()
	a.chatFor = a.searchKey()
	if len(a.page1) == 0 || a.src == nil {
		return
	}
	rs := dsapi.TopForAnswer(a.page1)
	t := &turn{sources: rs}
	a.addTurn(t)
	a.runTurn(t, dsapi.FirstMessages(a.searched, rs, "", time.Now()), false)
}

// newChat (⌘N) clears the conversation, keeping the search: the next
// question starts one over its results.
func (a *app) newChat() {
	a.resetChat()
	a.chatFor = a.searchKey()
	a.draft = ""
	a.focusChat = true
}

// searchKey names the search a conversation is about.
func (a *app) searchKey() string { return fmt.Sprint(a.seq, "\x00", a.searched) }

func (a *app) addTurn(t *turn) {
	t.md.OnLink = func(dest string) { a.cite(t, dest) }
	a.turns = append(a.turns, t)
}

// busy tells whether a turn is being answered.
func (a *app) chatBusy() bool {
	return len(a.turns) > 0 && a.turns[len(a.turns)-1].busy
}

// ask asks a follow-up question; the model may search the web for it.
func (a *app) ask(q string) bool {
	q = strings.TrimSpace(q)
	if q == "" || a.chatBusy() || a.src == nil {
		return false
	}
	t := &turn{question: q}
	msgs := append([]dsapi.Message(nil), a.msgs...)
	if len(msgs) == 0 { // a new conversation: the search's results come with the question
		rs := dsapi.TopForAnswer(a.page1)
		t.sources = rs
		msgs = dsapi.FirstMessages(a.searched, rs, q, time.Now())
	} else {
		prev := a.turns[len(a.turns)-1]
		t.sources, t.set = prev.sources, prev.set
		msgs = append(msgs, dsapi.Message{Role: "user", Content: q})
	}
	a.chatFor = a.searchKey()
	a.addTurn(t)
	a.lastQuestion = q
	a.runTurn(t, msgs, true)
	return true
}

// runTurn answers t: chat completions in a loop, running the web_search
// calls the model makes, at most maxSearchesPerTurn, until it answers.
func (a *app) runTurn(t *turn, msgs []dsapi.Message, tools bool) {
	if a.chatCancel != nil {
		a.chatCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.chatCancel = cancel
	t.busy = true
	b := chatBackend{src: a.src, ctx: ctx, seq: a.chatSeq, post: a.post}
	idx := len(a.turns) - 1
	live := func() bool { return b.seq == a.chatSeq && ctx.Err() == nil } // on the main thread
	a.spawn(func() {
		searches := 0
		var sources []dsapi.Result // this turn's own, once it searched
		var err error
		for call := 0; call < maxCallsPerTurn; call++ {
			var offer []dsapi.Tool
			if tools && searches < maxSearchesPerTurn && call < maxCallsPerTurn-1 {
				offer = []dsapi.Tool{dsapi.SearchTool}
			}
			b.post(func() {
				if live() {
					t.answer = ""
				}
			})
			var r dsapi.Reply
			r, err = b.src.Chat(ctx, msgs, offer, func(s string) {
				b.post(func() {
					if live() {
						t.answer += s
					}
				})
			})
			if r.Usage != nil {
				u := *r.Usage
				b.post(func() { a.billChat(b.src.ModelName(), u) }) // billed, even when cut short
			}
			if err != nil {
				break
			}
			if len(r.ToolCalls) == 0 {
				msgs = append(msgs, dsapi.Message{Role: "assistant", Content: r.Content})
				break
			}
			msgs = append(msgs, dsapi.Message{Role: "assistant", Content: r.Content, ToolCalls: r.ToolCalls})
			for _, tc := range r.ToolCalls {
				msgs = append(msgs, dsapi.Message{Role: "tool", ToolCallID: tc.ID, Content: a.runTool(b, t, idx, tc, &searches, &sources)})
				if ctx.Err() != nil {
					break
				}
			}
			if ctx.Err() != nil {
				err = ctx.Err()
				break
			}
		}
		b.post(func() {
			if b.seq != a.chatSeq {
				return
			}
			t.busy = false
			if ctx.Err() != nil {
				return
			}
			a.chatCancel = nil
			if err != nil {
				t.err = errText(err)
				return // the conversation goes on without this turn
			}
			a.msgs = msgs
		})
	})
}

// runTool runs one call of the model's: web_search, numbered after what
// this turn found before.
func (a *app) runTool(b chatBackend, t *turn, idx int, tc dsapi.ToolCall, searches *int, sources *[]dsapi.Result) string {
	if tc.Function.Name != dsapi.SearchTool.Function.Name {
		return "没有这个工具：" + tc.Function.Name
	}
	if *searches >= maxSearchesPerTurn {
		return fmt.Sprintf("本轮已搜索 %d 次，达到上限。请用已有结果回答。", maxSearchesPerTurn)
	}
	var args struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil || strings.TrimSpace(args.Query) == "" {
		return "参数无效：需要 {\"query\": \"搜索词\"}"
	}
	*searches++
	q := strings.TrimSpace(args.Query)
	if utf8.RuneCountInString(q) > 128 {
		q = string([]rune(q)[:128])
	}
	n := args.Count
	if n <= 0 {
		n = toolResults
	}
	n = min(n, 10)
	s := &toolSearch{query: q, busy: true}
	b.post(func() {
		if b.seq == a.chatSeq {
			t.searches = append(t.searches, s)
		}
	})
	rs, err := b.src.Search(b.ctx, q, n)
	if err != nil {
		b.post(func() { s.busy, s.err = false, errText(err) })
		return "搜索失败：" + errText(err)
	}
	first := len(*sources) + 1
	fresh := dedupe(nil, rs)
	if len(*sources) > 0 {
		fresh = dedupe(append([]dsapi.Result(nil), *sources...), rs)[len(*sources):]
	}
	*sources = append(*sources, fresh...)
	own := append([]dsapi.Result(nil), *sources...)
	b.post(func() {
		s.busy, s.n = false, len(fresh)
		if len(rs) > 0 {
			a.bill(billing.Search)
		}
		if b.seq != a.chatSeq {
			return
		}
		t.sources, t.set = own, idx
		a.showSet(idx) // the left list shows what this turn found
	})
	if len(fresh) == 0 {
		return "没有找到新的结果。"
	}
	return fmt.Sprintf("“%s”的搜索结果（编号 [%d]–[%d]）：\n\n%s", q, first, first+len(fresh)-1, dsapi.Sources(fresh, first))
}

// sets are the source lists the left pane switches between: the
// search's, and those of each turn that searched.
func (a *app) sets() []int {
	out := []int{0}
	for i, t := range a.turns {
		if i > 0 && t.set == i {
			out = append(out, i)
		}
	}
	return out
}

func (a *app) setLabel(s int) string {
	if s == 0 || s >= len(a.turns) {
		return "原始搜索"
	}
	q := ""
	if ss := a.turns[s].searches; len(ss) > 0 {
		q = ss[0].query
	}
	return fmt.Sprintf("第%d轮：%s", s+1, trim(q, 12))
}

// showSet shows source list s on the left.
func (a *app) showSet(s int) {
	if s < 0 || (s > 0 && (s >= len(a.turns) || a.turns[s].set != s)) {
		return
	}
	same := s == a.srcSet
	a.srcSet = s
	if s == 0 {
		a.results = a.all
	} else {
		a.results = a.turns[s].sources
	}
	if same { // grown, as by a second search: the choice stays
		a.chosen = min(a.chosen, len(a.results)-1)
		return
	}
	a.chosen = -1
	if len(a.results) > 0 {
		a.chosen = 0
		a.list.ScrollIntoView(0)
	}
}

// switchSet (⌥← ⌥→) shows the previous or next source list.
func (a *app) switchSet(d int) {
	ss := a.sets()
	for i, s := range ss {
		if s == a.srcSet {
			if j := i + d; j >= 0 && j < len(ss) {
				a.showSet(ss[j])
			}
			return
		}
	}
}

// cite selects source [n] of turn t on the left; again, it opens it.
func (a *app) cite(t *turn, dest string) {
	n := 0
	if _, err := fmt.Sscanf(dest, "cite:%d", &n); err != nil {
		a.openURL(dest)
		return
	}
	if n < 1 || n > len(t.sources) {
		return
	}
	u := t.sources[n-1].URL
	a.showSet(t.set)
	for i, r := range a.results {
		if r.URL == u {
			if a.chosen == i && a.cited == u {
				a.openURL(u)
				return
			}
			a.choose(i)
			a.cited = u
			return
		}
	}
	a.openURL(u) // not in the list: it went past
}

// citeNth (⌘1–9 in the conversation) cites [n] of the last answer.
func (a *app) citeNth(n int) {
	for i := len(a.turns) - 1; i >= 0; i-- {
		if t := a.turns[i]; t.answer != "" || len(t.sources) > 0 {
			a.cite(t, fmt.Sprintf("cite:%d", n))
			return
		}
	}
}

// expand opens the conversation on the right, starting it when the
// search has none yet.
func (a *app) expand(on bool) {
	if a.cfg.AI != on {
		a.cfg.AI = on
		a.changed(a.cfg)
		a.resize(on)
	}
	if on && a.chatFor != a.searchKey() && len(a.page1) > 0 && !a.searching {
		a.startChat()
	}
	if on {
		a.focusChat = true
	} else if len(a.results) > 0 && !a.showingHistory() {
		a.focusList = true
	} else {
		a.input.Focus()
	}
}

func trim(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
