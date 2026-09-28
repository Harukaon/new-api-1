package common

import "testing"

const sparkaiTestModel = "deepseek-v4.1"

func TestRewriteResponseModel(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{
			name:    "OpenAI 非流式：只改顶层 model，其余原样（顺序、数字、空格都不变）",
			in:      `{"id":"x","object":"chat.completion","created":1700000000,"model":"deepseek/deepseek-v4.1","choices":[{"index":0,"message":{"role":"assistant","content":"hi \u003c3"}}],"usage":{"prompt_tokens":1.0}}`,
			want:    `{"id":"x","object":"chat.completion","created":1700000000,"model":"deepseek-v4.1","choices":[{"index":0,"message":{"role":"assistant","content":"hi \u003c3"}}],"usage":{"prompt_tokens":1.0}}`,
			changed: true,
		},
		{
			name:    "带空格和换行的 JSON",
			in:      "{\n  \"model\" : \"up/stream\",\n  \"a\": 1\n}",
			want:    "{\n  \"model\" : \"deepseek-v4.1\",\n  \"a\": 1\n}",
			changed: true,
		},
		{
			name:    "Anthropic message_start：改 message.model",
			in:      `{"type":"message_start","message":{"id":"m","model":"claude-upstream","content":[]}}`,
			want:    `{"type":"message_start","message":{"id":"m","model":"deepseek-v4.1","content":[]}}`,
			changed: true,
		},
		{
			name:    "Responses 事件：改 response.model",
			in:      `{"type":"response.created","sequence_number":0,"response":{"id":"r","model":"gpt-upstream","status":"in_progress"}}`,
			want:    `{"type":"response.created","sequence_number":0,"response":{"id":"r","model":"deepseek-v4.1","status":"in_progress"}}`,
			changed: true,
		},
		{
			name:    "Gemini 原生：modelVersion",
			in:      `{"candidates":[],"modelVersion":"gemini-2.5-pro-001"}`,
			want:    `{"candidates":[],"modelVersion":"deepseek-v4.1"}`,
			changed: true,
		},
		{
			name:    "Gemini 原生数组流：每个元素都改",
			in:      `[{"modelVersion":"a"},{"candidates":[]},{"modelVersion":"b"}]`,
			want:    `[{"modelVersion":"deepseek-v4.1"},{"candidates":[]},{"modelVersion":"deepseek-v4.1"}]`,
			changed: true,
		},
		{
			name: "choices 里的 message 不是模型字段，不能动",
			in:   `{"choices":[{"message":{"model":"keep"}}]}`,
			want: `{"choices":[{"message":{"model":"keep"}}]}`,
		},
		{
			name: "更深层的 model 不动",
			in:   `{"response":{"output":[{"model":"keep"}]}}`,
			want: `{"response":{"output":[{"model":"keep"}]}}`,
		},
		{
			name: "已经是对外名：不算改动",
			in:   `{"model":"deepseek-v4.1"}`,
			want: `{"model":"deepseek-v4.1"}`,
		},
		{
			name: "model 不是字符串：不动",
			in:   `{"model":null}`,
			want: `{"model":null}`,
		},
		{name: "不是 JSON", in: `hello`, want: `hello`},
		{name: "坏 JSON", in: `{"model":"x"`, want: `{"model":"x"`},
		{name: "空", in: ``, want: ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := RewriteResponseModel([]byte(tc.in), sparkaiTestModel)
			if string(got) != tc.want || changed != tc.changed {
				t.Fatalf("got %s (changed=%v), want %s (changed=%v)", got, changed, tc.want, tc.changed)
			}
		})
	}
}

func TestRewriteResponseModelEscapes(t *testing.T) {
	got, _ := RewriteResponseModel([]byte(`{"model":"x"}`), `a"b<c`)
	if string(got) != `{"model":"a\"b<c"}` {
		t.Fatalf("got %s", got)
	}
	got, changed := RewriteResponseModel([]byte(`{"model":"x"}`), "")
	if changed || string(got) != `{"model":"x"}` {
		t.Fatalf("empty model must be no-op, got %s", got)
	}
}

func TestRewriteSSEModelLine(t *testing.T) {
	cases := map[string]string{
		`data: {"model":"up","x":1}`: `data: {"model":"deepseek-v4.1","x":1}`,
		`data:{"model":"up"}`:        `data:{"model":"deepseek-v4.1"}`,
		`data: [DONE]`:               `data: [DONE]`,
		`event: message_start`:       `event: message_start`,
		`: PING`:                     `: PING`,
		``:                           ``,
	}
	for in, want := range cases {
		got, _ := RewriteSSEModelLine([]byte(in), sparkaiTestModel)
		if string(got) != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
