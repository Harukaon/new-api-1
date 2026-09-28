package common

import (
	"bytes"
	"encoding/json"
)

// SparkAI fork: 返回给用户的模型名改回用户请求的名字。
//
// 渠道做了模型映射（对外 deepseek-v4.1 -> 上游 deepseek/deepseek-v4.1）后，
// 上游返回里的 model 仍是上游名，New API 原样转给用户，会暴露上游。
// 这里只改「写给用户的字节」，计费、日志、选渠道都不受影响。
//
// 只改这几个位置（其它内容逐字节保持原样，包括字段顺序和数字格式）：
//   - 顶层 "model"            OpenAI Chat/Completions/Embeddings 等
//   - 顶层 "modelVersion"     Gemini 原生
//   - "message"."model"       Anthropic 流式 message_start
//   - "response"."model"      OpenAI Responses 流式事件 / WebSocket 事件
//
// 顶层是数组（Gemini 原生非 SSE 流）时，对每个对象元素分别处理。

// RewriteResponseModel 把 JSON 里上面列出的模型名字段改成 model。
// 不是 JSON 对象/数组、解析失败或没有可改字段时，原样返回且 changed=false。
func RewriteResponseModel(body []byte, model string) (out []byte, changed bool) {
	if model == "" {
		return body, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return body, false
	}
	replacement, err := marshalModelName(model)
	if err != nil {
		return body, false
	}
	switch trimmed[0] {
	case '{':
		return rewriteModelObject(body, replacement, 0)
	case '[':
		return rewriteModelArray(body, replacement)
	default:
		return body, false
	}
}

// RewriteSSEModelLine 处理 SSE 的一行（不含换行符）。只改 "data:" 行里的 JSON，
// 其它行（event:、注释、[DONE] 等）原样返回。
func RewriteSSEModelLine(line []byte, model string) ([]byte, bool) {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return line, false
	}
	prefixLen := len("data:")
	if len(line) > prefixLen && line[prefixLen] == ' ' {
		prefixLen++
	}
	payload := line[prefixLen:]
	rewritten, changed := RewriteResponseModel(payload, model)
	if !changed {
		return line, false
	}
	out := make([]byte, 0, prefixLen+len(rewritten))
	out = append(out, line[:prefixLen]...)
	out = append(out, rewritten...)
	return out, true
}

func marshalModelName(model string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(model); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// rewriteModelObject 逐个字段扫描对象，只替换目标字段值所在的字节区间。
// depth=0 是顶层；depth=1 是 message/response 里面，只看 "model"。
func rewriteModelObject(obj []byte, replacement []byte, depth int) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return obj, false
	}
	var out bytes.Buffer
	last := 0
	changed := false
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return obj, false
		}
		key, ok := keyTok.(string)
		if !ok {
			return obj, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return obj, false
		}
		end := int(dec.InputOffset())
		start := end - len(raw)
		if start < last || end > len(obj) {
			return obj, false
		}
		newValue, replace := modelFieldReplacement(key, raw, replacement, depth)
		if !replace {
			continue
		}
		out.Write(obj[last:start])
		out.Write(newValue)
		last = end
		changed = true
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return obj, false
	}
	if !changed {
		return obj, false
	}
	out.Write(obj[last:])
	return out.Bytes(), true
}

func modelFieldReplacement(key string, raw json.RawMessage, replacement []byte, depth int) ([]byte, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	switch {
	case key == "model" && raw[0] == '"':
		return replacement, !bytes.Equal(raw, replacement)
	case depth == 0 && key == "modelVersion" && raw[0] == '"':
		return replacement, !bytes.Equal(raw, replacement)
	case depth == 0 && (key == "message" || key == "response") && raw[0] == '{':
		return rewriteModelObject(raw, replacement, 1)
	}
	return nil, false
}

func rewriteModelArray(arr []byte, replacement []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(arr))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return arr, false
	}
	var out bytes.Buffer
	last := 0
	changed := false
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return arr, false
		}
		end := int(dec.InputOffset())
		start := end - len(raw)
		if start < last || end > len(arr) || len(raw) == 0 || raw[0] != '{' {
			continue
		}
		newValue, ok := rewriteModelObject(raw, replacement, 0)
		if !ok {
			continue
		}
		out.Write(arr[last:start])
		out.Write(newValue)
		last = end
		changed = true
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim(']') {
		return arr, false
	}
	if !changed {
		return arr, false
	}
	out.Write(arr[last:])
	return out.Bytes(), true
}
