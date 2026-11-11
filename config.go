package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// config is what the app remembers between launches, in config.json of
// the app's data folder (~/Library/Application Support/DS Search). The
// API key is never in it (keystore.go).
type config struct {
	Shortcut string `json:"shortcut"`          // global shortcut, e.g. "Alt+Space"
	AI       bool   `json:"ai"`                // the conversation open on the right: a search summarizes
	Sources  bool   `json:"sources,omitempty"` // the sources drawer beside the summary, closed by default
	Model    string `json:"model,omitempty"`   // chat model
	Source   string `json:"source,omitempty"`  // web search provider: "" DeepSeek, or "tavily"
	// Billing: warn under this balance (¥), and over this month's budget (¥, 0 for none).
	LowBalance    float64  `json:"lowBalance"`
	MonthlyBudget float64  `json:"monthlyBudget,omitempty"`
	History       []string `json:"history,omitempty"` // recent queries, newest first, local only
	NoHistory     bool     `json:"noHistory,omitempty"`
	// Where the key is kept: "" the keychain, "file" api-key (0600) in the data folder.
	KeyStore string `json:"keyStore,omitempty"`
	// The panel: opaque unless Translucent; its size, as the user left it
	// (0 for the default); the results' share of it, expanded (0 for the default).
	Translucent bool    `json:"translucent,omitempty"`
	Width       int     `json:"width,omitempty"`
	WideWidth   int     `json:"wideWidth,omitempty"`
	Height      int     `json:"height,omitempty"`
	Split       float64 `json:"split,omitempty"`
}

const (
	defaultShortcut  = "Alt+Space"
	fallbackShortcut = "Alt+Shift+Space"
	secretKey        = "deepseek-api-key"
	tavilySecretKey  = "tavily-api-key"
	maxHistory       = 30
)

func defaultConfig() config { return config{Shortcut: defaultShortcut, LowBalance: 5, AI: true} }

func loadConfig(dir string) config {
	cfg := defaultConfig()
	if b, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	if cfg.Shortcut == "" {
		cfg.Shortcut = defaultShortcut
	}
	return cfg
}

func writeConfig(dir string, cfg config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "config.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "config.json"))
}

// remember puts q first in the history.
func remember(h []string, q string) []string {
	out := []string{q}
	for _, s := range h {
		if s != q && len(out) < maxHistory {
			out = append(out, s)
		}
	}
	return out
}
