package eio

import (
	"context"
	"sync"
)

type CancelMessage interface {
	Cancel()
	Err() error
}

type cancelMessage struct {
	cancel func()
	err    error
}

func (cm *cancelMessage) Cancel() {
	cm.cancel()
}

func (cm *cancelMessage) Err() error {
	return cm.err
}

type ioContext struct {
	context.Context

	cancel  func()
	channel chan CancelMessage
	mu      sync.Mutex
}

func (ctx *ioContext) sendError(err error) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()

	ctx.channel <- &cancelMessage{
		cancel: ctx.cancel,
		err:    err,
	}
}

func WithCancel(parent context.Context) (context.Context, chan CancelMessage) {
	ctx, cancel := context.WithCancel(parent)
	channel := make(chan CancelMessage)
	return &ioContext{
		Context: ctx,
		cancel:  cancel,
		channel: channel,
	}, channel
}

func TryCancel(ctx context.Context, err error) {
	if ioCtx, ok := ctx.(*ioContext); ok {
		ioCtx.sendError(err)
	}
}

func NewCancel() (context.Context, chan CancelMessage) {
	return WithCancel(context.TODO())
}
