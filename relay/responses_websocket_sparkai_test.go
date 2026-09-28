package relay

import (
	"context"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesWSRewritesModelForClient(t *testing.T) {
	peer, server, cleanup := newTestWebSocketPair(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &responsesWSSession{ctx: ctx, cancel: cancel, client: server}

	read := func() (int, string) {
		t.Helper()
		require.NoError(t, peer.SetReadDeadline(time.Now().Add(5*time.Second)))
		kind, body, err := peer.ReadMessage()
		require.NoError(t, err)
		return kind, string(body)
	}

	// 还没开始请求（没有对外模型名）时原样转发
	require.NoError(t, s.writeClient(websocket.TextMessage, []byte(`{"type":"response.created","response":{"model":"gpt-upstream"}}`)))
	_, body := read()
	assert.Equal(t, `{"type":"response.created","response":{"model":"gpt-upstream"}}`, body)

	s.setPublicModel("deepseek-v4.1")

	// 请求过程中的事件
	require.NoError(t, s.writeClient(websocket.TextMessage, []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"r1","model":"gpt-upstream","status":"in_progress"}}`)))
	_, body = read()
	assert.Equal(t, `{"type":"response.created","sequence_number":0,"response":{"id":"r1","model":"deepseek-v4.1","status":"in_progress"}}`, body)

	// 没有模型名的事件不动
	require.NoError(t, s.writeClient(websocket.TextMessage, []byte(`{"type":"response.output_text.delta","delta":"hi"}`)))
	_, body = read()
	assert.Equal(t, `{"type":"response.output_text.delta","delta":"hi"}`, body)

	// 二进制帧不动
	require.NoError(t, s.writeClient(websocket.BinaryMessage, []byte(`{"model":"gpt-upstream"}`)))
	kind, body := read()
	assert.Equal(t, websocket.BinaryMessage, kind)
	assert.Equal(t, `{"model":"gpt-upstream"}`, body)

	// 终止事件（runRequest 结束时直接写 client 的那条路径）
	terminal := s.rewriteClientModel(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"model":"gpt-upstream","usage":{"total_tokens":3}}}`))
	assert.Equal(t, `{"type":"response.completed","response":{"model":"deepseek-v4.1","usage":{"total_tokens":3}}}`, string(terminal))
}
