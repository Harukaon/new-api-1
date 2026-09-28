package middleware_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const requested = "deepseek-v4.1"

// newEngine 模拟线上的中间件顺序：Distribute 先把用户请求的模型名写进上下文，再包一层改写。
func newEngine(model string, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/v1/test", func(c *gin.Context) {
		if model != "" {
			c.Set("original_model", model)
		}
	}, middleware.ResponseModelRewrite(), handler)
	return engine
}

func serve(engine *gin.Engine) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/test", strings.NewReader("{}")))
	return recorder
}

func TestNonStreamViaIOCopyBytesGracefully(t *testing.T) {
	upstream := []byte(`{"id":"1","model":"deepseek/deepseek-v4.1","choices":[]}`)
	engine := newEngine(requested, func(c *gin.Context) {
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}}
		service.IOCopyBytesGracefully(c, resp, upstream)
	})
	rec := serve(engine)
	want := `{"id":"1","model":"deepseek-v4.1","choices":[]}`
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
		t.Fatalf("content-length = %q, want %d", rec.Header().Get("Content-Length"), len(want))
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestNonStreamDirectWriteAndErrorStatus(t *testing.T) {
	engine := newEngine(requested, func(c *gin.Context) {
		c.Writer.Header().Set("Content-Type", "application/json")
		c.Writer.WriteHeader(http.StatusBadRequest)
		// 分两次写，模拟 io.Copy
		_, _ = c.Writer.Write([]byte(`{"model":"up/`))
		_, _ = c.Writer.Write([]byte(`name","error":{"message":"bad"}}`))
	})
	rec := serve(engine)
	if rec.Code != http.StatusBadRequest || rec.Body.String() != `{"model":"deepseek-v4.1","error":{"message":"bad"}}` {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGinJSONRender(t *testing.T) {
	engine := newEngine(requested, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"model": "up", "object": "list"})
	})
	rec := serve(engine)
	if !strings.Contains(rec.Body.String(), `"model":"deepseek-v4.1"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestStreamViaHelpersAndSplitChunks(t *testing.T) {
	engine := newEngine(requested, func(c *gin.Context) {
		helper.SetEventStreamHeaders(c)
		_ = helper.StringData(c, `{"id":"1","model":"deepseek/deepseek-v4.1","choices":[{"delta":{"content":"hi"}}]}`)
		_ = helper.PingData(c)
		// 一个事件被切成三段写出
		_, _ = c.Writer.Write([]byte(`data: {"id":"2","mo`))
		c.Writer.Flush()
		_, _ = c.Writer.Write([]byte(`del":"deepseek/deepseek-v4.1"}` + "\r\n"))
		_, _ = c.Writer.Write([]byte("\r\n"))
		_ = helper.StringData(c, "[DONE]")
	})
	rec := serve(engine)
	body := rec.Body.String()
	if strings.Contains(body, "deepseek/deepseek-v4.1") {
		t.Fatalf("upstream name leaked: %s", body)
	}
	for _, want := range []string{
		`data: {"id":"1","model":"deepseek-v4.1","choices":[{"delta":{"content":"hi"}}]}`,
		": PING\n\n",
		"data: {\"id\":\"2\",\"model\":\"deepseek-v4.1\"}\r\n\r\n",
		"data: [DONE]",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %q", want, body)
		}
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestClaudeStreamEvents(t *testing.T) {
	engine := newEngine(requested, func(c *gin.Context) {
		helper.SetEventStreamHeaders(c)
		_, _ = io.WriteString(c.Writer, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude-up\"}}\n\n")
		c.Writer.Flush()
	})
	body := serve(engine).Body.String()
	if !strings.Contains(body, `"message":{"id":"m","model":"deepseek-v4.1"}`) || !strings.Contains(body, "event: message_start\n") {
		t.Fatalf("body = %s", body)
	}
}

func TestStreamViaGinStream(t *testing.T) {
	engine := newEngine(requested, func(c *gin.Context) {
		helper.SetEventStreamHeaders(c)
		sent := false
		c.Stream(func(w io.Writer) bool {
			if sent {
				return false
			}
			sent = true
			c.Render(-1, testEvent(`{"model":"up"}`))
			return true
		})
	})
	// c.Stream 需要真实连接（ResponseRecorder 不支持 CloseNotify），起一个本地服务
	server := httptest.NewServer(engine)
	defer server.Close()
	resp, err := http.Post(server.URL+"/v1/test", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	if !strings.Contains(body, `data: {"model":"deepseek-v4.1"}`) {
		t.Fatalf("body = %s", body)
	}
}

func TestPassthroughWhenNoModelOrBinary(t *testing.T) {
	upstream := []byte(`{"model":"deepseek/deepseek-v4.1"}`)
	engine := newEngine("", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/json", upstream)
	})
	if got := serve(engine).Body.String(); got != string(upstream) {
		t.Fatalf("no model: body changed: %s", got)
	}

	audio := bytes.Repeat([]byte{0xff, 0xfb, '{', '"'}, 100)
	engine = newEngine(requested, func(c *gin.Context) {
		c.Data(http.StatusOK, "audio/mpeg", audio)
	})
	rec := serve(engine)
	if !bytes.Equal(rec.Body.Bytes(), audio) || rec.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("binary body changed")
	}
}

func TestWrittenReportsBufferedBody(t *testing.T) {
	var written bool
	engine := newEngine(requested, func(c *gin.Context) {
		c.Writer.Header().Set("Content-Type", "application/json")
		_, _ = c.Writer.Write([]byte(`{"model":"x"}`))
		written = c.Writer.Written()
	})
	serve(engine)
	if !written {
		t.Fatal("Written() must be true once the body is buffered, otherwise callers may write a second response")
	}
}

type testEvent string

func (e testEvent) Render(w http.ResponseWriter) error {
	_, err := w.Write([]byte("data: " + string(e) + "\n\n"))
	return err
}

func (e testEvent) WriteContentType(http.ResponseWriter) {}
