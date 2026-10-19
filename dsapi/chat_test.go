package dsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// A tool call streams in pieces, by index; they make one call.
func TestChatToolCallStream(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		for _, s := range []string{
			`{"choices":[{"delta":{"content":"","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"query\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"杭州 天气\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_cache_hit_tokens":10,"prompt_cache_miss_tokens":90,"completion_tokens":20}}`,
			`[DONE]`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", s)
		}
	}))
	defer srv.Close()
	c := New("k")
	c.BaseURL = srv.URL
	r, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, []Tool{SearchTool}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].ID != "call_1" || r.ToolCalls[0].Function.Arguments != `{"query":"杭州 天气"}` || r.Finish != "tool_calls" {
		t.Errorf("reply %+v", r)
	}
	if r.Usage == nil || r.Usage.PromptCacheMiss != 90 {
		t.Errorf("usage %+v", r.Usage)
	}
	if tools, _ := got["tools"].([]any); len(tools) != 1 {
		t.Errorf("sent tools %v", got["tools"])
	}
}

// The real API calls web_search when the question needs new facts, and
// answers from the tool's results after.
func TestChatToolsLive(t *testing.T) {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" || testing.Short() {
		t.Skip("DEEPSEEK_API_KEY not set")
	}
	c := New(key)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	msgs := FirstMessages("Go 1.27 发布", []Result{{Title: "Go 1.27 Release Notes", URL: "https://go.dev/doc/go1.27", Snippet: "Go 1.27 arrives six months after Go 1.26."}}, "", time.Now())
	msgs = append(msgs, Message{Role: "assistant", Content: "Go 1.27 于 2026 年 8 月发布 [1]。"},
		Message{Role: "user", Content: "换个话题：今天杭州天气怎么样？"})
	r, err := c.Chat(ctx, msgs, []Tool{SearchTool}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ToolCalls) == 0 {
		t.Fatalf("no tool call: %q (%s)", r.Content, r.Finish)
	}
	var args struct{ Query string }
	_ = json.Unmarshal([]byte(r.ToolCalls[0].Function.Arguments), &args)
	t.Logf("web_search(%q), finish %s, usage %+v", args.Query, r.Finish, r.Usage)
	rs, err := c.Search(ctx, args.Query, 5)
	if err != nil {
		t.Fatal(err)
	}
	msgs = append(msgs, Message{Role: "assistant", Content: r.Content, ToolCalls: r.ToolCalls},
		Message{Role: "tool", ToolCallID: r.ToolCalls[0].ID, Content: Sources(rs, 1)})
	var streamed strings.Builder
	r2, err := c.Chat(ctx, msgs, []Tool{SearchTool}, func(s string) { streamed.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if r2.Content == "" || streamed.String() != r2.Content || r2.Usage == nil {
		t.Errorf("answer %q calls %d usage %v", r2.Content, len(r2.ToolCalls), r2.Usage)
	}
	t.Logf("answer: %s", r2.Content)
}
