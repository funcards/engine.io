package eio

import (
	"github.com/olebedev/emitter"
)

var _ Emitter = (*wrapEmitter)(nil)

const (
	TopicOpen          = "open"
	TopicClose         = "close"
	TopicConnection    = "connection"
	TopicDisconnect    = "disconnect"
	TopicDisconnecting = "disconnecting"
	TopicHeartbeat     = "heartbeat"
	TopicData          = "data"
	TopicMessage       = "message"
	TopicPacket        = "packet"
	TopicError         = "error"
)

type Emitter interface {
	Use(pattern string, middlewares ...func(event *emitter.Event))
	On(topic string, middlewares ...func(event *emitter.Event)) <-chan emitter.Event
	Once(topic string, middlewares ...func(event *emitter.Event)) <-chan emitter.Event
	Off(topic string, channels ...<-chan emitter.Event)
	Listeners(topic string) []<-chan emitter.Event
	Topics() []string
	Emit(topic string, args ...any) chan struct{}
	Fire(topic string, args ...any)
}

type wrapEmitter struct {
	*emitter.Emitter
}

func NewEmitter() *wrapEmitter {
	e := emitter.New(1)
	e.Use("*", emitter.Sync)
	return &wrapEmitter{
		Emitter: e,
	}
}

func (e *wrapEmitter) Fire(topic string, args ...any) {
	go e.Emit(topic, args...)
}
