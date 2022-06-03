package eio

import (
	"errors"
	"fmt"
	"github.com/funcards/engine.io-parser/v4"
	"github.com/olebedev/emitter"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"io"
	"net/url"
	"sync"
	"time"
)

var _ Socket = (*socket)(nil)

const HandshakeJSON = "{\"sid\": \"%s\", \"upgrades\": [], \"pingInterval\": %d, \"pingTimeout\": %d}"

type State byte

const (
	Opening State = iota
	Open
	Closing
	Closed
)

type Socket interface {
	io.Closer
	Emitter

	SID() string
	State() State
	Query() url.Values
	Headers() map[string]string
	Send(packet eiop.Packet)
}

type socket struct {
	Emitter

	cfg               Config
	ws                WebSocket
	stop              chan struct{}
	sid               string
	state             *atomic.Uint32
	log               *zap.Logger
	mu                sync.Mutex
	pingFuture        *time.Timer
	pingTimeoutFuture *time.Timer
}

func NewSocket(cfg Config, ws WebSocket, logger *zap.Logger) *socket {
	s := &socket{
		Emitter: NewEmitter(),
		sid:     NewSID(),
		state:   atomic.NewUint32(uint32(Opening)),
		stop:    make(chan struct{}, 1),
		cfg:     cfg,
		ws:      ws,
		log:     logger,
	}
	s.Emitter.Use("*", func(event *emitter.Event) {
		event.Args = append([]any{s}, event.Args...)
	})
	s.open()

	return s
}

func (s *socket) SID() string {
	return s.sid
}

func (s *socket) State() State {
	return State(s.state.Load())
}

func (s *socket) Query() url.Values {
	return s.ws.Query()
}

func (s *socket) Headers() map[string]string {
	return s.ws.Headers()
}

func (s *socket) Send(packet eiop.Packet) {
	s.sendPacket(packet)
}

func (s *socket) Close() error {
	if s.State() == Open {
		s.state.Store(uint32(Closing))
		close(s.stop)
		s.stopTimers()
		return s.ws.Close()
	}
	return nil
}

func (s *socket) open() {
	s.state.Store(uint32(Open))
	go s.run()

	data := fmt.Sprintf(HandshakeJSON, s.sid, s.cfg.PingInterval.Milliseconds(), s.cfg.PingTimeout.Milliseconds())
	packet := eiop.OpenPacket(data)
	s.sendPacket(packet)

	if s.cfg.InitialPacket != nil {
		s.sendPacket(*s.cfg.InitialPacket)
	}

	s.schedulePing()
	s.Fire(TopicOpen)
}

func (s *socket) run() {
	onError := s.ws.Once(TopicError)
	onClose := s.ws.Once(TopicClose)
	onPacket := s.ws.On(TopicPacket)

	defer func(ws WebSocket) {
		ws.Off(TopicPacket, onPacket)
		ws.Off(TopicClose, onClose)
		ws.Off(TopicError, onError)
	}(s.ws)

	for {
		select {
		case <-s.stop:
			return
		case event := <-onClose:
			s.onClose(event.String(0), event.String(1))
			return
		case event := <-onError:
			s.onError(event.String(0), event.Args[1].(error))
			return
		case event := <-onPacket:
			s.onPacket(event.Args[0].(eiop.Packet))
		}
	}
}

func (s *socket) onError(msg string, err error) {
	s.onClose(msg, err.Error())
}

func (s *socket) onClose(reason, description string) {
	if s.State() != Closed {
		_ = s.Close()
		s.state.Store(uint32(Closed))
		s.log.Debug("eio.Socket closed", zap.String("reason", reason), zap.String("description", description))
		s.Fire(TopicClose, reason, description)
	}
}

func (s *socket) onPacket(packet eiop.Packet) {
	if s.State() != Open {
		return
	}

	s.resetPingTimeout(s.cfg.PingTimeout + s.cfg.PingInterval)
	s.Fire(TopicPacket, packet)

	switch packet.Type {
	case eiop.Ping:
		s.log.Error("received ping")
		s.onError("eio.socket received ping", errors.New("received ping from websocket"))
	case eiop.Pong:
		s.log.Debug("received pong")
		s.schedulePing()
		s.Fire(TopicHeartbeat)
	case eiop.Error:
		s.onError("parse error", errors.New("received packet is not valid"))
	case eiop.Message:
		s.Fire(TopicData, packet.Data)
	}
}

func (s *socket) sendPacket(packet eiop.Packet) {
	s.log.Debug("eio.socket send packet", zap.Any("packet", packet))

	switch s.State() {
	case Closing, Closed:
		return
	}

	s.ws.Write(packet)
}

func (s *socket) pingInterval() {
	s.log.Debug("eio.Socket ping interval", zap.String("sid", s.sid))

	s.sendPacket(eiop.PingPacket())
	s.resetPingTimeout(s.cfg.PingTimeout)
}

func (s *socket) pingTimeout() {
	s.log.Debug("eio.Socket ping timeout", zap.String("sid", s.sid))
	s.onError("ping timeout", errors.New("ping timeout, pong not received"))
}

func (s *socket) schedulePing() {
	s.log.Debug("eio.Socket schedule ping", zap.String("sid", s.sid))

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
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pingFuture != nil {
		if !s.pingFuture.Stop() {
			<-s.pingFuture.C
		}
	}
	if s.pingTimeoutFuture != nil {
		if !s.pingTimeoutFuture.Stop() {
			<-s.pingTimeoutFuture.C
		}
	}
}
