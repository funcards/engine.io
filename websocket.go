package eio

import (
	"github.com/funcards/engine.io-parser/v4"
	"io"
	"net/url"
)

type WebSocket interface {
	io.Closer
	Emitter

	Query() url.Values
	Headers() map[string]string
	Write(packet eiop.Packet)
}
