package eio

import (
	"go.uber.org/zap"
	"reflect"
	"sync"
)

var _ Emitter = (*emitter)(nil)

const (
	TopicOpen          = "open"
	TopicClose         = "close"
	TopicUpgrading     = "upgrading"
	TopicConnect       = "connect"
	TopicConnection    = "connection"
	TopicDisconnect    = "disconnect"
	TopicDisconnecting = "disconnecting"
	TopicHeartbeat     = "heartbeat"
	TopicError         = "error"
	TopicData          = "data"
	TopicMessage       = "message"
	TopicPacket        = "packet"
	TopicDrain         = "drain"
	TopicFlush         = "flush"
)

type (
	Event struct {
		Topic string
		Args  []any
	}

	Listener func(event *Event) error

	Emitter interface {
		On(topic string, listeners ...Listener)
		Once(topic string, listeners ...Listener)
		OffListeners(topic string, listeners ...Listener)
		Off(topics ...string)
		Emit(topic string, args ...any) error
		Has(topic string) bool
		Listeners(topic string) []Listener
	}

	emitter struct {
		mu        sync.RWMutex
		listeners map[string][]Listener
		log       *zap.Logger
	}
)

func (e Event) Get(index uint, dflt ...any) (r any) {
	for _, n := range dflt {
		r = n
		break
	}
	if len(e.Args) > int(index) {
		r = e.Args[index]
	}
	return
}

func (e Event) Int(index uint, dflt ...int) (r int) {
	for _, n := range dflt {
		r = n
		break
	}
	if len(e.Args) > int(index) {
		if n, ok := e.Args[index].(int); ok {
			r = n
		}
	}
	return
}

func (e Event) String(index uint, dflt ...string) (r string) {
	for _, n := range dflt {
		r = n
		break
	}
	if len(e.Args) > int(index) {
		if c, ok := e.Args[index].(string); ok {
			r = c
		}
	}
	return
}

func (e Event) Err(index uint, dflt ...error) (r error) {
	for _, n := range dflt {
		r = n
		break
	}
	if len(e.Args) > int(index) {
		if c, ok := e.Args[index].(error); ok {
			r = c
		}
	}
	return
}

func once(emitter Emitter, topic string, listeners ...Listener) []Listener {
	data := make([]Listener, len(listeners))

	for i, listener := range listeners {
		var fn Listener
		fn = func(event *Event) error {
			emitter.OffListeners(topic, fn)
			return listener(event)
		}
		data[i] = fn
	}
	return data
}

func NewEmitter(logger *zap.Logger) *emitter {
	return &emitter{
		listeners: make(map[string][]Listener),
		log:       logger,
	}
}

func (e *emitter) On(topic string, listeners ...Listener) {
	if len(listeners) == 0 {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.log.Debug("subscribe", zap.String("topic", topic))

	if !e.has(topic) {
		e.listeners[topic] = listeners
	} else {
		e.listeners[topic] = append(e.listeners[topic], listeners...)
	}
}

func (e *emitter) Once(topic string, listeners ...Listener) {
	e.log.Debug("subscribe once", zap.String("topic", topic))
	e.On(topic, once(e, topic, listeners...)...)
}

func (e *emitter) OffListeners(topic string, listeners ...Listener) {
	e.mu.RLock()
	data, ok := e.listeners[topic]
	e.mu.RUnlock()

	if !ok {
		return
	}

	e.log.Debug("turn off listener for topic", zap.String("topic", topic))

	if len(listeners) == 0 {
		e.mu.Lock()
		delete(e.listeners, topic)
		e.mu.Unlock()
	} else {
		ptrs := make(map[uintptr]bool, len(listeners))
		for _, listener := range listeners {
			ptrs[reflect.ValueOf(listener).Pointer()] = true
		}

		newData := make([]Listener, 0)
		for _, listener := range data {
			if _, ok = ptrs[reflect.ValueOf(listener).Pointer()]; ok {
				continue
			}
			newData = append(newData, listener)
		}

		e.mu.Lock()
		if len(newData) == 0 {
			delete(e.listeners, topic)
		} else {
			e.listeners[topic] = newData
		}
		e.mu.Unlock()
	}
}

func (e *emitter) Off(topics ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.log.Debug("turn off listeners", zap.Strings("topics", topics))

	if len(topics) == 0 {
		e.listeners = make(map[string][]Listener)
	} else {
		for _, topic := range topics {
			if e.has(topic) {
				delete(e.listeners, topic)
			}
		}
	}
}

func (e *emitter) Emit(topic string, args ...any) error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.has(topic) {
		return nil
	}

	e.log.Debug("emit event", zap.String("topic", topic), zap.Any("args", args))

	event := Event{Topic: topic, Args: args}
	for _, listener := range e.listeners[topic] {
		if err := listener(&event); err != nil {
			return err
		}
	}
	return nil
}

func (e *emitter) Has(topic string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.has(topic)
}

func (e *emitter) Listeners(topic string) []Listener {
	e.mu.RLock()
	defer e.mu.RUnlock()

	data := make([]Listener, 0)
	if listeners, ok := e.listeners[topic]; ok {
		for _, listener := range listeners {
			data = append(data, listener)
		}
	}
	return data
}

func (e *emitter) has(topic string) bool {
	_, ok := e.listeners[topic]
	return ok
}
