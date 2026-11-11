package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"ds-search/billing"
	"ds-search/dsapi"
	"ds-search/websearch"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// The app is built on three roles, so the web search provider can differ
// from the model that reads and summarizes its results.
type searcher interface {
	Search(ctx context.Context, q string, n int) ([]dsapi.Result, error)
	Name() string
}

type reader interface {
	Fetch(ctx context.Context, url string) (dsapi.Page, error)
}

type llm interface {
	Chat(ctx context.Context, msgs []dsapi.Message, tools []dsapi.Tool, onDelta func(string)) (dsapi.Reply, error)
	Check(ctx context.Context) error
	GetBalance(ctx context.Context) (dsapi.AccountBalance, error)
	ModelName() string
}

// source is all three at once: the DeepSeek client, or a test fake.
type source interface {
	searcher
	reader
	llm
}

const (
	pageSize   = 10 // the first search asks for these, to show them fast
	maxResults = 50 // one more search asks for these, as the list nears its end
)

type page int

const (
	pageSearch page = iota
	pageReader
	pageSettings
)

// The tabs of the settings.
const (
	tabGeneral = iota
	tabKey
	tabUsage
	tabKeys
	tabAbout
)

var tabNames = []string{"通用", "API Key", "用量与费用", "快捷键", "关于"}

// app is the panel's state. It is read and changed on the main thread;
// work on goroutines (spawn) hands results back through post.
type app struct {
	cfg     config
	seek    searcher
	fetch   reader
	brain   llm
	newSrc  func(config, string, string) source
	keyFrom string // "keychain", "file", "env" or ""
	key     string // shown masked
	keyErr  string // the keychain could not be read
	tvKey   string // Tavily, shown masked
	tvFrom  string
	tvErr   string
	keys    keyStore
	dataDir string

	// Hooks from the shell (main.go), or from tests.
	post     func(func())     // run on the main thread and redraw
	spawn    func(func())     // run in the background
	open     func(url string) // open in the default browser
	hide     func()
	quit     func()
	changed  func(config) // the config changed: save it
	setShort func(string) error

	page   page
	query  string
	input  ui.Handle
	list   ui.ListState
	chosen int

	// The last search: its first 10 results, then up to 50 appended.
	searched   string
	all        []dsapi.Result
	page1      []dsapi.Result // the first 10, which the AI summary reads
	results    []dsapi.Result // what the list shows: all
	expanded   bool           // all is all there is: the search of 50 came back, or the first wasn't full
	expanding  bool
	expandErr  string
	listScroll ui.ScrollState
	listFocus  ui.Handle // the results, which take the keyboard after a search
	searching  bool
	searchErr  string
	took       time.Duration
	seq        int // the current search, to drop stale replies

	// ✨ AI 对话, on the right while the panel is expanded (cfg.AI)
	turns          []*turn
	msgs           []dsapi.Message // the conversation so far, as the model reads it
	chatSeq        int             // the current conversation, to drop stale replies
	chatCancel     context.CancelFunc
	chatFor        string // the search it is about (searchKey)
	draft          string
	chatInput      ui.Handle
	chatList       ui.ListState
	focusChat      bool
	chatFocused    bool
	srcSet         int    // the source list on the left: 0 the search's, or a turn's that searched
	cited          string // the source a citation selected: again, it opens
	lastQuestion   string
	resize         func(expanded bool) // the panel widens for the conversation
	setTranslucent func(bool)          // the panel's material shows, or not

	// Reader
	readerURL   string
	readerTitle string
	readerText  string
	readerBusy  bool
	readerErr   string
	readerMD    markdown.Options

	// Settings
	tab          int
	changingKey  bool // the key input shows over the saved key
	tvChanging   bool // the Tavily key input shows over the saved key
	confirmClear bool // 清空 the history: once more to confirm
	panesW       float32
	editKey      string
	tvEdit       string
	editShortcut string
	settingsMsg  string
	settingsErr  bool
	checking     bool

	// Billing
	ledger       *billing.Ledger
	balance      *dsapi.AccountBalance
	balErr       string
	balAt        time.Time
	balBusy      bool
	sinceBalance int    // billable calls since the balance was read
	onUsage      func() // the usage or the balance changed (the tray shows them)
	editLow      string
	editBudget   string
	usageMsg     string

	// Keyboard
	cheat        bool   // the cheatsheet (⌘/) shows
	focusQuery   bool   // ⌘L: select the query as the field builds
	caretToEnd   bool   // typing on the results: the caret at the query's end
	focusList    bool   // back on the results: the keyboard to them, as they build
	listFocused  bool   // the results list has the keyboard, as the frame found
	composing    string // what an input method composes on the results, on its way to the query
	readerScroll ui.ScrollState
	copy         func(string) // to the clipboard
	toast        string

	icons map[string]*ui.Bitmap // by icon URL; nil while loading or where none
	asked map[string]bool
}

func newApp(cfg config, key, from, tavilyKey string, newSrc func(config, string, string) source) *app {
	a := &app{cfg: cfg, newSrc: newSrc, keyFrom: from, key: key, tvKey: tavilyKey,
		icons: map[string]*ui.Bitmap{}, asked: map[string]bool{}, chosen: -1}
	a.keys = keyStore{secrets: mygoSecrets{}, env: os.Getenv}
	if key != "" {
		a.bind(newSrc(cfg, key, tavilyKey))
	} else {
		a.page, a.tab = pageSettings, tabKey
	}
	a.chatList.FollowEnd = true
	a.readerMD.OnLink = func(u string) { a.openURL(u) }
	a.post = func(fn func()) { fn() }
	a.spawn = func(fn func()) { go fn() }
	a.open = func(string) {}
	a.hide = func() {}
	a.quit = func() {}
	a.changed = func(config) {}
	a.setShort = func(string) error { return nil }
	a.onUsage = func() {}
	a.copy = func(string) {}
	a.resize = func(bool) {}
	a.setTranslucent = func(bool) {}
	a.ledger = billing.Open("")
	a.input.Focus() // waits for the field to be built
	return a
}

func (a *app) bind(src source) {
	if src == nil {
		a.seek, a.fetch, a.brain = nil, nil, nil
		return
	}
	a.seek, a.fetch, a.brain = src, src, src
}

func (a *app) rebuildSource() {
	if a.key == "" {
		a.bind(nil)
		return
	}
	a.bind(a.newSrc(a.cfg, a.key, a.tvKey))
}

func (a *app) configured() bool { return a.seek != nil && a.brain != nil }

// submit runs what Enter means: a new search, or opening the chosen row.
func (a *app) submit() {
	q := strings.TrimSpace(a.query)
	if q == "" {
		if a.chosen >= 0 && a.chosen < len(a.cfg.History) {
			a.query = a.cfg.History[a.chosen]
			a.search()
		}
		return
	}
	a.search()
}

func (a *app) search() {
	q := strings.TrimSpace(a.query)
	if q == "" || a.seek == nil {
		return
	}
	a.seq++
	seq := a.seq
	a.searched, a.searching, a.searchErr, a.toast = q, true, "", ""
	a.resetChat()
	a.expanding, a.expandErr = false, ""
	if !a.cfg.NoHistory {
		a.cfg.History = remember(a.cfg.History, q)
	}
	a.changed(a.cfg)
	search := a.seek
	provider := search.Name()
	start := time.Now()
	a.spawn(func() {
		rs, err := search.Search(context.Background(), q, pageSize)
		took := time.Since(start)
		a.post(func() {
			if seq != a.seq {
				return
			}
			before := a.wide()
			a.searching, a.took = false, took
			if err != nil {
				a.searchErr = errText(err)
				a.relayout(before)
				return
			}
			if len(rs) > 0 {
				a.billSearchAs(provider)
			}
			a.all = dedupe(nil, rs)
			a.page1, a.results = a.all, a.all
			a.expanded = len(rs) < pageSize
			a.chosen = -1
			a.listScroll.Y = 0
			a.relayout(before)
			if len(a.results) > 0 {
				a.choose(0)
				if a.cfg.AI && !a.cfg.Sources {
					a.focusChat = true // the answer: it is what the panel shows
				} else {
					a.listFocus.Focus() // ↑↓ ↵ 空格 on the results; typing goes back to the query
				}
			}
			if a.cfg.AI && len(rs) > 0 {
				a.startChat()
			}
		})
	})
}

// dedupe appends to have the results of rs whose URL it hasn't.
func dedupe(have, rs []dsapi.Result) []dsapi.Result {
	seen := map[string]bool{}
	for _, r := range have {
		seen[r.URL] = true
	}
	for _, r := range rs {
		if !seen[r.URL] {
			seen[r.URL] = true
			have = append(have, r)
		}
	}
	return have
}

// moreLeft tells whether one search of 50 may still add results.
func (a *app) moreLeft() bool {
	return a.srcSet == 0 && !a.expanded && !a.expanding && a.expandErr == "" && len(a.all) > 0
}

// loadMore appends the results of one search of 50 for the same query,
// those it hasn't yet, below the rows shown: the scroll and the choice
// stay. The API has no offset: 50 are all there are.
func (a *app) loadMore() {
	if a.expanded || a.expanding || len(a.all) == 0 || a.searching {
		return
	}
	a.expanding, a.expandErr = true, ""
	q, search, seq := a.searched, a.seek, a.seq
	provider := search.Name()
	a.spawn(func() {
		rs, err := search.Search(context.Background(), q, maxResults)
		a.post(func() {
			if seq != a.seq {
				return
			}
			a.expanding = false
			if err != nil {
				a.expandErr = errText(err)
				return
			}
			if len(rs) > 0 {
				a.billSearchAs(provider)
			}
			a.all = dedupe(a.all, rs)
			if a.srcSet == 0 {
				a.results = a.all
			}
			a.expanded = true
		})
	})
}

// endRow tells whether the list ends with a row of its state: loading,
// all shown, or a failure to retry with ↵.
func (a *app) endRow() bool {
	return a.srcSet == 0 && len(a.results) > 0 && (a.expanding || a.expandErr != "" || a.expanded)
}

// choose chooses row i, the failure's row too, to retry it with ↵.
// Nearing the end loads more.
func (a *app) choose(i int) {
	n := a.rows()
	if a.expandErr != "" && a.srcSet == 0 && !a.showingHistory() {
		n++ // the retry row
	}
	if n == 0 {
		a.chosen = -1
		return
	}
	a.chosen = max(0, min(i, n-1))
	if !a.showingHistory() {
		a.list.ScrollIntoView(a.chosen)
		if a.chosen >= len(a.results)-2 && a.moreLeft() {
			a.loadMore()
		}
	}
}

// rows is how many rows the list shows: results, or the history.
func (a *app) rows() int {
	if a.showingHistory() {
		return min(len(a.cfg.History), 8)
	}
	return len(a.results)
}

func (a *app) showingHistory() bool {
	return strings.TrimSpace(a.query) == "" && !a.searching
}

// read opens the chosen result in the reader.
func (a *app) read() {
	if a.chosen < 0 || a.chosen >= len(a.results) || a.showingHistory() {
		return
	}
	r := a.results[a.chosen]
	a.page = pageReader
	if a.readerURL == r.URL && (a.readerText != "" || a.readerBusy) {
		return
	}
	a.readerURL, a.readerTitle, a.readerText, a.readerErr, a.readerBusy = r.URL, r.Title, "", "", true
	a.readerScroll.X, a.readerScroll.Y = 0, 0
	fetch, u := a.fetch, r.URL
	a.spawn(func() {
		p, err := fetch.Fetch(context.Background(), u)
		a.post(func() {
			if a.readerURL != u {
				return
			}
			a.readerBusy = false
			if err != nil {
				a.readerErr = errText(err)
				return
			}
			if p.Content != "" {
				a.bill(billing.Fetch)
			}
			if p.Title != "" {
				a.readerTitle = p.Title
			}
			a.readerText = unescapeMD(p.Content)
		})
	})
}

func (a *app) openURL(u string) {
	if !ui.SafeToOpen(u) {
		return
	}
	a.hide()
	a.open(u)
}

func (a *app) back() {
	if a.page == pageSettings && !a.configured() {
		return
	}
	a.changingKey, a.confirmClear = false, false
	a.page = pageSearch
	if len(a.results) > 0 && !a.showingHistory() {
		a.focusList = true // as the list is built again
	} else {
		a.input.Focus()
	}
}

func (a *app) openSettings() { a.openTab(a.tab) }

// openTab opens the settings at tab.
func (a *app) openTab(tab int) {
	if a.page != pageSettings {
		a.editKey, a.tvEdit, a.editShortcut, a.settingsMsg, a.settingsErr = "", "", a.cfg.Shortcut, "", false
		a.changingKey, a.tvChanging = false, false
		a.editLow, a.editBudget, a.usageMsg = num(a.cfg.LowBalance), num(a.cfg.MonthlyBudget), ""
		if a.cfg.MonthlyBudget == 0 {
			a.editBudget = ""
		}
	}
	if !a.configured() && tab != tabKey && a.page != pageSettings {
		tab = tabKey
	}
	a.page, a.tab = pageSettings, max(0, min(tab, len(tabNames)-1))
	if a.tab == tabUsage || a.tab == tabGeneral {
		a.refreshBalance(false)
	}
}

// saveKey checks the key typed with a one-token chat, then keeps it.
func (a *app) saveKey() {
	key := strings.TrimSpace(a.editKey)
	if key == "" || a.checking {
		return
	}
	a.checking, a.settingsMsg, a.settingsErr = true, "正在验证…", false
	src, ks, mode := a.newSrc(a.cfg, key, a.tvKey), a.keys, a.cfg.KeyStore
	a.spawn(func() {
		err := src.Check(context.Background())
		var from, warn string
		if err == nil {
			from, warn, err = ks.save(key, mode)
		}
		a.post(func() {
			a.checking = false
			if err != nil {
				a.settingsMsg, a.settingsErr = errText(err), true
				return
			}
			first := !a.configured()
			a.bind(src)
			a.keyFrom, a.key, a.editKey, a.changingKey, a.keyErr = from, key, "", false, ""
			switch {
			case warn != "": // and from now on, not the keychain
				a.cfg.KeyStore = "file"
				a.changed(a.cfg)
				a.settingsMsg, a.settingsErr = warn+"，已改存到本地文件（备用）", true
			case from == "file":
				a.settingsMsg, a.settingsErr = "已验证并保存到本地文件", false
			default:
				a.settingsMsg, a.settingsErr = "已验证并保存到「登录」钥匙串", false
			}
			if first && warn == "" { // set up: on to searching
				a.page = pageSearch
				a.input.Focus()
			}
		})
	})
}

// deleteKey forgets the key everywhere.
func (a *app) deleteKey() {
	if err := a.keys.remove(); err != nil {
		a.settingsMsg, a.settingsErr = "删除失败："+err.Error(), true
		return
	}
	a.key, a.keyFrom, a.changingKey, a.keyErr = "", "", false, ""
	if k := strings.TrimSpace(a.keys.env("DEEPSEEK_API_KEY")); k != "" {
		a.bind(a.newSrc(a.cfg, k, a.tvKey))
		a.key, a.keyFrom = k, "env"
	} else {
		a.rebuildSource()
	}
	a.settingsMsg, a.settingsErr = "已删除", false
}

// saveTavilyKey checks the Tavily key with one minimal search, then keeps it.
func (a *app) saveTavilyKey() {
	key := strings.TrimSpace(a.tvEdit)
	if key == "" || a.checking {
		return
	}
	a.checking, a.settingsMsg, a.settingsErr = true, "正在验证 Tavily Key…", false
	ks, mode := a.keys, a.cfg.KeyStore
	a.spawn(func() {
		err := websearch.NewTavily(key).Check(context.Background())
		var from, warn string
		if err == nil {
			from, warn, err = ks.saveTavily(key, mode)
		}
		a.post(func() {
			a.checking = false
			if err != nil {
				a.settingsMsg, a.settingsErr = errText(err), true
				return
			}
			a.tvKey, a.tvFrom, a.tvErr = key, from, ""
			a.tvEdit, a.tvChanging = "", false
			a.cfg.Source = "tavily"
			a.changed(a.cfg)
			a.rebuildSource()
			switch {
			case warn != "":
				a.cfg.KeyStore = "file"
				a.changed(a.cfg)
				a.settingsMsg, a.settingsErr = warn+"，已改存到本地文件（备用）", true
			default:
				a.settingsMsg, a.settingsErr = "Tavily Key 已验证，搜索源已切到 Tavily", false
			}
		})
	})
}

// deleteTavilyKey forgets the Tavily key; the search goes back to DeepSeek
// unless TAVILY_API_KEY supplies another.
func (a *app) deleteTavilyKey() {
	if err := a.keys.removeTavily(); err != nil {
		a.settingsMsg, a.settingsErr = "删除失败："+err.Error(), true
		return
	}
	a.tvKey, a.tvFrom, a.tvErr, a.tvChanging = "", "", "", false
	if k := strings.TrimSpace(a.keys.env("TAVILY_API_KEY")); k != "" {
		a.tvKey, a.tvFrom = k, "env"
	} else if a.cfg.Source == "tavily" {
		a.cfg.Source = ""
		a.changed(a.cfg)
	}
	a.rebuildSource()
	a.settingsMsg, a.settingsErr = "Tavily Key 已删除", false
}

// reloadTavilyKey reads the key again, as after allowing the keychain.
func (a *app) reloadTavilyKey() {
	st := a.keys.loadTavily(a.cfg.KeyStore)
	a.tvErr = st.err
	if st.key != "" {
		a.tvKey, a.tvFrom = st.key, st.from
		a.rebuildSource()
		a.settingsMsg, a.settingsErr = "已读取 Tavily Key", false
	} else {
		a.settingsMsg, a.settingsErr = "仍然读不到 Tavily Key", true
	}
}

// setSource switches the web search provider.
func (a *app) setSource(source string) {
	if source == a.cfg.Source {
		return
	}
	a.cfg.Source = source
	a.changed(a.cfg)
	a.rebuildSource()
	if source == "tavily" && a.tvKey == "" {
		a.settingsMsg, a.settingsErr = "已切到 Tavily，请先填入并验证 Tavily API Key", true
		return
	}
	a.settingsMsg, a.settingsErr = "搜索源已切换", false
}

// reloadKey reads the key again, as after allowing the keychain.
func (a *app) reloadKey() {
	st := a.keys.load(a.cfg.KeyStore)
	a.keyErr = st.err
	if st.key != "" {
		a.bind(a.newSrc(a.cfg, st.key, a.tvKey))
		a.key, a.keyFrom = st.key, st.from
		a.settingsMsg, a.settingsErr = "已读取", false
	} else {
		a.settingsMsg, a.settingsErr = "仍然读不到", true
	}
}

// setKeyStore moves the saved key to the keychain or the file.
func (a *app) setKeyStore(mode string) {
	if mode == a.cfg.KeyStore {
		return
	}
	a.cfg.KeyStore = mode
	a.changed(a.cfg)
	moved, movedTo, warned := false, "", ""
	if a.key != "" && a.keyFrom != "env" {
		from, warn, err := a.keys.save(a.key, mode)
		if err != nil {
			a.settingsMsg, a.settingsErr = errText(err), true
			return
		}
		a.keyFrom, moved, movedTo = from, true, from
		warned = warn
	}
	if a.tvKey != "" && a.tvFrom != "env" {
		from, warn, err := a.keys.saveTavily(a.tvKey, mode)
		if err != nil {
			a.settingsMsg, a.settingsErr = "Tavily Key 移动失败："+errText(err), true
			return
		}
		a.tvFrom, moved, movedTo = from, true, from
		if warned == "" {
			warned = warn
		}
	}
	if !moved {
		return
	}
	switch {
	case warned != "":
		a.settingsMsg, a.settingsErr = warned+"，仍存在本地文件", true
	default:
		a.settingsMsg, a.settingsErr = "已移到"+storeName(movedTo), false
	}
}

func storeName(from string) string {
	switch from {
	case "keychain":
		return "macOS「登录」钥匙串"
	case "file":
		return "本地文件（备用）"
	case "env":
		return "环境变量 DEEPSEEK_API_KEY"
	}
	return "未保存"
}

func storeNameFor(from, envName string) string {
	if from == "env" {
		return "环境变量 " + envName
	}
	return storeName(from)
}

// forget deletes history item i; clearHistory all of it.
func (a *app) forget(i int) {
	if i < 0 || i >= len(a.cfg.History) {
		return
	}
	a.cfg.History = append(a.cfg.History[:i:i], a.cfg.History[i+1:]...)
	a.changed(a.cfg)
	if a.chosen >= a.rows() {
		a.chosen = a.rows() - 1
	}
}

func (a *app) clearHistory() {
	a.cfg.History, a.confirmClear, a.chosen = nil, false, -1
	a.changed(a.cfg)
}

// setHistory turns the history on or off; off, it is cleared.
func (a *app) setHistory(on bool) {
	a.cfg.NoHistory = !on
	if !on {
		a.cfg.History = nil
	}
	a.changed(a.cfg)
}

func (a *app) applyShortcut() {
	s := strings.TrimSpace(a.editShortcut)
	if s == "" || s == a.cfg.Shortcut {
		return
	}
	if err := a.setShort(s); err != nil {
		a.settingsMsg, a.settingsErr = "快捷键不可用："+err.Error(), true
		return
	}
	a.cfg.Shortcut = s
	a.changed(a.cfg)
	a.settingsMsg, a.settingsErr = "快捷键已改为 "+keys(s), false
}

var keyLeak = regexp.MustCompile(`(?i)[,;]?\s*your api key:\s*\S+`)

func errText(err error) string {
	if err == nil {
		return ""
	}
	return keyLeak.ReplaceAllString(err.Error(), "")
}

// loadIcon fetches a site's icon once, in the background.
func (a *app) icon(u string) *ui.Bitmap {
	if u == "" {
		return nil
	}
	if b := a.icons[u]; b != nil || a.asked[u] {
		return b
	}
	a.asked[u] = true
	a.spawn(func() {
		b := fetchIcon(u)
		a.post(func() { a.icons[u] = b })
	})
	return nil
}

var iconClient = &http.Client{Timeout: 8 * time.Second}

func fetchIcon(u string) *ui.Bitmap {
	res, err := iconClient.Get(u)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 256<<10))
	if err != nil {
		return nil
	}
	if b, err := ui.DecodeBitmap(data); err == nil {
		return b
	}
	if img, err := decodeICO(data); err == nil {
		return ui.NewBitmap(img)
	}
	return nil // SVG, a page…: the site's initial instead
}

// citations turns [n] into links to the n-th result, as text streams in.
func citations(s string, rs []dsapi.Result) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '[' {
			j := i + 1
			for j < len(s) && j-i <= 3 && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j > i+1 && j < len(s) && s[j] == ']' && (j+1 >= len(s) || s[j+1] != '(') {
				var n int
				fmt.Sscan(s[i+1:j], &n)
				if n >= 1 && n <= len(rs) {
					fmt.Fprintf(&b, "[[%d]](%s)", n, mdURL(rs[n-1].URL))
					i = j
					continue
				}
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// citeLinks makes the [n] of an answer links to its n-th source, which
// the conversation selects on the left (cite).
func citeLinks(s string, n int) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '[' {
			j := i + 1
			for j < len(s) && j-i <= 3 && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j > i+1 && j < len(s) && s[j] == ']' && (j+1 >= len(s) || s[j+1] != '(') {
				var k int
				fmt.Sscan(s[i+1:j], &k)
				if k >= 1 && k <= n {
					fmt.Fprintf(&b, "[[%d]](cite:%d)", k, k)
					i = j
					continue
				}
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func mdURL(u string) string {
	return strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29", "<", "%3C", ">", "%3E").Replace(u)
}

// when tells a result's date in Beijing time, shortly.
func when(r dsapi.Result, now time.Time) string {
	t := r.Time()
	if t.IsZero() {
		return ""
	}
	t, now = t.In(dsapi.Shanghai), now.In(dsapi.Shanghai)
	d := now.Sub(t)
	switch {
	case d < 0:
		return t.Format("2006-01-02")
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", max(1, int(d.Minutes())))
	case d < 24*time.Hour && t.Day() == now.Day():
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case t.Year() == now.Year():
		return t.Format("1月2日")
	}
	return t.Format("2006年1月2日")
}

// unescapeMD undoes the escaping of every punctuation mark that some
// fetched pages come back with ("\#\# 标题", "https\:\/\/…"), so that
// their headings, lists and tables show as such.
func unescapeMD(s string) string {
	n := strings.Count(s, `\#`) + strings.Count(s, `\-`) + strings.Count(s, `\|`) + strings.Count(s, `\/`) + strings.Count(s, `\.`)
	if n < 5 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[]^_`{|}~", s[i+1]) >= 0 {
			// Keep \* \_ \` \[ \] \< \> escaped: they rarely mean syntax here
			// and unescaped they could swallow text.
			if strings.IndexByte("*_`[]<>", s[i+1]) >= 0 {
				b.WriteByte('\\')
			}
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
