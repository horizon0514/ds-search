package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// chatPane is the AI conversation, on the right of the expanded panel:
// the turns, and the follow-up box under them.
func (a *app) chatPane(c *ui.Context, k ink) {
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		ui.Row(c).Padding(10, 16, 10, 18).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconSparkles).FontSize(14).TextColor(k.link)
			ui.Text(c, "AI 对话").FontSize(13).FontWeight(600).TextColor(k.link)
			if a.chatBusy() {
				ui.Spinner(c).Size(11, 11)
			}
			ui.Spacer(c)
			ui.Text(c, a.modelName()+" · 可联网追问").FontSize(11).TextColor(k.faint)
			ui.Row(c.Key("new")).Padding(3, 8).Radius(6).Cursor(ui.CursorPointer).Label("新对话").
				Tooltip("清空对话，保留搜索结果  " + cmdKey() + "N").OnClick(a.newChat).Children(func() {
				ui.Text(c, "新对话 "+cmdKey()+"N").FontSize(11).TextColor(k.muted)
			})
		})
		ui.Divider(c)
		if len(a.turns) == 0 {
			ui.Column(c.Key("empty")).Grow(1).Padding(40, 30).AlignItems(ui.Center).Gap(8).Children(func() {
				msg := "搜索后，这里会先给出 AI 总结。"
				if len(a.page1) > 0 {
					msg = "就这次搜索的结果提问；需要时 AI 会自己再搜。"
				}
				ui.Text(c, msg).FontSize(13).TextColor(k.faint).TextAlign(ui.Center)
			})
		} else {
			ui.List(c.Key("turns"), &a.chatList, len(a.turns), func(i int) {
				a.turnView(c.Key(i), k, i)
			}).Grow(1).MinHeight(0).Padding(12, 18, 12, 18).Gap(18)
		}
		ui.Divider(c)
		ui.Row(c.Key("composer")).Padding(10, 14, 10, 16).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.TextAreaBase(c.Key("draft"), &a.draft).Bind(&a.chatInput).Grow(1).FontSize(14).MaxHeight(96).
				Placeholder("追问…（↵ 发送，⇧↵ 换行）").Label("追问").OnTextKey(a.draftKey)
			if a.focusChat {
				a.focusChat = false
				a.chatInput.Focus()
			}
		})
	})
	a.chatFocused = a.chatInput.Focused(c)
}

// draftKey sends the follow-up with ↵; ⇧↵ breaks the line, and ↑ in an
// empty box brings the last question back.
func (a *app) draftKey(k ui.TextKey) bool {
	switch {
	case k.Key == ui.KeyEnter && k.Mods == 0:
		if a.ask(k.Text()) {
			k.SetText("")
		}
		return true
	case k.Key == ui.KeyUp && k.Mods == 0 && k.Text() == "" && a.lastQuestion != "":
		k.SetText(a.lastQuestion)
		return true
	}
	return false
}

func (a *app) modelName() string {
	if a.src == nil {
		return "deepseek"
	}
	return a.src.ModelName()
}

// turnView is a turn: the question, the searches it made, the answer.
func (a *app) turnView(c *ui.Context, k ink, i int) {
	t := a.turns[i]
	summary := t.question == "" // the first turn: the search's summary, the panel's headline
	ui.Column(c).Gap(8).Children(func() {
		if t.question != "" {
			ui.Row(c.Key("q")).Justify(ui.End).Children(func() {
				ui.Text(c, t.question).FontSize(14).Padding(7, 12).Radius(12).Background(k.chosen).MaxWidth(520)
			})
		} else {
			ui.Row(c.Key("q")).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconSparkles).FontSize(13).TextColor(k.link)
				ui.Text(c, "AI 总结").FontSize(12).FontWeight(650).TextColor(k.link)
				if n := len(t.sources); n > 0 {
					ui.Text(c, fmt.Sprintf("· 基于 %d 条最相关结果", n)).FontSize(11).TextColor(k.faint)
				}
			})
		}
		for j, s := range t.searches {
			text := "又搜了：" + s.query
			col := k.muted
			switch {
			case s.busy:
				text = "正在搜索：" + s.query + "…"
			case s.err != "":
				text, col = "搜索失败："+s.query+"（"+s.err+"）", k.danger
			default:
				text += fmt.Sprintf(" · %d 条", s.n)
			}
			ui.Row(c.Key(fmt.Sprint("s", j))).Gap(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).
				Tooltip("在左侧查看这一轮的来源").OnClick(func() { a.showSet(t.set) }).Children(func() {
				if s.busy {
					ui.Spinner(c).Size(11, 11)
				} else {
					ui.Icon(c, iconSearch).FontSize(12).TextColor(col)
				}
				ui.Text(c, text).FontSize(12).TextColor(col).SingleLine().Shrink(1)
			})
		}
		switch {
		case t.err != "":
			ui.Text(c.Key("err"), t.err).FontSize(13).TextColor(k.danger)
		case t.answer == "" && t.busy && !searching(t):
			ui.Row(c.Key("wait")).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(11, 11)
				ui.Text(c, "思考中…").FontSize(12).TextColor(k.faint)
			})
		}
		if t.answer != "" {
			src := citeLinks(t.answer, len(t.sources))
			size := float32(14)
			if summary { // what the search is for: read first, and largest
				size = 15.5
			}
			t.md.Theme = &markdown.Theme{FontSize: size, LineHeight: 1.6, Link: k.link}
			if t.busy {
				markdown.Stream(c.Key("md"), src, &t.md)
			} else {
				markdown.View(c.Key("md"), src, &t.md)
			}
		}
	})
}

func searching(t *turn) bool {
	for _, s := range t.searches {
		if s.busy {
			return true
		}
	}
	return false
}

// sourcesHead names the left pane what it is beside the summary: the
// sources. Once a turn has searched, its chips switch the list between
// the search's sources and each turn's (⌥← ⌥→).
func (a *app) setChips(c *ui.Context, k ink) {
	if a.showingHistory() || a.searchErr != "" {
		return
	}
	ss := a.sets()
	if len(ss) < 2 {
		if len(a.results) == 0 && !a.searching {
			return
		}
		head := fmt.Sprintf("来源 · %d 条", len(a.results))
		if a.searching && len(a.all) == 0 {
			head = "正在搜索…"
		}
		ui.Row(c.Key("srcs")).Padding(9, 12, 3, 14).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, head).FontSize(11).FontWeight(600).TextColor(k.faint).Grow(1).SingleLine()
			if a.searching || a.expanding {
				ui.Spinner(c).Size(10, 10)
			}
		})
		return
	}
	ui.Row(c).Padding(8, 12, 2, 14).Gap(6).Wrap().Children(func() {
		for _, s := range ss {
			bg, fg := ui.Transparent, k.muted
			if s == a.srcSet {
				bg, fg = k.chosen, k.link
			}
			ui.Row(c.Key(s)).Padding(3, 10).Radius(20).Background(bg).Border(1, k.line).Cursor(ui.CursorPointer).
				Label(a.setLabel(s)).Tooltip("⌥← ⌥→ 切换").OnClick(func() { a.showSet(s) }).Children(func() {
				ui.Text(c, a.setLabel(s)).FontSize(11).FontWeight(550).TextColor(fg).SingleLine()
			})
		}
	})
}
