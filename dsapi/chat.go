package dsapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Message is a chat message, in the OpenAI format DeepSeek takes.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a function the model calls.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON
	} `json:"function"`
}

// Tool is a function the model may call.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// SearchTool is web_search(query, count?), which the app runs with Search.
var SearchTool = Tool{Type: "function", Function: FunctionDef{
	Name: "web_search",
	Description: "用 DeepSeek 搜索引擎搜索网页。只有当已有的搜索结果不足以回答用户的追问（需要新的事实、更新的信息或另一个话题）时才调用。" +
		"返回带编号的结果；调用后本轮回答只引用这些新编号。",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "搜索词，简洁，可用 site: after: 等语法，最多 128 字"},
			"count": map[string]any{"type": "integer", "description": "要几条结果，1–10，默认 8"},
		},
		"required": []string{"query"},
	},
}}

// Reply is what one chat completion returned.
type Reply struct {
	Content   string
	ToolCalls []ToolCall
	Usage     *Usage
	Finish    string
}

// Chat streams one chat completion of msgs, with tools the model may call,
// calling onDelta with each piece of content as it comes.
func (c *Client) Chat(ctx context.Context, msgs []Message, tools []Tool, onDelta func(string)) (Reply, error) {
	body := map[string]any{
		"model": c.ModelName(), "messages": msgs, "stream": true,
		"stream_options": map[string]bool{"include_usage": true},
		"thinking":       map[string]string{"type": "disabled"}, "temperature": 0.3, "max_tokens": 1200,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	var out Reply
	res, err := c.post(ctx, "/chat/completions", body)
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return out, apiError(res)
	}
	var content strings.Builder
	calls := map[int]*ToolCall{}
	order := []int{}
	finish := func() Reply {
		out.Content = content.String()
		for _, i := range order {
			if tc := calls[i]; tc.Function.Name != "" {
				if tc.Type == "" {
					tc.Type = "function"
				}
				out.ToolCalls = append(out.ToolCalls, *tc)
			}
		}
		return out
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		payload, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			return finish(), nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *Usage `json:"usage"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		if chunk.Usage != nil {
			out.Usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != "" {
			out.Finish = ch.FinishReason
		}
		if ch.Delta.Content != "" {
			content.WriteString(ch.Delta.Content)
			if onDelta != nil {
				onDelta(ch.Delta.Content)
			}
		}
		for _, d := range ch.Delta.ToolCalls {
			tc := calls[d.Index]
			if tc == nil {
				tc = &ToolCall{}
				calls[d.Index] = tc
				order = append(order, d.Index)
			}
			if d.ID != "" {
				tc.ID = d.ID
			}
			if d.Type != "" {
				tc.Type = d.Type
			}
			tc.Function.Name += d.Function.Name
			tc.Function.Arguments += d.Function.Arguments
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return finish(), fmt.Errorf("流式输出中断：%w", err)
	}
	return finish(), ctx.Err()
}

// Sources lists results for the model, numbered from first: [first] …
func Sources(results []Result, first int) string {
	var b strings.Builder
	for i, r := range results {
		text := r.Summary
		if text == "" {
			text = r.Snippet
		}
		date := "未知"
		if t := r.Time(); !t.IsZero() {
			date = t.In(Shanghai).Format("2006-01-02")
		}
		fmt.Fprintf(&b, "[%d] %s\n来源：%s %s\n日期：%s\n%s\n\n", first+i, trim(r.Title, 200), r.SiteName, trim(r.URL, 500), date, trim(text, 1200))
	}
	return b.String()
}

// ChatSystem is the system prompt of a conversation about a search.
func ChatSystem(now time.Time) string {
	return "你是一个搜索引擎里的答案助手。今天是 " + now.In(Shanghai).Format("2006-01-02") + "（北京时间）。" +
		"根据给出的搜索结果，用简体中文简洁地回答（一般 3～6 句或几条要点，可用少量 Markdown 加粗/列表）。" +
		"每个事实后用 [n] 标注来源编号，可多个如 [1][3]。结果不足以回答时直说，不要编造。" +
		"不要复述问题，不要写“根据搜索结果”之类的开场白。\n" +
		"用户会追问。已有结果足够时直接回答，沿用最近一份结果的编号；需要新信息时调用 web_search（每轮最多 2 次），" +
		"它返回的结果重新从 [1] 编号（第二次接着编号），这时本轮回答只引用这些新编号。"
}

// FirstMessages is a conversation's start: the search's results, and
// question, or a summary of them for query when question is "".
func FirstMessages(query string, results []Result, question string, now time.Time) []Message {
	user := "查询：" + query + "\n\n搜索结果：\n" + Sources(results, 1)
	if question != "" {
		user += "问题：" + question
	} else {
		user += "请总结回答这个查询。"
	}
	return []Message{{Role: "system", Content: ChatSystem(now)}, {Role: "user", Content: user}}
}
