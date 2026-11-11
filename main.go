// ds-search is a personal web search in the menu bar, on DeepSeek's
// Search API: ⌥Space shows a panel over every app; type, Enter to search,
// ↑↓ to choose, Enter to open, Tab for an AI summary, Space to preview.
package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ds-search/billing"
	"ds-search/dsapi"
	"ds-search/websearch"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

const version = "0.2.1"

type shell struct {
	a        *app
	panel    *mygo.Window
	tray     *mygo.Tray
	shortcut string
	dir      string
	saving   sync.Mutex
	quitting bool
	menu     func() *mygo.Menu

	resizeSeq int // the latest resize: an older one's steps stop
	animating bool
	saveTimer *time.Timer
}

// hideOnClose tells whether closing the panel (Escape, ⌘W) should hide it
// instead: always, but while the app quits. App.Quit asks every window to
// close and gives up when one refuses, as the panel did in 0.1.0, which
// could not be quit.
func hideOnClose(quitting bool) bool { return !quitting }

// quitDeadline is how long quitting may take before the process exits
// anyway.
const quitDeadline = 3 * time.Second

// quit quits for sure: the config saved, the shortcut released, App.Quit,
// and os.Exit should anything hold it up.
func (s *shell) quit() {
	if s.quitting {
		return
	}
	s.quitting = true
	s.save(s.a.cfg)
	mygo.GlobalShortcut.UnregisterAll()
	go func() {
		time.Sleep(quitDeadline)
		log.Print("quit: still running after ", quitDeadline, ", exiting")
		os.Exit(0)
	}()
	mygo.App.Quit()
}

// combinedSource keeps DeepSeek for reading and the model, while the
// search comes from whichever provider the user chose.
type combinedSource struct {
	*dsapi.Client
	search searcher
}

func (c *combinedSource) Name() string { return c.search.Name() }

func (c *combinedSource) Search(ctx context.Context, q string, n int) ([]dsapi.Result, error) {
	return c.search.Search(ctx, q, n)
}

func newSource(cfg config, key, tavilyKey string) source {
	c := dsapi.New(key)
	if cfg.Model != "" {
		c.Model = cfg.Model
	}
	var search searcher = c
	if cfg.Source == "tavily" {
		search = websearch.NewTavily(tavilyKey)
	}
	return &combinedSource{Client: c, search: search}
}

func (s *shell) ready() {
	dir := dataDir()
	s.dir = dir
	cfg := loadConfig(dir)
	ks := keyStore{secrets: mygoSecrets{}, dir: dir, env: os.Getenv}
	st := ks.load(cfg.KeyStore)
	tv := ks.loadTavily(cfg.KeyStore)
	a := newApp(cfg, st.key, st.from, tv.key, newSource)
	a.keys, a.keyErr, a.dataDir = ks, st.err, dir
	a.tvErr = tv.err
	a.tvFrom = tv.from
	if st.key == "" && st.err != "" {
		a.settingsMsg, a.settingsErr = "", true
	}
	a.ledger = billing.Open(filepath.Join(dir, "usage.json"))
	s.a = a

	s.panel = mygo.NewWindow(mygo.WindowOptions{
		Title:       "DS 搜索",
		Panel:       true,
		Hidden:      true,
		Frameless:   true,
		Transparent: true,
		Vibrancy:    vibrancy(cfg.Translucent),
		Width:       widthFor(cfg, a.wide(), expandedWidth+48),
		Height:      heightFor(cfg),
		MinWidth:    minWidth,
		MinHeight:   minHeight,
		Content:     ui.View(a.view),
	})
	s.panel.OnClose(func(e *mygo.CloseEvent) {
		if hideOnClose(s.quitting) {
			e.PreventDefault()
			s.panel.Hide()
		}
	})
	// However it quits (the menu, ⌘Q, logging out), the panel lets it.
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { s.quitting = true })
	s.panel.OnBlur(s.panel.Hide)

	a.post = func(fn func()) { s.panel.Update(fn) }
	a.spawn = func(fn func()) { go fn() }
	a.hide = s.panel.Hide
	a.quit = s.quit
	a.open = func(u string) {
		if err := mygo.Shell.OpenExternal(u); err != nil {
			log.Print("open: ", err)
		}
	}
	a.changed = func(cfg config) { go s.save(cfg) }
	a.copy = func(t string) {
		if err := mygo.Clipboard.Write(transfer.TextData(t)); err != nil {
			log.Print("copy: ", err)
		}
	}
	a.setShort = s.setShortcut
	a.resize = s.resize
	a.setTranslucent = func(on bool) { s.panel.SetVibrancy(vibrancy(on)) }
	s.panel.OnResize(s.resized)
	a.onUsage = func() {
		if s.tray != nil {
			s.tray.SetToolTip("DS 搜索 · " + a.trayLine())
			s.tray.SetMenu(s.menu()) // its first line shows today's cost
		}
	}

	if err := s.setShortcut(cfg.Shortcut); err != nil {
		log.Printf("shortcut %s: %v", cfg.Shortcut, err)
		if cfg.Shortcut != fallbackShortcut && s.setShortcut(fallbackShortcut) == nil {
			a.cfg.Shortcut = fallbackShortcut
		}
	}
	s.setUpTray()
	a.refreshBalance(true)
	if !mygo.App.WasOpenedAtLogin() {
		s.show()
	}
}

func (s *shell) setShortcut(acc string) error {
	if err := mygo.GlobalShortcut.Register(acc, s.toggle); err != nil {
		return err
	}
	if s.shortcut != "" && s.shortcut != acc {
		mygo.GlobalShortcut.Unregister(s.shortcut)
	}
	s.shortcut = acc
	return nil
}

func (s *shell) save(cfg config) {
	s.saving.Lock()
	defer s.saving.Unlock()
	if err := writeConfig(s.dir, cfg); err != nil {
		log.Print("config: ", err)
	}
}

// show shows the panel on the display with the pointer, a fifth of the
// way down, with the query selected.
func (s *shell) show() {
	d := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint())
	w, h := widthFor(s.a.cfg, s.a.wide(), d.WorkArea.Width), min(heightFor(s.a.cfg), d.WorkArea.Height-40)
	s.animating = true
	defer func() { s.animating = false }()
	s.panel.SetBounds(mygo.Rectangle{X: d.WorkArea.X + (d.WorkArea.Width-w)/2, Y: d.WorkArea.Y + (d.WorkArea.Height-h)/5, Width: w, Height: h})
	if s.a.page == pageReader {
		s.a.page = pageSearch
	}
	s.a.focusQuery = true // typing replaces the last query; ↓ goes back to its results
	s.a.input.Focus()
	s.a.refreshBalance(false)
	s.panel.Show()
}

func (s *shell) toggle() {
	if s.panel.IsVisible() {
		s.panel.Hide()
	} else {
		s.show()
	}
}

func (s *shell) setUpTray() {
	var err error
	s.tray, err = mygo.NewTray(mygo.TrayOptions{Icon: trayIcon(), IconIsTemplate: true, ToolTip: "DS 搜索"})
	if err != nil {
		log.Print("no tray icon: ", err)
		return
	}
	s.menu = func() *mygo.Menu {
		return mygo.NewMenu([]*mygo.MenuItem{
			{Label: s.a.trayLine(), Disabled: true},
			mygo.Separator(),
			{Label: "打开搜索", Accelerator: s.shortcut, Click: func(*mygo.MenuItem, *mygo.Window) { s.show() }},
			{Label: "用量与费用…", Click: func(*mygo.MenuItem, *mygo.Window) { s.a.openUsage(); s.show() }},
			{Label: "设置…", Click: func(*mygo.MenuItem, *mygo.Window) { s.a.openSettings(); s.show() }},
			mygo.Separator(),
			{Label: "开机自动启动", Type: mygo.MenuItemCheckbox, Checked: mygo.App.OpenAtLogin(),
				Click: func(it *mygo.MenuItem, _ *mygo.Window) {
					if err := mygo.App.SetOpenAtLogin(it.Checked); err != nil {
						log.Print(err)
					}
				}},
			mygo.Separator(),
			{Label: "退出 DS 搜索", Accelerator: "CmdOrCtrl+Q", Click: func(*mygo.MenuItem, *mygo.Window) { s.quit() }},
		})
	}
	// Left and right clicks alike open the menu, which always has 退出; the
	// panel opens from its first item, or the shortcut.
	s.tray.SetMenu(s.menu())
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		s.quitting = true
		mygo.GlobalShortcut.UnregisterAll()
		s.tray.Destroy()
	})
}

// trayIcon draws a magnifying glass with a spark, black on transparent
// for macOS to tint.
func trayIcon() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 36, 36))
	ink := color.NRGBA{A: 255}
	for y := 0; y < 36; y++ {
		for x := 0; x < 36; x++ {
			dx, dy := float64(x)-15, float64(y)-15
			if r := dx*dx + dy*dy; r >= 7.5*7.5 && r <= 10.5*10.5 {
				img.Set(x, y, ink)
			}
		}
	}
	for i := 0; i < 9; i++ {
		draw.Draw(img, image.Rect(22+i, 22+i, 26+i, 26+i), image.NewUniform(ink), image.Point{}, draw.Src)
	}
	for i := -3; i <= 3; i++ { // the spark inside
		img.Set(15+i, 15, ink)
		img.Set(15, 15+i, ink)
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func main() {
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	s := &shell{}
	mygo.App.SetName("DS 搜索")
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnSecondInstance(func([]string, string) { s.show() })
	mygo.App.WhenReady(s.ready)
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

const minWidth, minHeight = 560, 380

// dataDir is where the app keeps its files: ~/Library/Application
// Support/DS Search. Builds up to 0.2.0 kept them under the app's display
// name, "DS 搜索"; they move here.
func dataDir() string {
	base, err := mygo.App.Path(mygo.PathAppData)
	if err != nil {
		log.Fatal(err)
	}
	dir := filepath.Join(base, "DS Search")
	old := filepath.Join(base, "DS 搜索")
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(old); err == nil {
			if err := os.Rename(old, dir); err != nil {
				log.Print("data: move ", old, ": ", err)
			}
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Print("data: ", err)
	}
	mygo.App.SetPath(mygo.PathUserData, dir)
	return dir
}

func vibrancy(on bool) mygo.Vibrancy {
	if on {
		return mygo.VibrancyPopover
	}
	return mygo.VibrancyNone
}

// widthFor is the panel's width, expanded or not, on a screen avail wide:
// as the user left it, or the default.
func widthFor(cfg config, expanded bool, avail int) int {
	w := cfg.Width
	if w == 0 {
		w = panelWidth
	}
	if expanded {
		w = cfg.WideWidth
		if w == 0 {
			w = expandedWidth
		}
	}
	return max(minWidth, min(w, avail-48))
}

func heightFor(cfg config) int {
	if cfg.Height == 0 {
		return panelHeight
	}
	return max(minHeight, cfg.Height)
}

// resized remembers the size the user dragged the panel's edges to.
func (s *shell) resized() {
	if s.animating || !s.panel.IsVisible() {
		return
	}
	w, h := s.panel.Size()
	cfg := &s.a.cfg
	if s.a.wide() {
		cfg.WideWidth = w
	} else {
		cfg.Width = w
	}
	cfg.Height = h
	s.saveLater()
}

// saveLater saves the config once resizing stops.
func (s *shell) saveLater() {
	if s.saveTimer != nil {
		s.saveTimer.Stop()
	}
	cfg := s.a.cfg
	s.saveTimer = time.AfterFunc(600*time.Millisecond, func() { s.save(cfg) })
}

// resize widens the panel for the conversation, or narrows it back,
// about its centre and on its screen, in a few quick steps.
func (s *shell) resize(expanded bool) {
	s.resizeSeq++
	seq, cfg := s.resizeSeq, s.a.cfg
	s.animating = true // not the user's size, which resized remembers
	go func() {
		defer func() {
			time.Sleep(80 * time.Millisecond)
			s.panel.Update(func() {
				if seq == s.resizeSeq {
					s.animating = false
				}
			})
		}()
		from := s.panel.Bounds()
		d := mygo.Screen.DisplayNearestPoint(mygo.Point{X: from.X + from.Width/2, Y: from.Y + from.Height/2})
		w := widthFor(cfg, expanded, d.WorkArea.Width)
		x := from.X + from.Width/2 - w/2
		x = max(d.WorkArea.X+8, min(x, d.WorkArea.X+d.WorkArea.Width-w-8))
		const steps = 7
		for i := 1; i <= steps; i++ {
			f := float64(i) / steps
			f = 1 - (1-f)*(1-f)*(1-f) // ease out
			lerp := func(a, b int) int { return a + int(float64(b-a)*f) }
			s.panel.Update(func() {
				if seq == s.resizeSeq {
					s.panel.SetBounds(mygo.Rectangle{X: lerp(from.X, x), Y: from.Y, Width: lerp(from.Width, w), Height: from.Height})
				}
			})
			time.Sleep(16 * time.Millisecond)
		}
	}()
}
