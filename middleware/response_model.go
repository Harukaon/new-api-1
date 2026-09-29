package middleware

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
)

// SparkAI fork: 把返回里的模型名改回用户请求的名字（见 common/response_model_rewrite.go）。
//
// 各渠道写响应的方式很分散（统一出口、c.Stream、c.Writer.Write、io.Copy…），
// 所以不去改出口，而是在 Distribute 之后包一层 ResponseWriter，所有写给用户的字节都会经过这里：
//   - application/json：整段缓存，处理结束时改好再一次写出（并更新 Content-Length）；
//   - text/event-stream：按行处理，只把完整的行改好立即写出，半行留到下一次，保证不改坏也不延迟；
//   - 其它类型（音频、图片、压缩过的内容等）：原样透传。
//
// 用户请求的模型名取 Distribute 写进上下文的 original_model；取不到就什么都不做。
func ResponseModelRewrite() gin.HandlerFunc {
	return func(c *gin.Context) {
		writer := &responseModelWriter{ResponseWriter: c.Writer, c: c}
		c.Writer = writer
		defer func() {
			writer.finish()
			c.Writer = writer.ResponseWriter
		}()
		c.Next()
	}
}

const (
	modeUndecided = iota
	modePassthrough
	modeJSON
	modeSSE
)

type responseModelWriter struct {
	gin.ResponseWriter
	c        *gin.Context
	mode     int
	model    string
	buffered bytes.Buffer
	pending  []byte
	finished bool
}

func (w *responseModelWriter) decide(firstChunk []byte) {
	if w.mode != modeUndecided {
		return
	}
	w.mode = modePassthrough
	w.model = common.GetContextKeyString(w.c, constant.ContextKeyOriginalModel)
	if w.model == "" || w.Header().Get("Content-Encoding") != "" {
		return
	}
	contentType := strings.ToLower(w.Header().Get("Content-Type"))
	switch {
	case strings.Contains(contentType, "text/event-stream"):
		w.mode = modeSSE
	case strings.Contains(contentType, "json"):
		w.mode = modeJSON
	case contentType == "" && firstChunk != nil:
		// 没声明类型：按内容判断
		trimmed := bytes.TrimSpace(firstChunk)
		switch {
		case bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")):
			w.mode = modeSSE
		case len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '['):
			w.mode = modeJSON
		}
	}
	if w.mode != modePassthrough {
		// 改写后长度会变，原来的 Content-Length 不能再用
		w.Header().Del("Content-Length")
	}
}

func (w *responseModelWriter) Write(data []byte) (int, error) {
	w.decide(data)
	switch w.mode {
	case modeJSON:
		return w.buffered.Write(data)
	case modeSSE:
		w.pending = append(w.pending, data...)
		cut := bytes.LastIndexByte(w.pending, '\n')
		if cut < 0 {
			return len(data), nil
		}
		complete := w.pending[:cut+1]
		out := rewriteSSELines(complete, w.model)
		w.pending = append([]byte(nil), w.pending[cut+1:]...)
		if _, err := w.ResponseWriter.Write(out); err != nil {
			return 0, err
		}
		return len(data), nil
	default:
		return w.ResponseWriter.Write(data)
	}
}

func (w *responseModelWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// WriteHeaderNow 会立刻把响应头发出去。JSON 要等改完才知道长度，所以先不发。
func (w *responseModelWriter) WriteHeaderNow() {
	w.decide(nil)
	if w.mode == modeJSON {
		return
	}
	w.ResponseWriter.WriteHeaderNow()
}

func (w *responseModelWriter) Flush() {
	if w.mode == modeJSON {
		return
	}
	w.ResponseWriter.Flush()
}

func (w *responseModelWriter) Written() bool {
	return w.buffered.Len() > 0 || len(w.pending) > 0 || w.ResponseWriter.Written()
}

func (w *responseModelWriter) Size() int {
	if !w.ResponseWriter.Written() && w.buffered.Len() > 0 {
		return w.buffered.Len()
	}
	return w.ResponseWriter.Size()
}

func (w *responseModelWriter) finish() {
	if w.finished {
		return
	}
	w.finished = true
	switch w.mode {
	case modeJSON:
		body := w.buffered.Bytes()
		if rewritten, changed := common.RewriteResponseModel(body, w.model); changed {
			body = rewritten
		}
		if !w.ResponseWriter.Written() {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		if len(body) > 0 {
			_, _ = w.ResponseWriter.Write(body)
		} else {
			w.ResponseWriter.WriteHeaderNow()
		}
	case modeSSE:
		if len(w.pending) > 0 {
			_, _ = w.ResponseWriter.Write(rewriteSSELines(w.pending, w.model))
			w.pending = nil
		}
		w.ResponseWriter.Flush()
	}
}

// rewriteSSELines 逐行改写，保留原来的换行符（\n 或 \r\n）。
func rewriteSSELines(chunk []byte, model string) []byte {
	var out bytes.Buffer
	out.Grow(len(chunk))
	for len(chunk) > 0 {
		lineEnd := bytes.IndexByte(chunk, '\n')
		var line, newline []byte
		if lineEnd < 0 {
			line, chunk = chunk, nil
		} else {
			line, newline, chunk = chunk[:lineEnd], chunk[lineEnd:lineEnd+1], chunk[lineEnd+1:]
		}
		carriage := bytes.HasSuffix(line, []byte("\r"))
		if carriage {
			line = line[:len(line)-1]
		}
		rewritten, _ := common.RewriteSSEModelLine(line, model)
		// 流式中途上游插入的错误事件：整行换成统一错误，不把原文给用户（见 common/upstream_error_mask.go）
		rewritten, _ = common.MaskUpstreamErrorSSELine(rewritten)
		out.Write(rewritten)
		if carriage {
			out.WriteByte('\r')
		}
		out.Write(newline)
	}
	return out.Bytes()
}
