package eio

import (
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
		Send(payload eiop.Payload) error
		Close() error
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

func (t *transport) Close() error {
	switch t.state {
	case Closed, Closing:
		return nil
	}
	t.state = Closing
	return t.Emit("doClose")
}

func (t *transport) onError(reason, description string) error {
	if t.Has(TopicError) {
		return t.Emit(TopicError, reason, description)
	}
	return nil
}

func (t *transport) onPacket(packet eiop.Packet) error {
	return t.Emit(TopicPacket, packet)
}

func (t *transport) onData(data any) error {
	packet, err := eiop.DecodePacket(data)
	if err != nil {
		return err
	}

	return t.onPacket(packet)
}

func (t *transport) onClose() error {
	t.state = Closed
	return t.Emit(TopicClose)
}

func NewWebSocketTransport(webSocket WebSocket, logger *zap.Logger) *webSocketTransport {
	t := &webSocketTransport{
		transport: newTransport(logger),
		webSocket: webSocket,
	}
	t.On("doClose", func(*Event) error {
		return webSocket.Close()
	})
	webSocket.On(TopicMessage, func(event *Event) error {
		return t.onData(event.Get(0))
	})
	webSocket.On(TopicClose, func(*Event) error {
		return t.onClose()
	})
	webSocket.On(TopicError, func(event *Event) error {
		return t.onError(event.String(0), event.String(1))
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

func (t *webSocketTransport) Send(payload eiop.Payload) error {
	for _, packet := range payload {
		switch packet.Data.(type) {
		case []byte, string, nil:
			if err := t.webSocket.Write(packet.Encode(true)); err != nil {
				return err
			}
		default:
			return ErrInvalidPacketData
		}
	}
	return nil
}
