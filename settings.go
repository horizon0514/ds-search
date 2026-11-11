package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// settings is the settings page: tabs on the left (⌘1–⌘5), the tab's
// sections on the right.
func (a *app) settings(c *ui.Context, k ink) {
	for i, key := range digitKeys[:len(tabNames)] {
		if c.Shortcut(ui.Cmd, key) {
			a.openTab(i)
		}
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(12, 16, 12, 12).Gap(10).AlignItems(ui.Center).Children(func() {
			if a.configured() {
				ui.Row(c.Key("back")).Gap(2).Padding(4, 8, 4, 4).Radius(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).
					Label("返回").OnClick(a.back).Children(func() {
					ui.Icon(c, iconBack).FontSize(16).TextColor(k.muted)
					ui.Text(c, "返回").FontSize(13).TextColor(k.muted)
				})
			}
			ui.Text(c, "设置").FontSize(15).FontWeight(650).Grow(1)
			ui.Text(c, "DS 搜索 "+version).FontSize(11).TextColor(k.faint)
		})
		ui.Divider(c)
		ui.Row(c.Key("body")).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c.Key("tabs")).Width(148).Padding(10, 8).Gap(2).Background(k.card).Children(func() {
				for i, name := range tabNames {
					on := i == a.tab
					bg, fg := ui.Transparent, k.text
					if on {
						bg, fg = k.chosen, k.link
					}
					ui.Row(c.Key(name)).Padding(7, 10).Radius(7).Background(bg).AlignItems(ui.Center).Cursor(ui.CursorPointer).
						Focusable().Label(name).OnClick(func() { a.openTab(i) }).Children(func() {
						w := 450
						if on {
							w = 600
						}
						ui.Text(c, name).FontSize(13).FontWeight(w).TextColor(fg).Grow(1).SingleLine()
						ui.Text(c, cmdKey()+fmt.Sprint(i+1)).FontSize(10).TextColor(k.faint)
					})
				}
			})
			ui.Divider(c)
			ui.Scroll(c.Key(fmt.Sprint("tab", a.tab))).Grow(1).MinHeight(0).Padding(18, 26, 24, 26).Gap(22).Children(func() {
				switch a.tab {
				case tabGeneral:
					a.tabGeneral(c, k)
				case tabKey:
					a.tabKey(c, k)
				case tabUsage:
					a.tabUsage(c, k)
				case tabKeys:
					a.tabKeys(c, k)
				case tabAbout:
					a.tabAbout(c, k)
				}
			})
		})
		ui.Divider(c)
		left := ""
		if a.tab == tabUsage {
			left = "金额均为估算"
		}
		esc := "返回"
		if !a.configured() {
			esc = "隐藏"
		}
		hintBar(c, k, left, hint(cmdKey()+"1–5", "切换", "⇥", "下一项", "↵", "确定", "esc", esc))
	})
}

// section is a titled group of settings.
func section(c *ui.Context, k ink, title, desc string, body func()) {
	ui.Column(c.Key(title)).Gap(8).Children(func() {
		ui.Text(c, title).FontSize(13).FontWeight(600)
		if desc != "" {
			ui.Text(c, desc).FontSize(12).TextColor(k.muted).LineHeight(1.45)
		}
		body()
	})
}

// option is a row with a switch on its right. The whole row toggles: the
// label is the easy target, the switch is the precise one.
func option(c *ui.Context, k ink, label, desc string, on *bool, changed func()) {
	ui.Row(c.Key(label)).Gap(12).Padding(10, 14).Radius(10).BorderWidth(1).BorderColor(k.line).AlignItems(ui.Center).
		Cursor(ui.CursorPointer).OnClick(func() {
		*on = !*on
		changed()
	}).Children(func() {
		ui.Column(c).Grow(1).Gap(3).Children(func() {
			ui.Text(c, label).FontSize(13)
			if desc != "" {
				ui.Text(c, desc).FontSize(11).TextColor(k.muted).LineHeight(1.4)
			}
		})
		if ui.Switch(c.Key("sw"), on).Label(label).Changed() {
			changed()
		}
	})
}

func (a *app) message(c *ui.Context, k ink) {
	if a.settingsMsg == "" {
		return
	}
	col := k.green
	if a.settingsErr {
		col = k.danger
	}
	ui.Row(c.Key("msg")).Gap(8).AlignItems(ui.Center).Children(func() {
		if a.checking {
			ui.Spinner(c).Size(12, 12)
		}
		ui.Text(c, a.settingsMsg).FontSize(12).TextColor(col).Grow(1).LineHeight(1.4)
	})
}

func (a *app) tabGeneral(c *ui.Context, k ink) {
	section(c, k, "AI 总结", "默认每次搜索都先搜再总结：面板上只有带 [n] 引用的总结，来源收在右侧抽屉里，按 ⇥ 或点「来源」展开。", func() {
		on := a.cfg.AI
		option(c, k, "搜索后自动 AI 总结", "关闭后只显示搜索结果，按 ⇥ 才展开对话（总结也会在那时才开始，不会白花钱）。", &on, func() {
			a.summarize(on) // it saves the config, resizes the panel, and summarizes what is on screen
		})
	})
	section(c, k, "外观", "", func() {
		translucent := a.cfg.Translucent
		option(c, k, "半透明背景", "透出桌面的毛玻璃效果。默认关闭：不透明背景在任何壁纸和网页上都更易读。", &translucent, func() {
			a.cfg.Translucent = translucent
			a.changed(a.cfg)
			a.setTranslucent(translucent)
		})
	})
	section(c, k, "搜索历史", "只保存在本机。首页的最近搜索可以逐条删除（选中后按 ⌫，或点 ×）。", func() {
		on := !a.cfg.NoHistory
		option(c, k, "记录搜索历史", "关闭后会清空已有记录，也不再记录。", &on, func() { a.setHistory(on) })
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("已有 %d 条", len(a.cfg.History))).FontSize(12).TextColor(k.muted).Grow(1)
			a.clearButton(c.Key("clear"), k)
		})
	})
	section(c, k, "唤起快捷键", "全局快捷键，任何时候都能唤起面板。被 Raycast/Alfred 占用时换一个，例如 Alt+Shift+Space、Cmd+Shift+K。", func() {
		a.shortcutField(c, k)
	})
	a.message(c, k)
}

// clearButton clears the history after a second click.
func (a *app) clearButton(c *ui.Context, k ink) {
	if len(a.cfg.History) == 0 {
		return
	}
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		if a.confirmClear {
			ui.Text(c, "确定清空全部？").FontSize(12).TextColor(k.danger)
			ui.Button(c.Key("yes"), "清空").OnClick(a.clearHistory)
			ui.Button(c.Key("no"), "取消").OnClick(func() { a.confirmClear = false })
			return
		}
		ui.Button(c.Key("ask"), "清空…").OnClick(func() { a.confirmClear = true })
	})
}

func (a *app) shortcutField(c *ui.Context, k ink) {
	ui.Row(c.Key("short")).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.TextInput(c.Key("shortcut"), &a.editShortcut).Label("快捷键").Placeholder(defaultShortcut).Grow(1).
			OnSubmit(a.applyShortcut)
		ui.Button(c.Key("applyshort"), "应用").Disabled(strings.TrimSpace(a.editShortcut) == a.cfg.Shortcut).OnClick(a.applyShortcut)
	})
	ui.Text(c, "当前："+keys(a.cfg.Shortcut)).FontSize(11).TextColor(k.muted)
}

func (a *app) tabKey(c *ui.Context, k ink) {
	section(c, k, "网页搜索源", "DeepSeek 仍是总结与阅读所用的模型；这里只决定网页搜索由谁来做。", func() {
		sel := 0
		if a.cfg.Source == "tavily" {
			sel = 1
		}
		if ui.Segmented(c.Key("source"), &sel, "DeepSeek（默认）", "Tavily").Label("搜索源").Changed() {
			source := ""
			if sel == 1 {
				source = "tavily"
			}
			a.setSource(source)
		}
		desc := "搜索走 DeepSeek Search API，和总结共用这一个 Key，约 ¥5 / 1000 次。"
		if a.cfg.Source == "tavily" {
			desc = "搜索走 Tavily，单独计费（约 $8 / 1000 次，按 7.1 汇率约 ¥0.057 / 次）；总结与阅读仍走 DeepSeek。"
			if a.tvKey == "" {
				desc += " 还没有 Tavily Key，搜索会提示先填写。"
			}
		}
		ui.Text(c, desc).FontSize(11).TextColor(k.muted).LineHeight(1.45)
		if a.cfg.Source == "tavily" {
			a.tavilyKeyBlock(c, k)
		}
	})
	section(c, k, "DeepSeek API Key", "到 platform.deepseek.com 的「API keys」创建，粘贴到这里。保存前会用一次极短的对话验证。", func() {
		if a.keyErr != "" && a.keyFrom != "keychain" {
			ui.Column(c.Key("kcerr")).Gap(8).Padding(10, 12).Radius(10).Background(k.danger.Alpha(0.08)).Children(func() {
				ui.Text(c, a.keyErr+"。通常是 App 更新后 macOS 需要重新授权：点「重试读取」，在弹窗中选「始终允许」；或者改用本地文件保存。").
					FontSize(12).TextColor(k.danger).LineHeight(1.45)
				ui.Row(c).Gap(8).Children(func() {
					ui.Button(c.Key("retry"), "重试读取").OnClick(a.reloadKey)
					if a.cfg.KeyStore != "file" {
						ui.Button(c.Key("tofile"), "改用本地文件").OnClick(func() { a.setKeyStore("file") })
					}
				})
			})
		}
		if a.key != "" && !a.changingKey {
			ui.Row(c.Key("saved")).Gap(10).Padding(12, 14).Radius(10).BorderWidth(1).BorderColor(k.line).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(3).Children(func() {
					ui.Text(c, "已保存  "+maskKey(a.key)).FontSize(14).FontWeight(550)
					ui.Text(c, "存于："+storeName(a.keyFrom)).FontSize(11).TextColor(k.muted)
				})
				ui.Button(c.Key("change"), "更换").OnClick(func() { a.changingKey, a.editKey = true, "" })
				if a.keyFrom != "env" {
					ui.Button(c.Key("delete"), "删除").OnClick(a.deleteKey)
				}
			})
		} else {
			ui.Row(c.Key("edit")).Gap(8).AlignItems(ui.Center).Children(func() {
				in := ui.TextInput(c.Key("key"), &a.editKey).Label("API Key").Placeholder("sk-…").Password().Grow(1).
					Disabled(a.checking).OnSubmit(a.saveKey)
				if !a.configured() || a.changingKey {
					in.AutoFocus()
				}
				ui.PrimaryButton(c.Key("savekey"), "验证并保存").Disabled(a.checking || strings.TrimSpace(a.editKey) == "").OnClick(a.saveKey)
				if a.changingKey {
					ui.Button(c.Key("cancel"), "取消").OnClick(func() { a.changingKey, a.editKey = false, "" })
				}
			})
		}
	})
	section(c, k, "保存位置", "", func() {
		sel := 0
		if a.cfg.KeyStore == "file" {
			sel = 1
		}
		if ui.Segmented(c.Key("store"), &sel, "macOS 钥匙串（推荐）", "本地文件").Label("保存位置").Changed() {
			mode := ""
			if sel == 1 {
				mode = "file"
			}
			a.setKeyStore(mode)
		}
		desc := "钥匙串由系统加密保存，两个 Key 用同一种方式。本 App 未经 Apple 签名，更新后 macOS 可能要求重新授权（选「始终允许」）。"
		if a.cfg.KeyStore == "file" {
			desc = "备用方式：两个 Key 分别保存在 " + a.keys.pathOf(keyFile) + " 与 " + a.keys.pathOf(tavilyKeyFile) + "（权限 0600，只有你的账户能读）。不经钥匙串，App 更新后也无需授权，但没有系统加密。"
		}
		ui.Text(c, desc).FontSize(11).TextColor(k.muted).LineHeight(1.45)
	})
	a.message(c, k)
}

// tavilyKeyBlock is the Tavily key's saved state, or its input.
func (a *app) tavilyKeyBlock(c *ui.Context, k ink) {
	ui.Column(c.Key("tavily")).Gap(8).Padding(10, 12).Radius(10).BorderWidth(1).BorderColor(k.line).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Tavily API Key").FontSize(12).FontWeight(600).Grow(1)
			ui.Link(c.Key("tvlink"), "创建 Key ›", "https://app.tavily.com/home").FontSize(11)
		})
		if a.tvErr != "" && a.tvFrom != "keychain" {
			ui.Text(c, a.tvErr+"。可点「重试读取」，或在弹窗中选「始终允许」。").FontSize(11).TextColor(k.danger).LineHeight(1.4)
			ui.Row(c).Gap(8).Children(func() {
				ui.Button(c.Key("tvretry"), "重试读取").OnClick(a.reloadTavilyKey)
				if a.cfg.KeyStore != "file" {
					ui.Button(c.Key("tvfile"), "改用本地文件").OnClick(func() { a.setKeyStore("file") })
				}
			})
		}
		if a.tvKey != "" && !a.tvChanging {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, "已保存  "+maskKey(a.tvKey)).FontSize(13).FontWeight(550)
					ui.Text(c, "存于："+storeNameFor(a.tvFrom, "TAVILY_API_KEY")).FontSize(11).TextColor(k.muted)
				})
				ui.Button(c.Key("tvchange"), "更换").OnClick(func() { a.tvChanging, a.tvEdit = true, "" })
				if a.tvFrom != "env" {
					ui.Button(c.Key("tvdelete"), "删除").OnClick(a.deleteTavilyKey)
				}
			})
		} else {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c.Key("tvkey"), &a.tvEdit).Label("Tavily API Key").Placeholder("tvly-…").Password().Grow(1).
					Disabled(a.checking).OnSubmit(a.saveTavilyKey)
				ui.PrimaryButton(c.Key("tvsave"), "验证并保存").Disabled(a.checking || strings.TrimSpace(a.tvEdit) == "").OnClick(a.saveTavilyKey)
				if a.tvChanging {
					ui.Button(c.Key("tvcancel"), "取消").OnClick(func() { a.tvChanging, a.tvEdit = false, "" })
				}
			})
			ui.Text(c, "验证会发起 1 次最小搜索，计 1 次 Tavily 费用。").FontSize(10).TextColor(k.faint)
		}
	})
}

func (a *app) tabUsage(c *ui.Context, k ink) {
	r := a.ledger.Report()
	ui.Row(c.Key("bal")).Gap(12).AlignItems(ui.Center).Padding(12, 16).Radius(12).Background(k.card).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "账户余额").FontSize(12).TextColor(k.muted)
			switch v, sym, ok := a.cny(); {
			case ok:
				ui.Text(c, fmt.Sprintf("%s%.2f", sym, v)).FontSize(24).FontWeight(650)
			case a.balErr != "":
				ui.Text(c, a.balErr).FontSize(12).TextColor(k.danger)
			case !a.configured():
				ui.Text(c, "设置 API Key 后显示").FontSize(13).TextColor(k.muted)
			default:
				ui.Text(c, "读取中…").FontSize(14).TextColor(k.muted)
			}
		})
		if a.balBusy {
			ui.Spinner(c).Size(14, 14)
		}
		ui.Button(c.Key("refresh"), "刷新").Disabled(a.balBusy || !a.configured()).OnClick(func() { a.refreshBalance(true) })
		ui.Link(c.Key("official"), "官方账单 ›", "https://platform.deepseek.com/usage").FontSize(12)
	})
	ui.Row(c.Key("stats")).Gap(10).Children(func() {
		stat(c.Key("today"), k, "今日", r.Today)
		stat(c.Key("month"), k, "本月", r.Month)
		stat(c.Key("total"), k, "累计", r.Total)
	})
	chart(c.Key("chart"), k, r.Last30)
	section(c, k, "提醒", "余额低于提醒线，或本月估算超过预算时，面板顶部会出现提示。", func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "余额低于 ¥").FontSize(12).TextColor(k.muted)
			ui.TextInput(c.Key("low"), &a.editLow).Label("余额提醒线").Placeholder("5").Width(72).OnSubmit(a.saveAlerts)
			ui.Text(c, "本月超过 ¥").FontSize(12).TextColor(k.muted)
			ui.TextInput(c.Key("budget"), &a.editBudget).Label("月度预算").Placeholder("不设").Width(72).OnSubmit(a.saveAlerts)
			ui.Spacer(c)
			ui.Button(c.Key("savealerts"), "保存").OnClick(a.saveAlerts)
		})
		if a.usageMsg != "" {
			ui.Text(c, a.usageMsg).FontSize(11).TextColor(k.muted)
		}
	})
	ui.Text(c, "本机按官方价格估算：DeepSeek 搜索、阅读每次成功请求 ¥0.005（含 AI 追问时模型发起的搜索）；Tavily 搜索按 $8 / 1000 次、约 7.1 汇率折算。AI 对话的每次调用（含工具调用轮次）按 deepseek-flash 实际返回的 token 数计价，区分缓存命中/未命中与北京时间高峰/空闲时段。以 DeepSeek 与 Tavily 的官方账单为准。").
		FontSize(11).TextColor(k.muted).LineHeight(1.5)
}

func (a *app) tabKeys(c *ui.Context, k ink) {
	section(c, k, "唤起快捷键", "", func() { a.shortcutField(c, k) })
	section(c, k, "面板里的快捷键", "全键盘操作；任何时候按 "+cmdKey()+"/ 也能看这张表。", func() {
		ui.Column(c).Gap(1).Children(func() {
			for i, g := range shortcutGroups() {
				if g[0] != "" {
					ui.Text(c, g[0]).FontSize(11).FontWeight(600).TextColor(k.muted).Margin(8, 0, 2, 0)
				}
				ui.Row(c.Key(i)).Gap(14).Padding(3, 0).AlignItems(ui.Center).Children(func() {
					ui.Text(c, g[1]).FontSize(12).FontWeight(600).TextColor(k.link).Width(160).SingleLine()
					ui.Text(c, g[2]).FontSize(12).Grow(1).SingleLine()
				})
			}
		})
	})
	a.message(c, k)
}

func (a *app) tabAbout(c *ui.Context, k ink) {
	section(c, k, "DS 搜索 "+version, "用 DeepSeek 搜索 API 做的极简搜索：菜单栏常驻，"+keys(a.cfg.Shortcut)+" 唤起，可展开 AI 对话、联网追问。", func() {
		ui.Row(c).Gap(14).Children(func() {
			ui.Link(c.Key("platform"), "DeepSeek 开放平台 ›", "https://platform.deepseek.com").FontSize(12)
			ui.Link(c.Key("docs"), "搜索 API 文档 ›", "https://api-docs.deepseek.com/zh-cn/guides/search").FontSize(12)
		})
	})
	section(c, k, "数据保存在哪", "", func() {
		rows := [][2]string{
			{"设置与搜索历史", a.dataDir + "/config.json"},
			{"用量账本", a.dataDir + "/usage.json"},
			{"DeepSeek API Key", storeName(a.keyFrom)},
			{"Tavily API Key", storeNameFor(a.tvFrom, "TAVILY_API_KEY")},
		}
		for _, r := range rows {
			ui.Row(c.Key(r[0])).Gap(12).Children(func() {
				ui.Text(c, r[0]).FontSize(12).TextColor(k.muted).Width(110)
				ui.Text(c, r[1]).FontSize(12).Grow(1).Selectable()
			})
		}
		ui.Text(c, "AI 对话只在内存里，不落盘。").FontSize(11).TextColor(k.muted)
	})
	ui.Row(c.Key("quitrow")).Children(func() {
		ui.Button(c.Key("quit"), "退出 DS 搜索  "+cmdKey()+"Q").OnClick(a.quit)
	})
}
