package eio

import (
	"context"
	"fmt"
	"github.com/funcards/engine.io-parser/v4"
	"go.uber.org/zap"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

var _ Socket = (*socket)(nil)

const (
	Opening State = iota
	Open
	Closing
	Closed
)

const (
	EmptyUpgrades     = "[]"
	WebSocketUpgrades = "[\"websocket\"]"
	HandshakeJSON     = "{\"sid\": \"%s\", \"upgrades\": %s, \"pingInterval\": %d, \"pingTimeout\": %d}"
)

type (
	State byte

	Socket interface {
		Emitter

		GetSID() string
		GetProtocolVersion() int
		GetState() State
		GetInitialQuery() url.Values
		GetInitialHeaders() map[string]string
		Send(ctx context.Context, packet eiop.Packet)
		Open(ctx context.Context, transport Transport)
		Upgrade(transport Transport)
		CanUpgrade(transport string) bool
		Close(ctx context.Context)
	}

	socket struct {
		Emitter

		mu                sync.Mutex
		cfg               Config
		log               *zap.Logger
		transport         Transport
		state             State
		payload           eiop.Payload
		cleanup           func()
		upgrading         int32
		sid               string
		initialQuery      url.Values
		initialHeaders    map[string]string
		pingFuture        *time.Timer
		pingTimeoutFuture *time.Timer
	}
)

func NewSocket(sid string, cfg Config, logger *zap.Logger) *socket {
	return &socket{
		Emitter: NewEmitter(logger),
		cfg:     cfg,
		log:     logger,
		sid:     sid,
		state:   Opening,
		payload: make(eiop.Payload, 0),
	}
}

func (s *socket) Emit(ctx context.Context, topic string, args ...any) {
	args = append([]any{s}, args...)
	s.Emitter.Emit(ctx, topic, args...)
}

func (s *socket) GetSID() string {
	return s.sid
}

func (s *socket) GetProtocolVersion() int {
	return eiop.Protocol
}

func (s *socket) GetState() State {
	return s.state
}

func (s *socket) GetInitialQuery() url.Values {
	return s.initialQuery
}

func (s *socket) GetInitialHeaders() map[string]string {
	return s.initialHeaders
}

func (s *socket) Send(ctx context.Context, packet eiop.Packet) {
	s.sendPacket(ctx, packet)
}

func (s *socket) Open(ctx context.Context, transport Transport) {
	s.setTransport(transport)
	s.initialQuery = transport.GetInitialQuery()
	s.initialHeaders = transport.GetInitialHeaders()
	s.onOpen(ctx)
}

func (s *socket) Upgrade(transport Transport) {
	atomic.StoreInt32(&(s.upgrading), 1)

	cleanup := func() {
		atomic.StoreInt32(&(s.upgrading), 0)
		transport.Off(TopicPacket, TopicClose, TopicError)
	}

	onError := func(ctx context.Context, event *Event) {
		cleanup()
		transport.Close(ctx)
	}

	transport.On(TopicPacket, func(ctx context.Context, event *Event) {
		packet := event.Get(0).(eiop.Packet)

		if str, ok := packet.Data.(string); ok && "probe" == str && packet.Type == eiop.Ping {
			reply := eiop.PongPacket("probe")

			transport.Send(ctx, eiop.Payload{reply})

			if s.transport.IsWritable() {
				s.transport.Send(ctx, eiop.Payload{eiop.NoopPacket})
			}
			s.Emit(ctx, TopicUpgrading, transport)
		} else if packet.Type == eiop.Upgrade && s.state != Closed && s.state != Closing {
			cleanup()
			s.clearTransport(ctx)
			s.setTransport(transport)
			s.Emit(ctx, TopicUpgrading, transport)
			s.flush(ctx)
			s.schedulePing()
		} else {
			cleanup()
			transport.Close(ctx)
		}
	})
	transport.Once(TopicClose, onError)
	transport.Once(TopicError, onError)

	s.Once(TopicClose, onError)
}

func (s *socket) CanUpgrade(transport string) bool {
	return atomic.LoadInt32(&(s.upgrading)) == 0 && s.transport.GetName() != TransportWebSocket && TransportWebSocket == transport
}

func (s *socket) Close(ctx context.Context) {
	if s.state == Open {
		s.state = Closing

		if len(s.payload) > 0 {
			s.transport.On(TopicDrain, func(ctx context.Context, _ *Event) {
				s.closeTransport(ctx)
			})
		} else {
			s.closeTransport(ctx)
		}
	}
}

func (s *socket) setTransport(transport Transport) {
	s.transport = transport
	transport.Once(TopicError, func(ctx context.Context, _ *Event) {
		s.onError(ctx)
	})
	transport.Once(TopicClose, func(ctx context.Context, event *Event) {
		s.onClose(ctx, "transport close", event.String(0))
	})
	transport.On(TopicPacket, func(ctx context.Context, event *Event) {
		s.onPacket(ctx, event.Get(0).(eiop.Packet))
	})
	transport.On(TopicDrain, func(ctx context.Context, _ *Event) {
		s.flush(ctx)
	})

	s.cleanup = func() {
		transport.Off(TopicError, TopicClose, TopicPacket, TopicDrain)
	}
}

func (s *socket) closeTransport(ctx context.Context) {
	s.transport.Close(ctx)
}

func (s *socket) clearTransport(ctx context.Context) {
	if s.cleanup != nil {
		s.cleanup()
	}
	s.transport.Close(ctx)
}

func (s *socket) onOpen(ctx context.Context) {
	s.state = Open

	var upgrades string
	if s.transport.GetName() == TransportPolling {
		upgrades = WebSocketUpgrades
	} else {
		upgrades = EmptyUpgrades
	}

	packet := eiop.OpenPacket(fmt.Sprintf(HandshakeJSON, s.sid, upgrades, s.cfg.PingInterval.Milliseconds(), s.cfg.PingTimeout.Milliseconds()))
	s.sendPacket(ctx, packet)

	if s.cfg.InitialPacket != nil {
		s.sendPacket(ctx, *s.cfg.InitialPacket)
	}
	s.Emit(ctx, TopicOpen)
	s.schedulePing()
}

func (s *socket) onClose(ctx context.Context, reason, description string) {
	if s.state != Closed {
		s.state = Closed

		s.stopTimers()
		s.clearTransport(ctx)
		s.Emit(ctx, TopicClose, reason, description)
	}
}

func (s *socket) onError(ctx context.Context) {
	s.onClose(ctx, "transport error", "")
}

func (s *socket) onPacket(ctx context.Context, packet eiop.Packet) {
	if s.state != Open {
		return
	}

	s.Emit(ctx, TopicPacket, packet)

	s.resetPingTimeout(s.cfg.PingTimeout + s.cfg.PingInterval)

	switch packet.Type {
	case eiop.Ping:
		s.log.Debug("received ping")
		s.onError(ctx)
	case eiop.Pong:
		s.log.Debug("received pong")
		s.schedulePing()
		s.Emit(ctx, TopicHeartbeat)
	case eiop.Error:
		s.onClose(ctx, "parse error", "")
	case eiop.Message:
		s.Emit(ctx, TopicData, packet.Data)
		s.Emit(ctx, TopicMessage, packet.Data)
	}
}

func (s *socket) sendPacket(ctx context.Context, packet eiop.Packet) {
	switch s.state {
	case Closing, Closed:
		return
	}

	s.mu.Lock()
	s.payload = append(s.payload, packet)
	s.mu.Unlock()

	s.flush(ctx)
}

func (s *socket) flush(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log.Debug("eio.socket flush", zap.Any("payload", s.payload))

	if s.state != Closed && s.transport.IsWritable() && len(s.payload) > 0 {
		s.Emit(ctx, TopicFlush, append(eiop.Payload{}, s.payload...))
		s.transport.Send(ctx, s.payload)
		s.payload = make(eiop.Payload, 0)
		s.Emit(ctx, TopicDrain)
	}
}

func (s *socket) pingInterval() {
	s.log.Debug("eio.Socket ping interval", zap.String("sid", s.GetSID()))

	s.sendPacket(context.Background(), eiop.PingPacket())
	s.resetPingTimeout(s.cfg.PingTimeout)
}

func (s *socket) pingTimeout() {
	s.log.Debug("eio.Socket ping timeout", zap.String("sid", s.GetSID()))
	s.onClose(context.Background(), "ping timeout", "")
}

func (s *socket) schedulePing() {
	s.log.Debug("eio.Socket schedule ping", zap.String("sid", s.GetSID()))

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pingFuture == nil {
		s.pingFuture = time.AfterFunc(s.cfg.PingInterval, s.pingInterval)
	} else {
		s.pingFuture.Reset(s.cfg.PingInterval)
	}
}

func (s *socket) resetPingTimeout(timeout time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pingTimeoutFuture == nil {
		s.pingTimeoutFuture = time.AfterFunc(timeout, s.pingTimeout)
	} else {
		s.pingTimeoutFuture.Reset(timeout)
	}
}

func (s *socket) stopTimers() {
	if s.pingFuture != nil {
		s.pingFuture.Stop()
	}
	if s.pingTimeoutFuture != nil {
		s.pingTimeoutFuture.Stop()
	}
}
