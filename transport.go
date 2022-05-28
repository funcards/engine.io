package eio

import (
	"context"
	"errors"
	"github.com/funcards/engine.io-parser/v4"
	"go.uber.org/zap"
	"net/url"
)

var _ Transport = (*webSocketTransport)(nil)

const (
	TransportWebSocket = "websocket"
	TransportPolling   = "polling"
)

var ErrInvalidPacketData = errors.New("invalid packet data")

type (
	Transport interface {
		Emitter

		GetName() string
		GetInitialQuery() url.Values
		GetInitialHeaders() map[string]string
		IsWritable() bool
		Send(ctx context.Context, payload eiop.Payload)
		Close(ctx context.Context)
	}

	transport struct {
		Emitter
		state State
		log   *zap.Logger
	}

	webSocketTransport struct {
		*transport
		webSocket WebSocket
	}
)

func newTransport(logger *zap.Logger) *transport {
	return &transport{
		Emitter: NewEmitter(logger),
		state:   Open,
		log:     logger,
	}
}

func (t *transport) Close(ctx context.Context) {
	switch t.state {
	case Closed, Closing:
		return
	}
	t.state = Closing
	t.Emit(ctx, "doClose")
}

func (t *transport) onError(ctx context.Context, reason, description string) {
	if t.Has(TopicError) {
		t.Emit(ctx, TopicError, reason, description)
	}
}

func (t *transport) onPacket(ctx context.Context, packet eiop.Packet) {
	t.Emit(ctx, TopicPacket, packet)
}

func (t *transport) onData(ctx context.Context, data any) {
	packet, err := eiop.DecodePacket(data)
	if err != nil {
		t.log.Error("engine.io decode packet", zap.Any("data", data))
		TryCancel(ctx, err)
		return
	}
	t.onPacket(ctx, packet)
}

func (t *transport) onClose(ctx context.Context) {
	t.state = Closed
	t.Emit(ctx, TopicClose)
}

func NewWebSocketTransport(webSocket WebSocket, logger *zap.Logger) *webSocketTransport {
	t := &webSocketTransport{
		transport: newTransport(logger),
		webSocket: webSocket,
	}
	t.On("doClose", func(ctx context.Context, _ *Event) {
		webSocket.Close(ctx)
	})
	webSocket.On(TopicMessage, func(ctx context.Context, event *Event) {
		t.onData(ctx, event.Get(0))
	})
	webSocket.On(TopicClose, func(ctx context.Context, _ *Event) {
		t.onClose(ctx)
	})
	webSocket.On(TopicError, func(ctx context.Context, event *Event) {
		t.onError(ctx, event.String(0), event.String(1))
	})

	return t
}

func (t *webSocketTransport) GetName() string {
	return TransportWebSocket
}

func (t *webSocketTransport) GetInitialQuery() url.Values {
	return t.webSocket.GetQuery()
}

func (t *webSocketTransport) GetInitialHeaders() map[string]string {
	return t.webSocket.GetHeaders()
}

func (t *webSocketTransport) IsWritable() bool {
	return true
}

func (t *webSocketTransport) Send(ctx context.Context, payload eiop.Payload) {
	for _, packet := range payload {
		switch packet.Data.(type) {
		case []byte, string, nil:
			t.webSocket.Write(ctx, packet.Encode(true))
		default:
			TryCancel(ctx, ErrInvalidPacketData)
		}
	}
}
