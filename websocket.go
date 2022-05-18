package eio

import "net/url"

type WebSocket interface {
	Emitter

	GetQuery() url.Values
	GetHeaders() map[string]string
	Write(data any) error
	Close() error
}
