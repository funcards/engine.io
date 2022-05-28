package eio

import (
	"context"
	"net/url"
)

type WebSocket interface {
	Emitter

	GetQuery() url.Values
	GetHeaders() map[string]string
	Write(ctx context.Context, data any)
	Close(ctx context.Context)
}
