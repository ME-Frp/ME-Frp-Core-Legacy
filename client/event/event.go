package event

import (
	"errors"

	"github.com/fatedier/frp/pkg/msg"
)

var ErrPayloadType = errors.New("错误的 Payload 类型")

type Handler func(payload interface{}) error

type StartProxyPayload struct {
	NewProxyMsg *msg.NewProxy
}

type CloseProxyPayload struct {
	CloseProxyMsg *msg.CloseProxy
}
