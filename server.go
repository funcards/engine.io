package eio

import (
	"context"
	"errors"
	"github.com/funcards/engine.io-parser/v4"
	"go.uber.org/zap"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type (
	Config struct {
		PingInterval  time.Duration `yaml:"ping_interval" env-default:"30s" env:"EIO_PING_INTERVAL"`
		PingTimeout   time.Duration `yaml:"ping_timeout" env-default:"1m" env:"EIO_PING_TIMEOUT"`
		InitialPacket *eiop.Packet  `yaml:"initial_packet"`
	}

	HandshakeInterceptor interface {
		Intercept(query url.Values, headers map[string]string) bool
	}

	Server interface {
		Emitter

		GetConfig() Config
		Shutdown()
		HandleRequest(ctx context.Context, w http.ResponseWriter, r *http.Request)
		HandleWebSocket(ctx context.Context, webSocket WebSocket)
	}

	server struct {
		Emitter

		cfg         Config
		log         *zap.Logger
		interceptor HandshakeInterceptor
		clients     map[string]Socket
		mu          sync.RWMutex
	}
)

func NewServer(cfg Config, logger *zap.Logger) *server {
	return NewServerWithInterceptor(cfg, logger, nil)
}

func NewServerWithInterceptor(cfg Config, logger *zap.Logger, interceptor HandshakeInterceptor) *server {
	return &server{
		Emitter:     NewEmitter(logger),
		cfg:         cfg,
		log:         logger,
		interceptor: interceptor,
		clients:     make(map[string]Socket),
	}
}

func (s *server) GetConfig() Config {
	return s.cfg
}

func (s *server) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for sid, client := range s.clients {
		if sck, ok := client.(*socket); ok {
			sck.stopTimers()
		}
		delete(s.clients, sid)
	}
}

func (s *server) HandleRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	TryCancel(ctx, errors.New("not support, transport not implemented"))
}

func (s *server) HandleWebSocket(ctx context.Context, webSocket WebSocket) {
	if sid, ok := webSocket.GetQuery()["sid"]; ok {
		s.mu.RLock()
		defer s.mu.RUnlock()

		if sck, ok := s.clients[sid[0]]; ok && sck.CanUpgrade(TransportWebSocket) {
			t := NewWebSocketTransport(webSocket, s.log)
			sck.Upgrade(t)
		} else {
			webSocket.Close(ctx)
		}
		return
	}

	if s.interceptor == nil || s.interceptor.Intercept(webSocket.GetQuery(), webSocket.GetHeaders()) {
		s.handshakeWebSocket(ctx, webSocket)
	} else {
		webSocket.Close(ctx)
	}
}

func (s *server) handshakeWebSocket(ctx context.Context, webSocket WebSocket) {
	sid := NewSID()
	t := NewWebSocketTransport(webSocket, s.log)
	sck := NewSocket(sid, s.cfg, s.log)

	s.log.Debug("handshake websocket", zap.Any("query", webSocket.GetQuery()), zap.Any("headers", webSocket.GetHeaders()))

	sck.Open(ctx, t)

	s.mu.Lock()
	s.clients[sid] = sck
	s.mu.Unlock()

	sck.Once(TopicClose, func(context.Context, *Event) {
		s.mu.Lock()
		delete(s.clients, sid)
		s.mu.Unlock()
	})
	s.Emit(ctx, TopicConnection, sck)
}
