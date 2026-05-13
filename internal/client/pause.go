package client

import (
	"context"
	"sync"
)

type PauseController struct {
	mu     sync.Mutex
	paused bool
	resume chan struct{}
	queued int
}

func NewPauseController() *PauseController {
	return &PauseController{resume: make(chan struct{})}
}

func (p *PauseController) SetPaused(paused bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.paused == paused {
		return
	}
	p.paused = paused
	if paused {
		p.resume = make(chan struct{})
		return
	}
	close(p.resume)
}

func (p *PauseController) Toggle() bool {
	p.mu.Lock()
	next := !p.paused
	p.mu.Unlock()
	p.SetPaused(next)
	return next
}

func (p *PauseController) IsPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

func (p *PauseController) QueueDepth() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.queued
}

func (p *PauseController) WaitIfPaused(ctx context.Context) (bool, error) {
	p.mu.Lock()
	if !p.paused {
		p.mu.Unlock()
		return false, nil
	}
	resume := p.resume
	p.queued++
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.queued--
		p.mu.Unlock()
	}()

	select {
	case <-resume:
		return true, nil
	case <-ctx.Done():
		return true, ctx.Err()
	}
}
