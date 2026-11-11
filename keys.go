package main

import (
	"runtime"
	"strings"

	"github.com/egoist/mygo/ui"
)

// macHints spells keys as macOS does (⌘, ⌥), on macOS; tests set it to
// render the Mac's hints elsewhere.
var macHints = runtime.GOOS == "darwin"

func cmdKey() string {
	if macHints {
		return "⌘"
	}
	return "Ctrl+"
}

// keys spells a shortcut as macOS does: ⌥Space.
func keys(shortcut string) string {
	if !macHints {
		return shortcut
	}
	return strings.NewReplacer("CmdOrCtrl+", "⌘", "Cmd+", "⌘", "Ctrl+", "⌃", "Alt+", "⌥", "Option+", "⌥", "Shift+", "⇧", "Space", "空格").Replace(shortcut)
}

// keysSearch: the keyboard on the search page. After a search it is on the
// answer, as in Spotlight: ⇥ brings in the sources, ↑↓ choose one, ↵ opens
// it, space previews it; typing goes back to the query (listInput). On the
// query, ↵ searches and ↓ goes down to the sources.
func (a *app) keysSearch(c *ui.Context) {
	onChat := a.cfg.AI && a.chatInput.Focused(c)
	onList := a.listFocus.Focused(c) && len(a.results) > 0 && !a.showingHistory()
	if c.Shortcut(0, ui.KeyEscape) {
		// esc always hides the panel: it is called with ⌥Space to be
		// dismissed. ⇧⇥ is what closes the sources, then the answer.
		a.hide()
	}
	if c.Shortcut(ui.Cmd, ui.KeyL) {
		a.focusQuery = true
		a.input.Focus()
	}
	if c.Shortcut(ui.Shift, ui.KeyTab) {
		// esc hides the panel, so ⇧⇥ is the keyboard's way back: it
		// closes the sources, and with them already closed, the answer.
		if a.cfg.Sources {
			a.hideSources()
		} else {
			a.toggleSummary()
		}
		return
	}
	if a.cfg.AI && c.Shortcut(ui.Cmd, ui.KeyN) {
		a.newChat()
	}
	if onChat {
		switch {
		case c.Shortcut(0, ui.KeyTab):
			if len(a.results) > 0 && !a.showingHistory() {
				a.showSources()
			} else {
				a.input.Focus()
			}
		case c.Shortcut(0, ui.KeyDown) && strings.TrimSpace(a.draft) == "":
			// ↓ opens the sources, as it does on the query.
			a.showSources()
			a.choose(max(a.chosen, 0))
		case c.Shortcut(ui.Cmd, ui.KeyEnter):
			if a.chosen >= 0 && a.chosen < len(a.results) {
				a.openURL(a.results[a.chosen].URL)
			}
		}
		for i, key := range digitKeys {
			if c.Shortcut(ui.Cmd, key) {
				a.citeNth(i + 1)
			}
		}
		return
	}
	if c.Shortcut(0, ui.KeyTab) || c.Shortcut(ui.Cmd, ui.KeyEnter) {
		switch {
		case !a.cfg.AI: // the answer, and the keyboard to it
			a.summarize(true)
		case onList: // back to the answer
			a.focusChat = true
		default: // the sources, opened for the reading
			a.showSources()
		}
	}
	if !onList {
		switch {
		case a.showingHistory() && c.Shortcut(0, ui.KeyDown):
			a.choose(a.chosen + 1)
		case a.showingHistory() && c.Shortcut(0, ui.KeyUp):
			a.choose(a.chosen - 1)
		case len(a.results) > 0 && !a.showingHistory() && c.Shortcut(0, ui.KeyDown):
			if a.cfg.AI {
				a.showSources()
			} else {
				a.listFocus.Focus()
			}
			a.choose(max(a.chosen, 0))
		}
		return
	}
	if c.Shortcut(ui.Alt, ui.KeyLeft) {
		a.switchSet(-1)
	}
	if c.Shortcut(ui.Alt, ui.KeyRight) {
		a.switchSet(1)
	}
	last := len(a.results) - 1
	if a.expandErr != "" && a.srcSet == 0 {
		last++ // the retry row
	}
	switch {
	case c.Shortcut(0, ui.KeyDown):
		a.choose(min(a.chosen+1, last)) // the last stays
	case c.Shortcut(0, ui.KeyUp):
		if a.chosen <= 0 {
			a.input.Focus() // above the first: the query
		} else {
			a.choose(a.chosen - 1)
		}
	case c.Shortcut(ui.Cmd, ui.KeyDown), c.Shortcut(0, ui.KeyEnd):
		a.choose(last)
	case c.Shortcut(ui.Cmd, ui.KeyUp), c.Shortcut(0, ui.KeyHome):
		a.choose(0)
	case c.Shortcut(0, ui.KeyPageDown):
		a.choose(min(a.chosen+6, last))
	case c.Shortcut(0, ui.KeyPageUp):
		a.choose(a.chosen - 6)
	case c.Shortcut(0, ui.KeyEnter):
		if a.chosen == len(a.results) {
			a.retry()
		} else if a.chosen >= 0 {
			a.openURL(a.results[a.chosen].URL)
		}
	case a.composing == "" && c.Shortcut(0, ui.KeySpace), c.Shortcut(ui.Cmd, ui.KeyR):
		a.read()
	case c.Shortcut(ui.Cmd, ui.KeyC):
		a.copyLink()
	}
	for i, key := range digitKeys {
		if c.Shortcut(ui.Cmd, key) {
			a.openNth(i)
		}
	}
}

// queryKey: on the history, with nothing typed, ⌫ (or ⌘⌫) deletes the
// chosen search.
func (a *app) queryKey(k ui.TextKey) bool {
	if k.Key != ui.KeyBackspace || k.Mods&^ui.Cmd != 0 || strings.TrimSpace(k.Text()) != "" || !a.showingHistory() {
		return false
	}
	if a.chosen < 0 || a.chosen >= a.rows() {
		return false
	}
	a.forget(a.chosen)
	return true
}

var digitKeys = []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9}

// listInput takes the typing on the results back to the query: a
// character, or one composed with an input method, adds to it; ⌫ takes
// its last off.
func (a *app) listInput(ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputCompose:
		a.composing = ev.Text
		return true
	case ui.InputText:
		a.composing = ""
		if ev.Text == "" || ev.Text == " " || ev.Text == "\t" || ev.Text == "\r" || ev.Text == "\n" {
			return false
		}
		a.query += ev.Text
	case ui.InputKeyDown:
		if ev.Key != ui.KeyBackspace || ev.Mods != 0 {
			return false
		}
		if r := []rune(a.query); len(r) > 0 {
			a.query = string(r[:len(r)-1])
		}
	default:
		return false
	}
	a.caretToEnd = true
	a.input.Focus()
	return true
}

// retry tries the search of 50 again, after it failed.
func (a *app) retry() {
	a.expandErr = ""
	a.loadMore()
}

// keysReader: the preview scrolls with ↑↓ and space; ↵ opens the page in
// the browser; esc and ← go back to the results.
func (a *app) keysReader(c *ui.Context) {
	s := &a.readerScroll
	screen := float32(panelHeight - 140)
	by := func(dy float32) { s.ScrollTo(0, max(0, min(s.MaxY, s.Y+dy)), ui.Smooth) }
	switch {
	case c.Shortcut(0, ui.KeyEscape), c.Shortcut(0, ui.KeyLeft):
		a.back()
	case c.Shortcut(0, ui.KeyEnter), c.Shortcut(ui.Cmd, ui.KeyEnter):
		a.openURL(a.readerURL)
	case c.Shortcut(0, ui.KeyDown):
		by(64)
	case c.Shortcut(0, ui.KeyUp):
		by(-64)
	case c.Shortcut(0, ui.KeySpace), c.Shortcut(0, ui.KeyPageDown):
		by(screen)
	case c.Shortcut(ui.Shift, ui.KeySpace), c.Shortcut(0, ui.KeyPageUp):
		by(-screen)
	case c.Shortcut(0, ui.KeyEnd), c.Shortcut(ui.Cmd, ui.KeyDown):
		s.ScrollTo(0, s.MaxY, ui.Smooth)
	case c.Shortcut(0, ui.KeyHome), c.Shortcut(ui.Cmd, ui.KeyUp):
		s.ScrollTo(0, 0, ui.Smooth)
	}
}

// shortcutGroups is the cheatsheet (⌘/), and the README's table.
func shortcutGroups() [][3]string {
	m := cmdKey()
	return [][3]string{
		{"输入框", "↵", "搜索，并让 AI 总结（默认自动）"},
		{"", "↓", "展开来源（右侧抽屉）并进入列表"},
		{"来源（右侧）", "↑ ↓", "选择（滚到底自动加载更多）"},
		{"", "↵", "在浏览器打开"},
		{"", "空格", "预览（阅读模式）"},
		{"", "⇥", "回到 AI 总结"},
		{"", "⇧⇥", "收起来源（再按一次只看搜索结果）"},
		{"", m + "C", "复制链接"},
		{"", "直接打字  /  ⌫", "回到输入框改搜索词"},
		{"", m + "↑  " + m + "↓", "到第一条 / 最后一条"},
		{"", "⌥←  ⌥→", "切换来源：原始搜索 / 各轮追问搜到的"},
		{"", m + "1 … " + m + "9", "打开第 N 条"},
		{"AI 总结（中间）", "↵  /  ⇧↵", "发送追问 / 换行"},
		{"", "⇥", "展开来源，键盘到列表"},
		{"", "↑（输入框为空时）", "取回上一个问题"},
		{"", m + "1 … " + m + "9", "在来源里选中 [N]，再按一次打开"},
		{"", m + "↵", "打开选中的来源"},
		{"", m + "N", "新对话（保留搜索结果）"},
		{"", "esc", "隐藏面板（⇧⇥ 收起来源，再按只看结果）"},
		{"预览", "↑ ↓  /  空格", "滚动"},
		{"", "↵", "在浏览器打开原文"},
		{"", "esc  /  ←", "返回结果"},
		{"通用", keys(defaultShortcut), "唤起 / 隐藏面板（可在设置里改）"},
		{"", m + "L", "回到输入框并全选"},
		{"", m + ",", "设置"},
		{"", m + "U", "用量与费用"},
		{"", m + "/", "本快捷键表"},
		{"", m + "Q", "退出 DS 搜索"},
	}
}

// hint is the hint bar's text for a list of keys and what they do.
func hint(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		if b.Len() > 0 {
			b.WriteString("   ")
		}
		b.WriteString(pairs[i] + " " + pairs[i+1])
	}
	return b.String()
}

// hintBar is the row at the bottom of each view.
func hintBar(c *ui.Context, k ink, left, right string) {
	ui.Row(c).Padding(8, 16).Gap(14).AlignItems(ui.Center).FontSize(11).TextColor(k.faint).Children(func() {
		ui.Text(c, left).Grow(1).Shrink(1).SingleLine()
		ui.Text(c, right).SingleLine()
	})
}

func (a *app) cheatsheet(c *ui.Context, k ink) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(14, 20).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "键盘快捷键").FontSize(15).FontWeight(650).Grow(1)
			ui.Text(c, "全键盘操作，鼠标可选").FontSize(12).TextColor(k.faint)
		})
		ui.Divider(c)
		ui.Scroll(c.Key("cheat")).Grow(1).Padding(10, 24, 14, 24).Gap(1).Children(func() {
			for i, g := range shortcutGroups() {
				if g[0] != "" {
					top := float32(10)
					if i == 0 {
						top = 2
					}
					ui.Text(c, g[0]).FontSize(11).FontWeight(600).TextColor(k.faint).Margin(top, 0, 3, 0)
				}
				ui.Row(c.Key(i)).Gap(14).Padding(3, 0).AlignItems(ui.Center).Children(func() {
					ui.Text(c, g[1]).FontSize(12).FontWeight(600).TextColor(k.link).Width(190).SingleLine()
					ui.Text(c, g[2]).FontSize(12).TextColor(k.text).Grow(1).SingleLine()
				})
			}
		})
		ui.Divider(c)
		hintBar(c, k, keys(a.cfg.Shortcut)+" 随时唤起", hint("esc", "关闭"))
	})
}

func (a *app) copyLink() {
	if a.chosen < 0 || a.chosen >= len(a.results) {
		return
	}
	a.copy(a.results[a.chosen].URL)
	a.toast = "已复制链接"
}

// openNth opens the n-th result of the page (0-based).
func (a *app) openNth(n int) {
	if a.showingHistory() || n >= len(a.results) {
		return
	}
	a.choose(n)
	a.openURL(a.results[n].URL)
}
