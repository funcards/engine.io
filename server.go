package eio

import (
	"errors"
	"github.com/funcards/engine.io-parser/v4"
	"go.uber.org/zap"
	"net/url"
	"sync"
	"time"
)

var _ Server = (*server)(nil)

type Config struct {
	PingInterval  time.Duration `yaml:"ping_interval" env-default:"30s" env:"EIO_PING_INTERVAL"`
	PingTimeout   time.Duration `yaml:"ping_timeout" env-default:"1m" env:"EIO_PING_TIMEOUT"`
	InitialPacket *eiop.Packet  `yaml:"initial_packet"`
}

type HandshakeInterceptor func(query url.Values, headers map[string]string) bool

type Server interface {
	Emitter

	Cfg() Config
	Shutdown()
	HandleWebSocket(ws WebSocket) error
}

type server struct {
	Emitter

	interceptor HandshakeInterceptor
	cfg         Config
	log         *zap.Logger
	clients     *sync.Map
	stop        chan struct{}
}

func NewServer(cfg Config, logger *zap.Logger) *server {
	return NewServerWithInterceptor(cfg, logger, nil)
}

func NewServerWithInterceptor(cfg Config, logger *zap.Logger, interceptor HandshakeInterceptor) *server {
	return &server{
		Emitter:     NewEmitter(),
		cfg:         cfg,
		log:         logger,
		interceptor: interceptor,
		clients:     new(sync.Map),
		stop:        make(chan struct{}, 1),
	}
}

func (s *server) Cfg() Config {
	return s.cfg
}

func (s *server) Shutdown() {
	close(s.stop)
	s.clients.Range(func(key, value any) bool {
		_ = value.(Socket).Close()
		s.clients.Delete(key)
		return true
	})
}

func (s *server) HandleWebSocket(ws WebSocket) error {
	if s.interceptor == nil || s.interceptor(ws.Query(), ws.Headers()) {
		s.handshakeWebSocket(ws)
		return nil
	}

	return errors.New("websocket don't pass interceptor")
}

func (s *server) handshakeWebSocket(ws WebSocket) {
	s.log.Debug("eio.server handshake websocket", zap.Any("query", ws.Query()), zap.Any("headers", ws.Headers()))

	sck := NewSocket(s.cfg, ws, s.log)
	s.clients.Store(sck.SID(), sck)

	go s.run(sck)
	s.Fire(TopicConnection, sck)
}

func (s *server) run(sck Socket) {
	onClose := sck.Once(TopicClose)
	defer sck.Off(TopicClose, onClose)

	select {
	case <-s.stop:
		return
	case <-onClose:
		s.clients.Delete(sck.SID())
		return
	}
}
