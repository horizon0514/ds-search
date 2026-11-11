package main

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// memSecrets is a keychain in memory; fail, when set, is every call's error.
type memSecrets struct {
	m    map[string]string
	fail error
	sets int
}

func (s *memSecrets) Get(svc, acct string) ([]byte, error) {
	if s.fail != nil {
		return nil, s.fail
	}
	v, ok := s.m[svc+"|"+acct]
	if !ok {
		return nil, errNotFound
	}
	return []byte(v), nil
}

func (s *memSecrets) Set(svc, acct string, v []byte) error {
	if s.fail != nil {
		return s.fail
	}
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.sets++
	s.m[svc+"|"+acct] = string(v)
	return nil
}

func (s *memSecrets) Delete(svc, acct string) error {
	if s.fail != nil {
		return s.fail
	}
	if _, ok := s.m[svc+"|"+acct]; !ok {
		return errNotFound
	}
	delete(s.m, svc+"|"+acct)
	return nil
}

const testKey = "sk-fake-test-key-not-real-1234"

func noEnv(string) string { return "" }

func TestKeyStoreKeychain(t *testing.T) {
	sec := &memSecrets{}
	ks := keyStore{secrets: sec, dir: t.TempDir(), env: noEnv}
	if st := ks.load(""); st.key != "" || st.err != "" {
		t.Fatalf("empty: %+v", st)
	}
	from, warn, err := ks.save(testKey, "")
	if from != "keychain" || warn != "" || err != nil {
		t.Fatalf("save: %q %q %v", from, warn, err)
	}
	if _, err := os.Stat(ks.path()); !errors.Is(err, os.ErrNotExist) {
		t.Error("the keychain's key also in the file")
	}
	// The next launch finds it.
	if st := (keyStore{secrets: sec, dir: ks.dir, env: noEnv}).load(""); st.key != testKey || st.from != "keychain" {
		t.Errorf("reload: from %q, key found %v", st.from, st.key == testKey)
	}
	if err := ks.remove(); err != nil {
		t.Fatal(err)
	}
	if st := ks.load(""); st.key != "" {
		t.Error("removed, still found")
	}
}

// The bug of 0.2.0: a keychain that can't be read was taken for no key.
func TestKeyStoreKeychainFails(t *testing.T) {
	sec := &memSecrets{fail: errors.New("mygo: secrets: SecItemCopyMatching: OSStatus -25293")}
	ks := keyStore{secrets: sec, dir: t.TempDir(), env: noEnv}
	st := ks.load("")
	if st.err != "钥匙串不可用（授权失败）" {
		t.Errorf("err %q", st.err)
	}
	from, warn, err := ks.save(testKey, "")
	if from != "file" || warn == "" || err != nil {
		t.Fatalf("fallback: %q %q %v", from, warn, err)
	}
	fi, err := os.Stat(ks.path())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", fi, err)
	}
	if filepath.Base(ks.dir) == "" || filepath.Dir(ks.path()) != ks.dir {
		t.Error("file outside the data folder")
	}
	st = ks.load("")
	if st.key != testKey || st.from != "file" || st.err == "" {
		t.Errorf("reload: from %q err %q found %v", st.from, st.err, st.key == testKey)
	}
	// In mode file, the keychain isn't asked at all.
	st = ks.load("file")
	if st.key != testKey || st.err != "" {
		t.Errorf("file mode: %+v", st.err)
	}
}

func TestKeyStoreMigrates(t *testing.T) {
	for _, l := range legacy {
		sec := &memSecrets{m: map[string]string{l.service + "|" + l.account: testKey + "\n"}}
		ks := keyStore{secrets: sec, dir: t.TempDir(), env: noEnv}
		st := ks.load("")
		if st.key != testKey || st.from != "keychain" {
			t.Errorf("%v: not found", l)
		}
		if _, ok := sec.m["|"+secretKey]; !ok || len(sec.m) != 1 {
			t.Errorf("%v: not moved: %d items", l, len(sec.m))
		}
	}
}

func TestKeyStoreFileMode(t *testing.T) {
	sec := &memSecrets{}
	ks := keyStore{secrets: sec, dir: filepath.Join(t.TempDir(), "DS Search"), env: noEnv}
	ks.save(testKey, "")
	from, _, err := ks.save(testKey, "file")
	if from != "file" || err != nil || len(sec.m) != 0 {
		t.Fatalf("to file: %q %v, %d in keychain", from, err, len(sec.m))
	}
	if st := ks.load("file"); st.key != testKey || st.from != "file" {
		t.Error("file mode: not found")
	}
	env := keyStore{secrets: sec, dir: t.TempDir(), env: func(string) string { return testKey }}
	if st := env.load(""); st.from != "env" {
		t.Errorf("env: %q", st.from)
	}
}

func TestMaskKey(t *testing.T) {
	if m := maskKey(testKey); m != "sk-••••1234" {
		t.Error(m)
	}
	if m := maskKey("short"); strings.Contains(m, "short") {
		t.Error(m)
	}
}

// Settings shows a saved key masked, with 更换 and 删除; never the key.
func TestSettingsSavedKey(t *testing.T) {
	a, _ := testApp(&fake{results: canned})
	sec := &memSecrets{}
	a.keys = keyStore{secrets: sec, dir: t.TempDir(), env: noEnv}
	a.keys.save(testKey, "")
	a.key, a.keyFrom = testKey, "keychain"
	a.openTab(tabKey)
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	if !tt.HasText("已保存  sk-••••1234") || tt.HasText(testKey) {
		t.Fatal("not masked")
	}
	if err := tt.Click("更换"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("验证并保存") {
		t.Error("更换: no input")
	}
	tt.Click("取消")
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	if a.key != "" || len(sec.m) != 0 {
		t.Error("删除: key kept")
	}
	// A keychain that fails is said, with what to do.
	a.keys.secrets = &memSecrets{fail: errors.New("OSStatus -128")}
	a.reloadKey()
	tt.Frame()
	if !tt.HasText("重试读取") || !tt.HasText("改用本地文件") {
		t.Error("no way out of a failing keychain")
	}
}

func TestHistoryDelete(t *testing.T) {
	a, _ := testApp(&fake{results: canned})
	saved := 0
	a.changed = func(config) { saved++ }
	a.cfg.History = []string{"一", "二", "三", "四"}
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	for i := 0; i < 3 && a.chosen != 1; i++ {
		tt.Key(0, ui.KeyDown) // 二
	}
	tt.Key(0, ui.KeyBackspace)
	if strings.Join(a.cfg.History, "") != "一三四" || saved != 1 {
		t.Fatalf("⌫: %v", a.cfg.History)
	}
	tt.Key(ui.Cmd, ui.KeyBackspace) // 三, chosen now
	if strings.Join(a.cfg.History, "") != "一四" {
		t.Fatalf("⌘⌫: %v", a.cfg.History)
	}
	// ⌫ in a typed query edits it.
	tt.Type("ab")
	tt.Key(0, ui.KeyBackspace)
	if a.query != "a" || len(a.cfg.History) != 2 {
		t.Fatalf("typed: %q %v", a.query, a.cfg.History)
	}
	tt.Key(0, ui.KeyBackspace)
	// The × of a row.
	if err := tt.Click("删除 一"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(a.cfg.History, "") != "四" {
		t.Fatalf("×: %v", a.cfg.History)
	}
	// 清空 asks first.
	a.cfg.History = []string{"一", "二"}
	tt.Frame()
	tt.Click("清空历史")
	if len(a.cfg.History) != 2 || !tt.HasText("清空全部记录？") {
		t.Fatal("清空 without asking")
	}
	tt.Click("取消清空")
	if len(a.cfg.History) != 2 {
		t.Fatal("取消 cleared")
	}
	tt.Click("清空历史")
	tt.Click("确定清空")
	if len(a.cfg.History) != 0 {
		t.Fatal("not cleared")
	}
}

func TestHistoryOff(t *testing.T) {
	a, _ := testApp(&fake{results: canned})
	a.cfg.AI = false
	a.cfg.History = []string{"一"}
	a.setHistory(false)
	if len(a.cfg.History) != 0 || !a.cfg.NoHistory {
		t.Fatal("off kept the history")
	}
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	tt.Type("go")
	tt.Key(0, ui.KeyEnter)
	if len(a.cfg.History) != 0 {
		t.Error("off, yet remembered")
	}
	if !tt.HasText("go") {
		t.Error("no search")
	}
	a.setHistory(true)
	a.query = "rust"
	a.search()
	if len(a.cfg.History) != 1 {
		t.Error("on, not remembered")
	}
}

// The switch in the settings is the other way round for those who want
// the search alone: off, nothing is asked; on, what is on screen is
// summarized at once. The whole row is the target, so the label is
// enough; space on the focused switch does the same (TestSettingsKeyboard).
func TestSummarySwitch(t *testing.T) {
	f := &fake{results: canned, answer: []string{"总结"}}
	a, _ := testApp(f)
	a.cfg.AI = false
	tt := ui.NewTester(a.view, panelWidth, panelHeight)
	tt.Type("go")
	tt.Key(0, ui.KeyEnter)
	if len(f.chats) != 0 || tt.HasText("AI 总结") {
		t.Fatalf("off, yet chats %d pane shown", len(f.chats))
	}
	// The row in the settings, then back to the search.
	toggle := func() {
		a.openTab(tabGeneral)
		tt.Frame()
		if err := tt.Click("搜索后自动 AI 总结"); err != nil {
			t.Fatal(err)
		}
		a.back()
		tt.Frame()
	}
	toggle()
	if !a.cfg.AI || len(f.chats) != 1 || len(a.turns) != 1 {
		t.Fatalf("on: AI %v chats %d turns %d", a.cfg.AI, len(f.chats), len(a.turns))
	}
	if !tt.HasText("AI 总结") {
		t.Errorf("no summary after the settings: %q", tt.Texts())
	}
	// Off again: the sources alone, and nothing new asked.
	toggle()
	if a.cfg.AI || tt.HasText("AI 总结") {
		t.Errorf("off: AI %v texts %q", a.cfg.AI, tt.Texts())
	}
	if len(f.chats) != 1 {
		t.Errorf("off, yet chats %d", len(f.chats))
	}
}

func TestSplit(t *testing.T) {
	if w := splitWidth(0, 1180); math.Abs(float64(w-430)) > 0.5 {
		t.Errorf("default: %v", w)
	}
	if w := splitWidth(0.1, 1180); w != minLeft {
		t.Errorf("min left: %v", w)
	}
	if w := splitWidth(0.95, 1180); w != 1180-minRight {
		t.Errorf("min right: %v", w)
	}
	if w := splitWidth(0.5, 500); w > 250 || w < 160 {
		t.Errorf("narrow: %v", w)
	}

	a, _ := testApp(&fake{results: canned, answer: []string{"总结"}})
	saved := 0
	a.changed = func(config) { saved++ }
	tt := ui.NewTester(a.view, expandedWidth, panelHeight)
	tt.Type("go")
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	r, ok := tt.Find("分隔条")
	if !ok {
		t.Fatal("no divider")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	for i := 1; i <= 10; i++ {
		tt.Move(x+float32(i)*12, y)
	}
	tt.Release(x+120, y)
	tt.Frame()
	if l := a.leftWidth(); l < 530 || l > 570 {
		t.Errorf("dragged 120: left %v (split %v, panes %v)", l, a.cfg.Split, a.panesW)
	}
	if saved == 0 {
		t.Error("split not saved")
	}
	r, _ = tt.Find("分隔条")
	tt.ClickAt(r.X+r.W/2, y)
	tt.ClickAt(r.X+r.W/2, y)
	if a.cfg.Split != 0 {
		t.Errorf("double-click: split %v", a.cfg.Split)
	}
}

func TestPanelSize(t *testing.T) {
	c := defaultConfig()
	if widthFor(c, false, 3000) != panelWidth || widthFor(c, true, 3000) != expandedWidth || heightFor(c) != panelHeight {
		t.Error("defaults")
	}
	c.Width, c.WideWidth, c.Height = 800, 1400, 700
	if widthFor(c, false, 3000) != 800 || widthFor(c, true, 3000) != 1400 || heightFor(c) != 700 {
		t.Error("remembered")
	}
	if widthFor(c, true, 1280) != 1232 {
		t.Error("screen")
	}
	c.Width, c.Height = 100, 100
	if widthFor(c, false, 3000) != minWidth || heightFor(c) != minHeight {
		t.Error("min")
	}
}

// Every text color of the opaque panel is WCAG AA on its background.
func TestContrast(t *testing.T) {
	lum := func(h string) float64 {
		var c [3]float64
		for i := range c {
			v, _ := strconv.ParseUint(h[1+2*i:3+2*i], 16, 8)
			s := float64(v) / 255
			if s <= 0.03928 {
				c[i] = s / 12.92
			} else {
				c[i] = math.Pow((s+0.055)/1.055, 2.4)
			}
		}
		return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
	}
	for dark, p := range colors {
		bg := lum(p["bg"])
		for _, n := range []string{"text", "muted", "faint", "link", "gold", "green", "danger"} {
			l := lum(p[n])
			r := (max(l, bg) + 0.05) / (min(l, bg) + 0.05)
			if r < 4.5 {
				t.Errorf("dark %v: %s %s on %s: %.2f", dark, n, p[n], p["bg"], r)
			}
		}
	}
}
