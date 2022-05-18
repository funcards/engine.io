package eio

import (
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
		Send(packet eiop.Packet) error
		Open(transport Transport) error
		Upgrade(transport Transport) error
		CanUpgrade(transport string) bool
		Close() error
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

func (s *socket) Emit(topic string, args ...any) error {
	args = append([]any{s}, args...)
	return s.Emitter.Emit(topic, args...)
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

func (s *socket) Send(packet eiop.Packet) error {
	return s.sendPacket(packet)
}

func (s *socket) Open(transport Transport) error {
	s.setTransport(transport)
	s.initialQuery = transport.GetInitialQuery()
	s.initialHeaders = transport.GetInitialHeaders()
	return s.onOpen()
}

func (s *socket) Upgrade(transport Transport) error {
	atomic.StoreInt32(&(s.upgrading), 1)

	cleanup := func() {
		atomic.StoreInt32(&(s.upgrading), 0)
		transport.Off(TopicPacket, TopicClose, TopicError)
	}

	onError := func(event *Event) error {
		cleanup()
		return transport.Close()
	}

	transport.On(TopicPacket, func(event *Event) error {
		packet := event.Get(0).(eiop.Packet)

		if str, ok := packet.Data.(string); ok && "probe" == str && packet.Type == eiop.Ping {
			reply := eiop.PongPacket("probe")

			if err := transport.Send(eiop.Payload{reply}); err != nil {
				return err
			}

			if s.transport.IsWritable() {
				if err := s.transport.Send(eiop.Payload{eiop.NoopPacket}); err != nil {
					return err
				}
			}

			return s.Emit(TopicUpgrading, transport)
		} else if packet.Type == eiop.Upgrade && s.state != Closed && s.state != Closing {
			cleanup()
			if err := s.clearTransport(); err != nil {
				return err
			}

			s.setTransport(transport)

			if err := s.Emit(TopicUpgrading, transport); err != nil {
				return err
			}
			if err := s.flush(); err != nil {
				return err
			}

			s.schedulePing()
		} else {
			cleanup()
			return transport.Close()
		}
		return nil
	})
	transport.Once(TopicClose, onError)
	transport.Once(TopicError, onError)

	s.Once(TopicClose, onError)

	return nil
}

func (s *socket) CanUpgrade(transport string) bool {
	return atomic.LoadInt32(&(s.upgrading)) == 0 && s.transport.GetName() != TransportWebSocket && TransportWebSocket == transport
}

func (s *socket) Close() error {
	if s.state == Open {
		s.state = Closing

		if len(s.payload) > 0 {
			s.transport.On(TopicDrain, func(*Event) error {
				return s.closeTransport()
			})
		} else {
			return s.closeTransport()
		}
	}
	return nil
}

func (s *socket) setTransport(transport Transport) {
	s.transport = transport
	transport.Once(TopicError, func(*Event) error {
		return s.onError()
	})
	transport.Once(TopicClose, func(event *Event) error {
		return s.onClose("transport close", event.String(0))
	})
	transport.On(TopicPacket, func(event *Event) error {
		return s.onPacket(event.Get(0).(eiop.Packet))
	})
	transport.On(TopicDrain, func(*Event) error {
		return s.flush()
	})

	s.cleanup = func() {
		transport.Off(TopicError, TopicClose, TopicPacket, TopicDrain)
	}
}

func (s *socket) closeTransport() error {
	return s.transport.Close()
}

func (s *socket) clearTransport() error {
	if s.cleanup != nil {
		s.cleanup()
	}
	return s.transport.Close()
}

func (s *socket) onOpen() error {
	s.state = Open

	var upgrades string
	if s.transport.GetName() == TransportPolling {
		upgrades = WebSocketUpgrades
	} else {
		upgrades = EmptyUpgrades
	}

	packet := eiop.OpenPacket(fmt.Sprintf(HandshakeJSON, s.sid, upgrades, s.cfg.PingInterval.Milliseconds(), s.cfg.PingTimeout.Milliseconds()))
	if err := s.sendPacket(packet); err != nil {
		return err
	}

	if s.cfg.InitialPacket != nil {
		if err := s.sendPacket(*s.cfg.InitialPacket); err != nil {
			return err
		}
	}

	if err := s.Emit(TopicOpen); err != nil {
		return err
	}

	s.schedulePing()

	return nil
}

func (s *socket) onClose(reason, description string) error {
	if s.state != Closed {
		s.state = Closed

		s.stopTimers()

		if err := s.clearTransport(); err != nil {
			return err
		}
		return s.Emit(TopicClose, reason, description)
	}

	return nil
}

func (s *socket) onError() error {
	return s.onClose("transport error", "")
}

func (s *socket) onPacket(packet eiop.Packet) error {
	if s.state != Open {
		return nil
	}

	if err := s.Emit(TopicPacket, packet); err != nil {
		return err
	}

	s.resetPingTimeout(s.cfg.PingTimeout + s.cfg.PingInterval)

	switch packet.Type {
	case eiop.Ping:
		s.log.Debug("received ping")
		return s.onError()
	case eiop.Pong:
		s.log.Debug("received pong")
		s.schedulePing()
		return s.Emit(TopicHeartbeat)
	case eiop.Error:
		return s.onClose("parse error", "")
	case eiop.Message:
		if err := s.Emit(TopicData, packet.Data); err != nil {
			return err
		}
		return s.Emit(TopicMessage, packet.Data)
	}

	return nil
}

func (s *socket) sendPacket(packet eiop.Packet) error {
	switch s.state {
	case Closing, Closed:
		return nil
	}

	s.mu.Lock()
	s.payload = append(s.payload, packet)
	s.mu.Unlock()

	return s.flush()
}

func (s *socket) flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log.Debug("eio.socket flush", zap.Any("payload", s.payload))

	if s.state != Closed && s.transport.IsWritable() && len(s.payload) > 0 {
		if err := s.Emit(TopicFlush, append(eiop.Payload{}, s.payload...)); err != nil {
			return err
		}
		if err := s.transport.Send(s.payload); err != nil {
			return err
		}
		s.payload = make(eiop.Payload, 0)

		return s.Emit(TopicDrain)
	}
	return nil
}

func (s *socket) pingInterval() {
	s.log.Debug("eio.Socket ping interval", zap.String("sid", s.GetSID()))

	if err := s.sendPacket(eiop.PingPacket()); err != nil {
		s.log.Warn("eio.Socket ping interval send", zap.Error(err))
	}
	s.resetPingTimeout(s.cfg.PingTimeout)
}

func (s *socket) pingTimeout() {
	s.log.Debug("eio.Socket ping timeout", zap.String("sid", s.GetSID()))

	_ = s.onClose("ping timeout", "")
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
