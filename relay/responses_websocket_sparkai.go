package relay

import (
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"

	"github.com/gorilla/websocket"
)

// SparkAI fork: Responses WebSocket（Codex 等客户端使用）发给用户的事件里，
// 把 response.model / model 改回用户请求的名字，和 HTTP 接口保持一致。
// 这条路径不经过 HTTP 中间件，所以单独处理：所有发给用户的消息都走
// writeClient 和 runRequest 里的终止事件写出，两处都调用 rewriteClientModel。

// sparkaiPublicModel 在请求协程里写、在上游读取协程里读，所以用原子指针。
type sparkaiPublicModel struct {
	value atomic.Pointer[string]
}

func (s *responsesWSSession) setPublicModel(model string) {
	s.publicModel.value.Store(&model)
}

func (s *responsesWSSession) rewriteClientModel(kind int, message []byte) []byte {
	if kind != websocket.TextMessage {
		return message
	}
	model := s.publicModel.value.Load()
	if model == nil || *model == "" {
		return message
	}
	if rewritten, changed := common.RewriteResponseModel(message, *model); changed {
		return rewritten
	}
	return message
}
