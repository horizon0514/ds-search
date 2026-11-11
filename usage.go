package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ds-search/billing"
	"ds-search/dsapi"
)

// bill records a successful Search or Fetch.
func (a *app) bill(k billing.Kind) {
	a.ledger.AddRequest(k)
	a.billed()
}

// billSearch records a successful web search, with the provider that served it.
func (a *app) billSearch() { a.billSearchAs(a.seek.Name()) }

func (a *app) billSearchAs(provider string) {
	if provider == "tavily" {
		a.bill(billing.TavilySearch)
		return
	}
	a.bill(billing.Search)
}

func (a *app) billChat(model string, u dsapi.Usage) {
	a.ledger.AddChat(model, billing.Usage{PromptCacheHit: u.PromptCacheHit, PromptCacheMiss: u.PromptCacheMiss, Completion: u.Completion})
	a.billed()
}

func (a *app) billed() {
	a.sinceBalance++
	if a.sinceBalance >= 20 {
		a.refreshBalance(true)
	}
	a.onUsage()
}

// refreshBalance reads the balance, unless it was read within a minute.
func (a *app) refreshBalance(force bool) {
	if a.brain == nil || a.balBusy || (!force && time.Since(a.balAt) < time.Minute) {
		return
	}
	a.balBusy, a.sinceBalance = true, 0
	brain := a.brain
	a.spawn(func() {
		b, err := brain.GetBalance(context.Background())
		a.post(func() {
			a.balBusy, a.balAt = false, time.Now()
			if err != nil {
				a.balErr = errText(err)
				return
			}
			a.balance, a.balErr = &b, ""
			a.onUsage()
		})
	})
}

// cny returns the balance in yuan (or the first currency), and its symbol.
func (a *app) cny() (float64, string, bool) {
	if a.balance == nil || len(a.balance.Infos) == 0 {
		return 0, "", false
	}
	info := a.balance.Infos[0]
	for _, i := range a.balance.Infos {
		if i.Currency == "CNY" {
			info = i
		}
	}
	v, err := strconv.ParseFloat(info.Total, 64)
	if err != nil {
		return 0, "", false
	}
	sym := "¥"
	if info.Currency == "USD" {
		sym = "$"
	}
	return v, sym, true
}

// warning is the banner the panel shows: a low balance, or a month over
// its budget; "" for none.
func (a *app) warning() string {
	if v, sym, ok := a.cny(); ok && sym == "¥" && a.cfg.LowBalance > 0 && v < a.cfg.LowBalance {
		return fmt.Sprintf("余额 ¥%.2f，低于提醒线 ¥%s，记得去充值", v, num(a.cfg.LowBalance))
	}
	if a.balance != nil && !a.balance.Available {
		return "DeepSeek 账号已无可用余额，请充值"
	}
	if a.cfg.MonthlyBudget > 0 {
		if m := a.ledger.Report().Month.Cost; m >= a.cfg.MonthlyBudget {
			return fmt.Sprintf("本月估算花费 %s，已超出预算 ¥%s", yuan(m), num(a.cfg.MonthlyBudget))
		}
	}
	return ""
}

// trayLine is the tray menu's line: 今日 ¥0.12 · 余额 ¥98.30.
func (a *app) trayLine() string {
	s := "今日 " + yuan(a.ledger.Report().Today.Cost) + "（估算）"
	if v, sym, ok := a.cny(); ok {
		s += fmt.Sprintf(" · 余额 %s%.2f", sym, v)
	}
	return s
}

// openUsage opens 用量与费用, a tab of the settings (⌘U, the tray).
func (a *app) openUsage() { a.openTab(tabUsage) }

func (a *app) saveAlerts() {
	low, err1 := parseYuan(a.editLow)
	budget, err2 := parseYuan(a.editBudget)
	if err1 != nil || err2 != nil {
		a.usageMsg = "请输入金额，例如 5 或 12.5（预算留空表示不设）"
		return
	}
	a.cfg.LowBalance, a.cfg.MonthlyBudget = low, budget
	a.changed(a.cfg)
	a.usageMsg = "已保存"
	a.onUsage()
}

func parseYuan(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "¥"))
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("bad amount")
	}
	return v, nil
}

// yuan formats an estimate: ¥0.12, or ¥0.0046 under a cent.
func yuan(v float64) string {
	switch {
	case v == 0:
		return "¥0"
	case v < 0.01:
		return fmt.Sprintf("¥%.4f", v)
	case v < 100:
		return fmt.Sprintf("¥%.2f", v)
	}
	return fmt.Sprintf("¥%.1f", v)
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
