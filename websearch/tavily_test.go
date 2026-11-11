package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestTranslate(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want query
	}{
		{
			`golang site:go.dev -site:example.com "release notes" after:2026-01 before:2026-12 lang:zh`,
			query{text: `golang "release notes"`, include: []string{"go.dev"}, exclude: []string{"example.com"}, start: "2026-01-01", end: "2026-12-31"},
		},
		{"site:github.com", query{text: "web", include: []string{"github.com"}}},
		{"天气 after:2026", query{text: "天气", start: "2026-01-01"}},
		{"  plain   words  ", query{text: "plain words"}},
	} {
		if got := translate(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestTavilySearch(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tvly-test" {
			t.Errorf("authorization %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Go 1.27","url":"https://go.dev/doc/go1.27","content":"Release notes","score":0.92,"published_date":"2026-08-19T00:00:00Z"},
			{"title":"bad","url":"not a url","content":"x","score":0.1}
		]}`))
	}))
	defer srv.Close()
	c := NewTavily("tvly-test")
	c.BaseURL = srv.URL
	rs, err := c.Search(context.Background(), `go site:go.dev after:2026-01-01`, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].URL != "https://go.dev/doc/go1.27" || rs[0].SiteName != "go.dev" {
		t.Fatalf("results %+v", rs)
	}
	if rs[0].Snippet != "Release notes" || rs[0].Date == nil || rs[0].Time().Format("2006-01-02") != "2026-08-19" {
		t.Errorf("result %+v", rs[0])
	}
	if rs[0].RerankScore != 0 || rs[0].Authority != 0 {
		t.Errorf("invented DeepSeek scores: %+v", rs[0])
	}
	if body["query"] != "go" || body["max_results"].(float64) != 20 || body["start_date"] != "2026-01-01" {
		t.Errorf("body %+v", body)
	}
	if got := body["include_domains"].([]any); !reflect.DeepEqual(got, []any{"go.dev"}) {
		t.Errorf("include %v", got)
	}
}

func TestTavilyNoKey(t *testing.T) {
	if _, err := NewTavily("").Search(context.Background(), "go", 1); err != ErrNoKey {
		t.Errorf("err %v", err)
	}
}
