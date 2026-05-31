// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"sync"
)

type runWebJobState string

const (
	runWebJobRunning   runWebJobState = "running"
	runWebJobSucceeded runWebJobState = "succeeded"
	runWebJobFailed    runWebJobState = "failed"
)

type runWebStreamType string

const (
	runWebStreamOutput runWebStreamType = "output"
	runWebStreamStatus runWebStreamType = "status"
)

const (
	defaultRunWebJobBufferLimit   = 1 << 20
	defaultRunWebSubscriberBuffer = 64
	runWebOutputOmittedMessage    = "[older output omitted]\n"
	runWebBrowserClosedMessage    = "Browser tab closed. Press Ctrl-C to quit.\n"
)

type runWebJobConfig struct {
	Stdout           io.Writer
	Notice           io.Writer
	BufferLimit      int
	SubscriberBuffer int
}

type runWebStreamEvent struct {
	ID    int64
	Type  runWebStreamType
	Chunk []byte
	State runWebJobState
	Error string
}

type runWebJobStatus struct {
	ID    string         `json:"jobId"`
	State runWebJobState `json:"state"`
	Error string         `json:"error,omitempty"`
}

type runWebJob struct {
	id               string
	stdout           io.Writer
	notice           io.Writer
	bufferLimit      int
	subscriberBuffer int

	mu          sync.Mutex
	nextID      int64
	state       runWebJobState
	errText     string
	buffer      []runWebStreamEvent
	bufferBytes int
	omitted     bool
	omittedID   int64
	subscribers map[*runWebJobSubscriber]struct{}
	done        chan struct{}
	finished    bool
	finishOnce  sync.Once
	noticeOnce  sync.Once
}

type runWebJobSubscriber struct {
	live      chan runWebStreamEvent
	closeOnce sync.Once
}

func newRunWebJob(id string, cfg runWebJobConfig) *runWebJob {
	stdout := cfg.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	notice := cfg.Notice
	if notice == nil {
		notice = io.Discard
	}
	bufferLimit := cfg.BufferLimit
	if bufferLimit <= 0 {
		bufferLimit = defaultRunWebJobBufferLimit
	}
	subscriberBuffer := cfg.SubscriberBuffer
	if subscriberBuffer <= 0 {
		subscriberBuffer = defaultRunWebSubscriberBuffer
	}
	return &runWebJob{
		id:               id,
		stdout:           stdout,
		notice:           notice,
		bufferLimit:      bufferLimit,
		subscriberBuffer: subscriberBuffer,
		state:            runWebJobRunning,
		subscribers:      make(map[*runWebJobSubscriber]struct{}),
		done:             make(chan struct{}),
	}
}

func (j *runWebJob) Write(p []byte) (int, error) {
	n, err := j.stdout.Write(p)
	if err != nil {
		return n, err
	}
	j.mu.Lock()
	ev := runWebStreamEvent{
		Type:  runWebStreamOutput,
		Chunk: append([]byte(nil), p...),
	}
	ev = j.appendOutputLocked(ev)
	j.broadcastLocked(ev)
	j.mu.Unlock()
	return n, nil
}

func (j *runWebJob) finish(err error) {
	j.finishOnce.Do(func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		if err != nil {
			j.state = runWebJobFailed
			j.errText = err.Error()
			if !j.retainedOutputContainsLocked(j.errText) {
				ev := runWebStreamEvent{
					Type:  runWebStreamOutput,
					Chunk: []byte("Error: " + j.errText + "\n"),
				}
				ev = j.appendOutputLocked(ev)
				j.broadcastLocked(ev)
			}
		} else {
			j.state = runWebJobSucceeded
		}
		status := runWebStreamEvent{
			ID:    j.nextEventIDLocked(),
			Type:  runWebStreamStatus,
			State: j.state,
			Error: j.errText,
		}
		j.buffer = append(j.buffer, status)
		j.broadcastLocked(status)
		j.finished = true
		close(j.done)
		for sub := range j.subscribers {
			delete(j.subscribers, sub)
			sub.close()
		}
	})
}

func (j *runWebJob) status() runWebJobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return runWebJobStatus{ID: j.id, State: j.state, Error: j.errText}
}

func (j *runWebJob) subscribe(ctx context.Context, lastID int64) (<-chan runWebStreamEvent, <-chan struct{}) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan runWebStreamEvent, j.subscriberBuffer)
	done := make(chan struct{})
	sub := &runWebJobSubscriber{live: make(chan runWebStreamEvent, j.subscriberBuffer)}

	j.mu.Lock()
	replay := j.replayEventsLocked(lastID)
	finished := j.finished
	if !finished {
		j.subscribers[sub] = struct{}{}
	}
	j.mu.Unlock()

	go j.runSubscription(ctx, out, done, sub, replay, finished)
	return out, done
}

func (j *runWebJob) browserClosed() {
	j.noticeOnce.Do(func() {
		_, _ = io.WriteString(j.notice, runWebBrowserClosedMessage)
	})
}

func (ev runWebStreamEvent) ssePayload() (eventName string, data []byte, err error) {
	switch ev.Type {
	case runWebStreamOutput:
		data, err = json.Marshal(struct {
			Encoding string `json:"encoding"`
			Chunk    string `json:"chunk"`
		}{
			Encoding: "base64",
			Chunk:    base64.StdEncoding.EncodeToString(ev.Chunk),
		})
	case runWebStreamStatus:
		data, err = json.Marshal(struct {
			State runWebJobState `json:"state"`
			Error string         `json:"error,omitempty"`
		}{
			State: ev.State,
			Error: ev.Error,
		})
	default:
		data, err = json.Marshal(map[string]any{})
	}
	return string(ev.Type), data, err
}

func (j *runWebJob) nextEventIDLocked() int64 {
	j.nextID++
	return j.nextID
}

func (j *runWebJob) appendOutputLocked(ev runWebStreamEvent) runWebStreamEvent {
	if j.bufferLimit > 0 {
		retainedBeforeAppend := j.bufferLimit - len(ev.Chunk)
		if retainedBeforeAppend < 0 {
			retainedBeforeAppend = 0
		}
		j.trimOutputBufferToLimitLocked(retainedBeforeAppend)
	}
	if ev.ID == 0 {
		ev.ID = j.nextEventIDLocked()
	}
	j.buffer = append(j.buffer, ev)
	j.bufferBytes += len(ev.Chunk)
	j.trimOutputBufferLocked()
	return ev
}

func (j *runWebJob) trimOutputBufferLocked() {
	j.trimOutputBufferToLimitLocked(j.bufferLimit)
}

func (j *runWebJob) trimOutputBufferToLimitLocked(limit int) {
	if j.bufferLimit <= 0 {
		return
	}
	for j.bufferBytes > limit {
		idx := -1
		for i, ev := range j.buffer {
			if ev.Type == runWebStreamOutput {
				idx = i
				break
			}
		}
		if idx == -1 {
			j.bufferBytes = 0
			return
		}
		chunkLen := len(j.buffer[idx].Chunk)
		j.omitted = true
		j.omittedID = j.buffer[idx].ID
		j.bufferBytes -= chunkLen
		j.buffer = append(j.buffer[:idx], j.buffer[idx+1:]...)
	}
}

func (j *runWebJob) broadcastLocked(ev runWebStreamEvent) {
	for sub := range j.subscribers {
		select {
		case sub.live <- ev:
		default:
		}
	}
}

func (j *runWebJob) replayEventsLocked(lastID int64) []runWebStreamEvent {
	replay := make([]runWebStreamEvent, 0, len(j.buffer)+1)
	if j.omitted && j.omittedID > lastID {
		replay = append(replay, runWebStreamEvent{
			ID:    j.omittedID,
			Type:  runWebStreamOutput,
			Chunk: []byte(runWebOutputOmittedMessage),
		})
	}
	for _, ev := range j.buffer {
		if ev.ID > lastID {
			replay = append(replay, cloneRunWebStreamEvent(ev))
		}
	}
	return replay
}

func (j *runWebJob) runSubscription(ctx context.Context, out chan<- runWebStreamEvent, done chan<- struct{}, sub *runWebJobSubscriber, replay []runWebStreamEvent, finished bool) {
	defer close(done)
	defer close(out)
	defer j.unsubscribe(sub)

	if !sendRunWebReplay(ctx, out, replay) || finished {
		return
	}
	forwardRunWebLiveEvents(ctx, out, sub.live)
}

func sendRunWebReplay(ctx context.Context, out chan<- runWebStreamEvent, replay []runWebStreamEvent) bool {
	for _, ev := range replay {
		select {
		case out <- ev:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

func forwardRunWebLiveEvents(ctx context.Context, out chan<- runWebStreamEvent, live <-chan runWebStreamEvent) {
	for {
		select {
		case ev, ok := <-live:
			if !ok {
				return
			}
			if !sendRunWebLiveEvent(ctx, out, ev) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func sendRunWebLiveEvent(ctx context.Context, out chan<- runWebStreamEvent, ev runWebStreamEvent) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func (j *runWebJob) retainedOutputContainsLocked(s string) bool {
	if s == "" {
		return true
	}
	for _, ev := range j.buffer {
		if ev.Type == runWebStreamOutput && strings.Contains(string(ev.Chunk), s) {
			return true
		}
	}
	return false
}

func (j *runWebJob) unsubscribe(sub *runWebJobSubscriber) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.subscribers[sub]; !ok {
		return
	}
	delete(j.subscribers, sub)
	sub.close()
}

func (sub *runWebJobSubscriber) close() {
	sub.closeOnce.Do(func() {
		close(sub.live)
	})
}

func cloneRunWebStreamEvent(ev runWebStreamEvent) runWebStreamEvent {
	ev.Chunk = append([]byte(nil), ev.Chunk...)
	return ev
}
