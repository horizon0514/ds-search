package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/egoist/mygo"
)

// The API key lives in the login keychain, or, where the keychain fails
// (an unsigned app that macOS no longer recognizes after an update), in a
// file only the user can read, api-key in the app's data folder.

// secretStore is the keychain: mygo.Secrets, or a fake in tests.
type secretStore interface {
	Get(service, account string) ([]byte, error)
	Set(service, account string, v []byte) error
	Delete(service, account string) error
}

type mygoSecrets struct{}

func (mygoSecrets) Get(svc, acct string) ([]byte, error) { return mygo.Secrets.Service(svc).Get(acct) }
func (mygoSecrets) Set(svc, acct string, v []byte) error {
	return mygo.Secrets.Service(svc).Set(acct, v)
}
func (mygoSecrets) Delete(svc, acct string) error { return mygo.Secrets.Service(svc).Delete(acct) }

var errNotFound = mygo.ErrSecretNotFound

// legacy are where older builds may have kept the key: other accounts,
// and the services of builds without a bundle identifier.
var legacy = []struct{ service, account string }{
	{"", "deepseek_api_key"},
	{"DS 搜索", "deepseek-api-key"},
	{"DS 搜索", "deepseek_api_key"},
	{"ds-search", "deepseek-api-key"},
	{"ds-search", "deepseek_api_key"},
}

const keyFile = "api-key"

type keyStore struct {
	secrets secretStore
	dir     string              // the app's data folder, for the file
	env     func(string) string // os.Getenv
}

// keyState is where the key was found, and why the keychain failed if it did.
type keyState struct {
	key  string
	from string // "keychain", "file", "env" or ""
	err  string // the keychain could not be read (not "no key")
}

func (s keyStore) path() string { return filepath.Join(s.dir, keyFile) }

// load finds the key: the keychain, moving it there from where older
// builds kept it; else the file; else DEEPSEEK_API_KEY. In mode "file"
// the keychain is not asked.
func (s keyStore) load(mode string) keyState {
	var st keyState
	if mode == "file" {
		if k, err := s.readFile(); err == nil && k != "" {
			return keyState{key: k, from: "file"}
		}
	}
	b, err := s.secrets.Get("", secretKey)
	switch {
	case err == nil && len(strings.TrimSpace(string(b))) > 0:
		return keyState{key: strings.TrimSpace(string(b)), from: "keychain"}
	case err == nil || errors.Is(err, errNotFound):
		if k := s.migrate(); k != "" {
			return keyState{key: k, from: "keychain"}
		}
	default:
		st.err = keychainErr(err)
	}
	if k, err := s.readFile(); err == nil && k != "" {
		st.key, st.from = k, "file"
		return st
	}
	if k := strings.TrimSpace(s.env("DEEPSEEK_API_KEY")); k != "" {
		st.key, st.from = k, "env"
	}
	return st
}

// migrate moves a key of an older build to the keychain's current item.
func (s keyStore) migrate() string {
	for _, l := range legacy {
		b, err := s.secrets.Get(l.service, l.account)
		k := strings.TrimSpace(string(b))
		if err != nil || k == "" {
			continue
		}
		if s.secrets.Set("", secretKey, []byte(k)) == nil {
			_ = s.secrets.Delete(l.service, l.account)
		}
		return k
	}
	return ""
}

func (s keyStore) readFile() (string, error) {
	b, err := os.ReadFile(s.path())
	return strings.TrimSpace(string(b)), err
}

func (s keyStore) writeFile(key string) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, []byte(key+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

// save keeps key where mode says ("file", or "" for the keychain), and
// says where it went. A keychain that fails to keep it, or to give it
// back, falls back to the file; warn says why.
func (s keyStore) save(key, mode string) (from, warn string, err error) {
	if mode != "file" {
		err := s.secrets.Set("", secretKey, []byte(key))
		if err == nil {
			var b []byte
			if b, err = s.secrets.Get("", secretKey); err == nil && strings.TrimSpace(string(b)) != key {
				err = errors.New("读回的内容不一致")
			}
		}
		if err == nil {
			_ = os.Remove(s.path())
			return "keychain", "", nil
		}
		warn = keychainErr(err)
	}
	if err := s.writeFile(key); err != nil {
		if warn != "" {
			return "", "", fmt.Errorf("钥匙串：%s；本地文件：%v", warn, err)
		}
		return "", "", fmt.Errorf("写入本地文件失败：%v", err)
	}
	if mode == "file" {
		_ = s.secrets.Delete("", secretKey)
	}
	return "file", warn, nil
}

// remove deletes the key everywhere the app keeps it.
func (s keyStore) remove() error {
	err := s.secrets.Delete("", secretKey)
	if errors.Is(err, errNotFound) {
		err = nil
	}
	if e := os.Remove(s.path()); e != nil && !errors.Is(e, os.ErrNotExist) && err == nil {
		err = e
	}
	if err != nil {
		return errors.New(keychainErr(err))
	}
	return nil
}

var osStatus = regexp.MustCompile(`OSStatus (-?\d+)`)

// keychainErr says in Chinese why the keychain failed.
func keychainErr(err error) string {
	why := err.Error()
	if m := osStatus.FindStringSubmatch(why); m != nil {
		switch m[1] {
		case "-128":
			why = "授权请求被取消"
		case "-25293":
			why = "授权失败"
		case "-25308":
			why = "此时不允许弹出授权"
		case "-25291", "-25294", "-25295":
			why = "找不到可用的钥匙串"
		case "-34018":
			why = "App 缺少钥匙串权限"
		default:
			why = "错误 " + m[1]
		}
	}
	return "钥匙串不可用（" + why + "）"
}

// maskKey shows a key as sk-••••1234.
func maskKey(k string) string {
	r := []rune(k)
	if len(r) <= 8 {
		return "••••"
	}
	prefix := ""
	if strings.HasPrefix(k, "sk-") {
		prefix = "sk-"
	}
	return prefix + "••••" + string(r[len(r)-4:])
}
