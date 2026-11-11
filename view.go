package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"ds-search/billing"
	"ds-search/dsapi"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// The panel: the results beside the AI conversation, which is the
// default, so a search is read as its summary. With the conversation
// closed (⇧⇥) the results are all there is.
const (
	panelWidth, panelHeight = 720, 560
	expandedWidth           = 1180
	leftWidth               = 430 // the sources beside the summary
)

var (
	iconSearch   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>`))
	iconGear     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></svg>`))
	iconBack     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m15 18-6-6 6-6"/></svg>`))
	iconClock    = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/></svg>`))
	iconSparkles = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9.94 14.06 8 20l-1.94-5.94L0 12l6.06-1.94L8 4l1.94 6.06L16 12z" transform="translate(2 -1) scale(.9)"/><path d="M20 3v4M22 5h-4"/></svg>`))
	iconBook     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2z"/><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z"/></svg>`))
	iconAlert    = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="M12 8v4M12 16h.01"/></svg>`))
)

// ink is the app's palette for the window's theme.
type ink struct {
	bg, text, muted, faint, accent, link, line, hover, chosen, gold, green, card, danger ui.Color
}

// colors are the palette's opaque colors, by name; every text color is
// WCAG AA (4.5:1) on bg, as TestContrast checks.
var colors = map[bool]map[string]string{
	false: {"bg": "#ffffff", "text": "#1f2328", "muted": "#57606a", "faint": "#6e7781", "link": "#1a4fd6",
		"gold": "#8a5a00", "green": "#15803d", "danger": "#c62828", "line": "#d8dce1"},
	true: {"bg": "#1c1d21", "text": "#e6e8eb", "muted": "#b0b5bd", "faint": "#8f96a0", "link": "#8ab4f8",
		"gold": "#f4c152", "green": "#6dd58c", "danger": "#ff8a80", "line": "#3a3d44"},
}

func palette(c *ui.Context) ink {
	t := c.Theme()
	p := colors[t.Dark]
	h := func(n string) ui.Color { return ui.Hex(p[n]) }
	k := ink{bg: h("bg"), text: h("text"), muted: h("muted"), faint: h("faint"), link: h("link"), accent: t.Accent,
		gold: h("gold"), green: h("green"), danger: h("danger"), line: h("line")}
	if t.Dark {
		k.hover = ui.RGB(255, 255, 255).Alpha(0.05)
		k.chosen = k.link.Alpha(0.16)
		k.card = ui.RGB(255, 255, 255).Alpha(0.045)
	} else {
		k.hover = ui.RGB(0, 0, 0).Alpha(0.04)
		k.chosen = k.link.Alpha(0.09)
		k.card = ui.Hex("#f4f5f7")
	}
	return k
}

func (a *app) view(c *ui.Context) {
	k := palette(c)
	root := c.Root().TextColor(k.text)
	if a.cfg.Translucent && c.Vibrancy() {
		root.Background(ui.Transparent)
	} else { // opaque, framed: readable over any page
		root.Background(k.bg).Radius(12).BorderWidth(1).BorderColor(k.line)
	}
	c.OnShortcut(ui.Cmd, ui.KeyQ, a.quit)
	c.OnShortcut(ui.Cmd, ui.KeyComma, a.openSettings)
	c.OnShortcut(ui.Cmd, ui.KeyU, a.openUsage)
	if c.Shortcut(ui.Cmd, ui.KeySlash) || c.Shortcut(ui.Cmd|ui.Shift, ui.KeySlash) {
		a.cheat = !a.cheat
	}
	if a.cheat {
		if c.Shortcut(0, ui.KeyEscape) {
			a.cheat = false
		}
		a.cheatsheet(c, k)
		return
	}

	switch a.page {
	case pageSettings:
		if c.Shortcut(0, ui.KeyEscape) {
			if a.configured() {
				a.back()
			} else {
				a.hide()
			}
		}
		a.settings(c, k)
	case pageReader:
		a.keysReader(c)
		a.reader(c, k)
	default:
		a.keysSearch(c)
		a.searchPage(c, k)
	}
}

func (a *app) searchPage(c *ui.Context, k ink) {
	a.listFocused = a.listFocus.Focused(c)
	ui.Column(c).Fill().Children(func() {
		// The search field.
		ui.Row(c).Padding(16, 18, 14, 20).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconSearch).FontSize(20).TextColor(k.muted)
			in := ui.TextInputBase(c.Key("q"), &a.query).Bind(&a.input).Grow(1).FontSize(21).
				Placeholder("搜索网页，或输入 site: after: \"…\"").Label("搜索").OnTextKey(a.queryKey)
			if in.Changed() && a.showingHistory() {
				a.choose(0)
			}
			if a.focusQuery { // ⌘L: the query, all selected, to type anew
				a.focusQuery = false
				in.SetTextSelection(0, utf8.RuneCountInString(a.query))
			}
			if a.caretToEnd { // typed on the results: carry on at the end
				a.caretToEnd = false
				n := utf8.RuneCountInString(a.query)
				in.SetTextSelection(n, n)
			}
			if in.Submitted() {
				a.submit()
			}
			if a.composing != "" { // composed on the results, on its way here
				ui.Text(c, a.composing).FontSize(18).TextColor(k.muted).Underline()
			}
			if a.searching {
				ui.Spinner(c).Size(16, 16)
			}
			a.aiChip(c.Key("ai"), k)
			ui.Box(c.Key("gear")).Padding(4).Radius(6).Cursor(ui.CursorPointer).Tooltip("设置  " + cmdKey() + ",").
				OnClick(a.openSettings).Children(func() {
				ui.Icon(c, iconGear).FontSize(16).TextColor(k.faint)
			})
		})
		ui.Divider(c)
		if w := a.warning(); w != "" {
			ui.Row(c.Key("warn")).Gap(8).Padding(7, 20).AlignItems(ui.Center).Background(k.gold.Alpha(0.12)).
				Cursor(ui.CursorPointer).OnClick(a.openUsage).Children(func() {
				ui.Icon(c, iconAlert).FontSize(13).TextColor(k.gold)
				ui.Text(c, w).FontSize(12).TextColor(k.gold).Grow(1).SingleLine()
				ui.Text(c, "查看用量 ›").FontSize(12).TextColor(k.gold)
			})
		}

		if a.cfg.AI {
			panes := ui.Row(c.Key("panes")).Grow(1).MinHeight(0).AlignItems(ui.Stretch)
			if w := panes.Bounds().W; w > 0 {
				a.panesW = w
			}
			panes.Children(func() {
				ui.Column(c.Key("left")).Width(a.leftWidth()).MinHeight(0).Children(func() {
					a.setChips(c.Key("chips"), k)
					a.resultsPane(c, k)
				})
				a.splitter(c.Key("split"), k)
				a.chatPane(c.Key("chat"), k)
			})
		} else {
			a.resultsPane(c, k)
		}
		ui.Divider(c)
		a.footer(c, k)
	})
}

// The left pane is at least minLeft wide, the right at least minRight.
const minLeft, minRight = 300, 340

// splitWidth is the left pane's width, of panes w wide, at split (the
// left's share; 0 for the default).
func splitWidth(split, w float32) float32 {
	if w <= 0 {
		return leftWidth
	}
	if split <= 0 {
		split = float32(leftWidth) / expandedWidth
	}
	return max(min(split*w, w-minRight), min(minLeft, w/2))
}

func (a *app) leftWidth() float32 {
	w := a.panesW
	if w == 0 {
		w = float32(widthFor(a.cfg, true, 1<<20))
	}
	return splitWidth(float32(a.cfg.Split), w)
}

// splitter is the line between the panes: drag it to share the width out
// anew, double-click it for the default. The share is remembered.
func (a *app) splitter(c *ui.Context, k ink) {
	line := k.line
	h := ui.Box(c).Width(7).Margin(0, -3).Cursor(ui.CursorResizeColumn).Label("分隔条")
	if h.Hovered() {
		line = k.link.Alpha(0.6)
	}
	if dx, _, ok := h.Dragged(); ok && a.panesW > 0 {
		line = k.link
		if dx != 0 {
			a.cfg.Split = float64(splitWidth(float32(a.cfg.Split), a.panesW)+dx) / float64(a.panesW)
			a.cfg.Split = float64(splitWidth(float32(a.cfg.Split), a.panesW)) / float64(a.panesW)
		}
	}
	if _, _, ok := h.DragEnded(); ok {
		a.changed(a.cfg)
	}
	if h.DoubleClicked() {
		a.cfg.Split = 0
		a.changed(a.cfg)
	}
	h.AlignItems(ui.Center).Children(func() {
		ui.Box(c).Width(1).Grow(1).Background(line)
	})
}

// resultsPane is the results list, or the history, or why there are none.
func (a *app) resultsPane(c *ui.Context, k ink) {
	switch {
	case a.searchErr != "":
		ui.Column(c).Grow(1).Children(func() { notice(c, k, a.searchErr, true) })
	case a.showingHistory():
		ui.Scroll(c.Key("hist")).Grow(1).Padding(8, 10).Gap(2).Children(func() { a.history(c, k) })
	default:
		// Near the end, by scrolling or choosing, the rest loads.
		// Only once the list has been scrolled: in a short pane the end of
		// 10 results is in view at once, and that must not ask for 50.
		if a.moreLeft() && a.listScroll.Y > 0 && a.listScroll.MaxY-a.listScroll.Y < 150 {
			a.loadMore()
		}
		end := 0
		if a.endRow() {
			end = 1
		}
		n := len(a.results)
		a.list.Key = func(i int) any {
			if i >= n {
				return "end"
			}
			return a.results[i].URL
		}
		ui.List(c.Key("results"), &a.list, n+end, func(i int) {
			if i >= n {
				a.endOfList(c.Key("end"), k)
			} else {
				a.row(c.Key(i), k, i)
			}
		}).TrackScroll(&a.listScroll).Bind(&a.listFocus).Focusable().InputMethod(true).Label("搜索结果").HandleInput(a.listInput).
			Grow(1).MinHeight(0).Padding(8, 10).Gap(4).Children(func() {
			if !a.searching && len(a.results) == 0 && a.searched != "" {
				notice(c, k, "没有找到相关结果。换个说法，或去掉 site:/after: 试试。", false)
			}
		})
		if a.focusList {
			a.focusList = false
			a.listFocus.Focus()
		}
	}
}

// aiChip opens and closes the AI conversation: ⇥ opens it, ⇧⇥ closes it.
func (a *app) aiChip(c *ui.Context, k ink) {
	bg, fg := ui.Transparent, k.faint
	if a.cfg.AI {
		bg, fg = k.chosen, k.link
	}
	// The chip stays called 对话: 总结 is the answer's own heading, which
	// only shows while this pane is open.
	ui.Row(c).Gap(4).Padding(4, 9).Radius(20).Background(bg).AlignItems(ui.Center).Cursor(ui.CursorPointer).
		Tooltip("AI 总结  ⇥ 打开 / ⇧⇥ 收起").Label("AI 对话").OnClick(a.toggleAI).Children(func() {
		ui.Icon(c, iconSparkles).FontSize(13).TextColor(fg)
		ui.Text(c, "AI 对话").FontSize(12).FontWeight(550).TextColor(fg)
	})
}

// row is a result: the site and its date, the title, the snippet.
//
// Beside the summary (dense) a result is a source: numbered like the [n]
// the answer cites, one line of title, one of site, no snippet. With the
// conversation closed the list is all there is, and the rows breathe.
func (a *app) row(c *ui.Context, k ink, i int) {
	r := a.results[i]
	dense := a.cfg.AI
	bg := ui.Transparent
	if i == a.chosen {
		bg = k.chosen // the keyboard is on the results: clearly
		if !a.listFocused {
			bg = k.hover // on the query: subtly
		}
	}
	pad, gap, rad := float32(9), float32(3), float32(10)
	if dense {
		pad, gap, rad = 6, 1, 9
	}
	row := ui.Column(c).Padding(pad, 12, pad+1, 12).Radius(rad).Background(bg).Gap(gap).Cursor(ui.CursorPointer)
	if row.Hovered() && i != a.chosen {
		row.Background(k.hover)
	}
	row.OnClick(func() {
		if a.chosen == i {
			a.openURL(r.URL)
		} else {
			a.choose(i)
		}
	})
	row.Children(func() {
		ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
			if dense {
				// Its number, so the row and the answer's [n] are one thing.
				ui.Text(c, fmt.Sprintf("%d.", i+1)).FontSize(11).FontWeight(600).TextColor(k.faint)
			}
			site := siteName(r)
			if b := a.icon(r.SiteIcon); b != nil {
				ui.Image(c, b).Size(16, 16).Radius(4)
			} else {
				ui.Avatar(c, site, nil).Size(16, 16).FontSize(9)
			}
			ui.Text(c, site).FontSize(12).TextColor(k.muted).SingleLine().Shrink(1)
			if !dense {
				ui.Text(c, host(r.URL)).FontSize(11).TextColor(k.faint).SingleLine().Shrink(2)
			}
			if d := when(r, time.Now()); d != "" {
				ui.Text(c, "· "+d).FontSize(11).TextColor(k.faint).SingleLine()
			}
			ui.Spacer(c)
			if r.Authoritative() {
				badge(c, "权威", k.green)
			}
			if r.Good() {
				badge(c, "优质", k.gold)
			}
			if i == a.chosen {
				ui.Row(c.Key("read")).Gap(4).Padding(2, 7).Radius(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).
					Tooltip("预览  空格").Label("预览").OnClick(a.read).Children(func() {
					ui.Icon(c, iconBook).FontSize(12).TextColor(k.link)
					ui.Text(c, "预览").FontSize(11).TextColor(k.link)
				})
			}
		})
		size := float32(16)
		if dense {
			size = 13.5
		}
		ui.Text(c, oneLine(r.Title)).FontSize(size).FontWeight(500).TextColor(k.link).MaxLines(1)
		if r.Snippet != "" && !dense {
			ui.Text(c, oneLine(r.Snippet)).FontSize(13).LineHeight(1.45).TextColor(k.muted).MaxLines(2)
		}
	})
}

func badge(c *ui.Context, text string, col ui.Color) {
	ui.Text(c, text).FontSize(10).FontWeight(600).TextColor(col).Padding(1, 6).Radius(5).Background(col.Alpha(0.13))
}

func (a *app) history(c *ui.Context, k ink) {
	if len(a.cfg.History) == 0 {
		ui.Column(c).Padding(48, 24).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "干掉百度，也干掉 Google 🤣").FontSize(15).TextColor(k.muted)
			ui.Text(c, "输入后回车搜索。支持 site:github.com、-site:、\"精确短语\"、lang:zh、after:2026-01-01、before:").
				FontSize(12).TextColor(k.faint).TextAlign(ui.Center).LineHeight(1.5)
			if a.cfg.NoHistory {
				ui.Text(c, "搜索历史已关闭（设置 › 通用）").FontSize(11).TextColor(k.faint)
			}
		})
		return
	}
	ui.Row(c.Key("hhead")).Padding(4, 12, 2, 12).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "最近搜索").FontSize(11).FontWeight(500).TextColor(k.faint).Grow(1)
		if a.confirmClear {
			ui.Text(c, "清空全部记录？").FontSize(11).TextColor(k.danger)
			ui.Box(c.Key("yes")).Padding(2, 6).Radius(5).Cursor(ui.CursorPointer).Label("确定清空").OnClick(a.clearHistory).Children(func() {
				ui.Text(c, "清空").FontSize(11).FontWeight(600).TextColor(k.danger)
			})
			ui.Box(c.Key("no")).Padding(2, 6).Radius(5).Cursor(ui.CursorPointer).Label("取消清空").OnClick(func() { a.confirmClear = false }).Children(func() {
				ui.Text(c, "取消").FontSize(11).TextColor(k.muted)
			})
			return
		}
		ui.Box(c.Key("clear")).Padding(2, 6).Radius(5).Cursor(ui.CursorPointer).Label("清空历史").OnClick(func() { a.confirmClear = true }).Children(func() {
			ui.Text(c, "清空").FontSize(11).TextColor(k.faint)
		})
	})
	for i, q := range a.cfg.History[:a.rows()] {
		bg := ui.Transparent
		if i == a.chosen {
			bg = k.chosen
		}
		row := ui.Row(c.Key("h"+q)).Gap(10).Padding(4, 6, 4, 12).MinHeight(34).Radius(8).Background(bg).AlignItems(ui.Center)
		hover := row.Hovered()
		row.Children(func() {
			ui.Row(c.Key("go")).Grow(1).Gap(10).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(q).
				OnClick(func() { a.query = q; a.search() }).Children(func() {
				ui.Icon(c, iconClock).FontSize(14).TextColor(k.faint)
				ui.Text(c, q).FontSize(14).SingleLine().Grow(1)
			})
			if hover || i == a.chosen {
				if i == a.chosen && !hover {
					ui.Text(c, "⌫ 删除").FontSize(11).TextColor(k.faint)
				}
				ui.Box(c.Key("x")).Padding(3, 7).Radius(6).Cursor(ui.CursorPointer).Label("删除 " + q).Tooltip("删除这条  ⌫").
					OnClick(func() { a.forget(i) }).Children(func() {
					ui.Text(c, "×").FontSize(15).TextColor(k.muted)
				})
			}
		})
	}
}

func notice(c *ui.Context, k ink, msg string, bad bool) {
	col := k.muted
	if bad {
		col = k.danger
	}
	ui.Row(c).Gap(8).Padding(24, 16).AlignItems(ui.Start).Children(func() {
		ui.Icon(c, iconAlert).FontSize(15).TextColor(col)
		ui.Text(c, msg).FontSize(13).TextColor(col).Grow(1).LineHeight(1.45)
	})
}

func (a *app) footer(c *ui.Context, k ink) {
	left := keys(a.cfg.Shortcut) + " 唤起"
	if len(a.results) > 0 && !a.showingHistory() {
		if a.cfg.AI {
			left = fmt.Sprintf("%d 条来源 · %.2f 秒", len(a.results), a.took.Seconds())
		} else {
			left = fmt.Sprintf("%d 条 · %.2f 秒", len(a.results), a.took.Seconds())
		}
	}
	if a.srcSet > 0 {
		left = fmt.Sprintf("%s · %d 条来源", a.setLabel(a.srcSet), len(a.results))
	}
	if a.toast != "" {
		left = a.toast + " · " + left
	}
	var right string
	esc := "隐藏"
	switch {
	case a.cfg.AI && a.chatFocused:
		right = hint("↵", "追问", "⇥", "结果", cmdKey()+"N", "新对话", "esc", esc)
	case a.listFocused && len(a.results) > 0 && a.cfg.AI:
		right = hint("↑↓", "选择", "↵", "打开", "空格", "预览", "⇥", "对话", "esc", esc)
	case a.listFocused && len(a.results) > 0:
		right = hint("↑↓", "选择", "↵", "打开", "空格", "预览", "⇥", "AI", "esc", "隐藏")
	case a.showingHistory() && len(a.cfg.History) > 0:
		right = hint("↑↓", "选择", "↵", "搜索", "esc", esc)
	case len(a.results) > 0:
		right = hint("↵", "搜索", "↓", "结果", "⇥", "AI", "esc", esc)
	default:
		right = hint("↵", "搜索", cmdKey()+"/", "快捷键", "esc", esc)
	}
	hintBar(c, k, left, right)
}

// endOfList is the list's last row: loading, all shown, or a failure.
func (a *app) endOfList(c *ui.Context, k ink) {
	row := ui.Row(c).Gap(8).Padding(12, 12).Radius(10).Justify(ui.Center).AlignItems(ui.Center)
	row.Children(func() {
		switch {
		case a.expanding:
			ui.Spinner(c).Size(12, 12)
			ui.Text(c, "加载中…").FontSize(12).TextColor(k.faint)
		case a.expandErr != "":
			if a.chosen == len(a.results) {
				row.Background(k.chosen)
			}
			row.Cursor(ui.CursorPointer).OnClick(a.retry)
			ui.Icon(c, iconAlert).FontSize(12).TextColor(k.danger)
			ui.Text(c, "加载失败，↵ 重试").FontSize(12).TextColor(k.danger)
			ui.Text(c, a.expandErr).FontSize(11).TextColor(k.faint).SingleLine().Shrink(1)
		default:
			ui.Text(c, fmt.Sprintf("已显示全部 %d 条", len(a.results))).FontSize(12).TextColor(k.faint)
		}
	})
}

func (a *app) reader(c *ui.Context, k ink) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(12, 14, 12, 10).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Row(c.Key("back")).Gap(2).Padding(4, 8, 4, 4).Radius(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).
				Label("返回").OnClick(a.back).Children(func() {
				ui.Icon(c, iconBack).FontSize(16).TextColor(k.muted)
				ui.Text(c, "返回").FontSize(13).TextColor(k.muted)
			})
			ui.Column(c).Grow(1).Shrink(1).Gap(1).Children(func() {
				ui.Text(c, oneLine(a.readerTitle)).FontSize(14).FontWeight(600).SingleLine()
				ui.Text(c, a.readerURL).FontSize(11).TextColor(k.faint).SingleLine()
			})
			ui.Button(c.Key("open"), "在浏览器打开").OnClick(func() { a.openURL(a.readerURL) })
		})
		ui.Divider(c)
		ui.Scroll(c.Key("reader")).TrackScroll(&a.readerScroll).Grow(1).Padding(18, 28, 28, 28).Children(func() {
			switch {
			case a.readerBusy:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, "正在抓取正文…").FontSize(13).TextColor(k.muted)
				})
			case a.readerErr != "":
				notice(c, k, a.readerErr, true)
			default:
				a.readerMD.Theme = &markdown.Theme{FontSize: 15, Link: k.link}
				markdown.View(c.Key("page"), a.readerText, &a.readerMD)
			}
		})
		ui.Divider(c)
		hintBar(c, k, "预览", hint("↑↓ 空格", "滚动", "↵", "原文", "esc", "返回"))
	})
}

func siteName(r dsapi.Result) string {
	if s := strings.TrimSpace(r.SiteName); s != "" {
		return s
	}
	return host(r.URL)
}

func host(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(p.Hostname(), "www.")
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

func stat(c *ui.Context, k ink, title string, s billing.Summary) {
	ui.Column(c).Grow(1).Gap(6).Padding(12, 14).Radius(12).BorderWidth(1).BorderColor(k.line).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(12).TextColor(k.muted).Grow(1)
			ui.Text(c, "估算").FontSize(10).TextColor(k.faint)
		})
		ui.Text(c, yuan(s.Cost)).FontSize(20).FontWeight(650)
		ui.Text(c, fmt.Sprintf("搜索 %d · 阅读 %d · AI 对话 %d", s.Search, s.Fetch, s.Chat)).FontSize(11).TextColor(k.muted).SingleLine()
		ui.Text(c, "AI 对话 "+yuan(s.ChatCost)).FontSize(11).TextColor(k.faint).SingleLine()
	})
}

// chart draws the last 30 days' estimated cost as bars.
func chart(c *ui.Context, k ink, days []billing.Day) {
	const h = 90
	top := 0.0
	for _, d := range days {
		top = max(top, d.Cost)
	}
	ui.Column(c).Gap(6).Children(func() {
		ui.Row(c).Children(func() {
			ui.Text(c, "近 30 天（估算）").FontSize(13).FontWeight(600).Grow(1)
			if top > 0 {
				ui.Text(c, "最高 "+yuan(top)+" / 天").FontSize(11).TextColor(k.faint)
			}
		})
		ui.Row(c).Height(h).Gap(4).AlignItems(ui.End).Padding(0, 2).BorderWidth(0, 0, 1, 0).BorderColor(k.line).Children(func() {
			for i, d := range days {
				bh := float32(2)
				if top > 0 && d.Cost > 0 {
					bh = max(3, float32(d.Cost/top)*h)
				}
				col := k.link.Alpha(0.75)
				if d.Cost == 0 {
					col = k.line
				}
				if i == len(days)-1 {
					col = k.link
				}
				ui.Box(c.Key(d.Date)).Grow(1).Height(bh).Radius(3, 3, 0, 0).Background(col).
					Tooltip(fmt.Sprintf("%s  %s  搜索 %d · 阅读 %d · AI %d", d.Date[5:], yuan(d.Cost), d.Search, d.Fetch, d.Chat))
			}
		})
		ui.Row(c).Children(func() {
			if len(days) > 0 {
				ui.Text(c, days[0].Date[5:]).FontSize(10).TextColor(k.faint).Grow(1)
			}
			ui.Text(c, "今天").FontSize(10).TextColor(k.faint)
		})
	})
}
