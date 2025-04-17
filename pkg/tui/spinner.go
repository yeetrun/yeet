// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

var DefaultFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type Spinner struct {
	out        io.Writer
	frames     []string
	interval   time.Duration
	hideCursor bool
	color      Colorizer
	frameColor string

	mu      sync.Mutex
	msg     string
	idx     int
	running bool
	stopCh  chan struct{}
	doneCh  chan struct{}
}

type SpinnerOption func(*Spinner)

func WithFrames(frames []string) SpinnerOption {
	return func(s *Spinner) {
		if len(frames) > 0 {
			s.frames = frames
		}
	}
}

func WithInterval(d time.Duration) SpinnerOption {
	return func(s *Spinner) {
		if d > 0 {
			s.interval = d
		}
	}
}

func WithHideCursor(hide bool) SpinnerOption {
	return func(s *Spinner) {
		s.hideCursor = hide
	}
}

func WithColor(colorizer Colorizer, frameColor string) SpinnerOption {
	return func(s *Spinner) {
		s.color = colorizer
		s.frameColor = frameColor
	}
}

func NewSpinner(out io.Writer, opts ...SpinnerOption) *Spinner {
	s := &Spinner{
		out:      out,
		frames:   DefaultFrames,
		interval: 120 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Spinner) Start(msg string) {
	s.mu.Lock()
	if s.running {
		s.msg = msg
		s.mu.Unlock()
		return
	}
	s.running = true
	s.msg = msg
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	s.mu.Unlock()

	if s.hideCursor {
		fmt.Fprint(s.out, "\x1b[?25l")
	}
	s.renderFrame(0, msg)
	go s.loop()
}

func (s *Spinner) Update(msg string) {
	s.mu.Lock()
	s.msg = msg
	running := s.running
	frameIdx := s.idx
	s.mu.Unlock()
	if !running {
		return
	}
	s.renderFrame(frameIdx, msg)
}

func (s *Spinner) Stop(clear bool) {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	stopCh := s.stopCh
	doneCh := s.doneCh
	s.running = false
	s.mu.Unlock()

	close(stopCh)
	<-doneCh

	if clear {
		s.clearLine()
	} else {
		fmt.Fprintln(s.out)
	}
	if s.hideCursor {
		fmt.Fprint(s.out, "\x1b[?25h")
	}
}

func (s *Spinner) loop() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.tick()
		case <-s.stopCh:
			close(s.doneCh)
			return
		}
	}
}

func (s *Spinner) tick() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	if len(s.frames) == 0 {
		s.mu.Unlock()
		return
	}
	s.idx = (s.idx + 1) % len(s.frames)
	frameIdx := s.idx
	msg := s.msg
	s.mu.Unlock()

	s.renderFrame(frameIdx, msg)
}

func (s *Spinner) renderFrame(idx int, msg string) {
	if len(s.frames) == 0 {
		return
	}
	frame := s.frames[idx%len(s.frames)]
	line := s.color.Wrap(s.frameColor, frame)
	if msg != "" {
		line = line + " " + msg
	}
	fmt.Fprintf(s.out, "\r\033[K%s", line)
}

func (s *Spinner) clearLine() {
	fmt.Fprint(s.out, "\r\033[K")
}
