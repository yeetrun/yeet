// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

type runWebJobState string

const (
	runWebJobRunning   runWebJobState = "running"
	runWebJobSucceeded runWebJobState = "succeeded"
	runWebJobFailed    runWebJobState = "failed"
)

type runWebStreamType string

const (
	runWebStreamTerminal runWebStreamType = "terminal"
	runWebStreamOutput   runWebStreamType = "output"
	runWebStreamResize   runWebStreamType = "resize"
	runWebStreamWarning  runWebStreamType = "warning"
	runWebStreamStatus   runWebStreamType = "status"
)

const (
	runWebTerminalScrollback      = 1000
	runWebOutputTailLimit         = 64 << 10
	runWebSubscriberCloseDebounce = 750 * time.Millisecond
	runWebBrowserClosedMessage    = "Browser tab closed. Press Ctrl-C to quit.\n"
	runWebJournalDegradedMessage  = "Browser terminal replay stopped because its local journal became unavailable."
	runWebDefaultTerminalCols     = 80
	runWebDefaultTerminalRows     = 24
)

type runWebJobConfig struct {
	Stdout       io.Writer
	Notice       io.Writer
	JournalDir   string
	JournalLimit int64
	Profile      runWebTerminalProfile
	Resize       <-chan catchrpc.Resize
	NewJournal   func(string, int64) (runWebEventJournal, error)
}

type runWebTerminalProfile struct {
	TTY        bool   `json:"tty"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
	Term       string `json:"term,omitempty"`
	Scrollback int    `json:"scrollback"`
}

type runWebStreamEvent struct {
	ID      int64
	Type    runWebStreamType
	Chunk   []byte
	Profile runWebTerminalProfile
	Cols    int
	Rows    int
	Warning string
	State   runWebJobState
	Error   string
}

type runWebJobStatus struct {
	ID       string         `json:"jobId"`
	State    runWebJobState `json:"state"`
	Error    string         `json:"error,omitempty"`
	Degraded bool           `json:"degraded,omitempty"`
}

type runWebJob struct {
	id      string
	stdout  io.Writer
	notice  io.Writer
	journal runWebEventJournal
	resize  <-chan catchrpc.Resize

	mu          sync.Mutex
	state       runWebJobState
	errText     string
	degraded    bool
	outputTail  []byte
	subscribers int
	finished    bool
	closed      bool

	done       chan struct{}
	ack        chan struct{}
	resizeStop chan struct{}
	resizeDone chan struct{}

	finishOnce sync.Once
	ackOnce    sync.Once
	stopOnce   sync.Once
	closeOnce  sync.Once
	noticeOnce sync.Once
	closeErr   error
}

func newRunWebJob(id string, cfg runWebJobConfig) (*runWebJob, error) {
	stdout := cfg.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	notice := cfg.Notice
	if notice == nil {
		notice = io.Discard
	}
	newJournal := cfg.NewJournal
	if newJournal == nil {
		newJournal = func(dir string, limit int64) (runWebEventJournal, error) {
			return newRunWebJournal(dir, limit)
		}
	}
	profile := normalizeRunWebTerminalProfile(cfg.Profile)
	journal, err := newJournal(cfg.JournalDir, cfg.JournalLimit)
	if err != nil {
		return nil, err
	}
	if _, err := journal.append(runWebStreamEvent{
		Type:    runWebStreamTerminal,
		Profile: profile,
	}, true); err != nil {
		return nil, errors.Join(err, journal.close())
	}

	job := &runWebJob{
		id:         id,
		stdout:     stdout,
		notice:     notice,
		journal:    journal,
		resize:     cfg.Resize,
		state:      runWebJobRunning,
		done:       make(chan struct{}),
		ack:        make(chan struct{}),
		resizeStop: make(chan struct{}),
		resizeDone: make(chan struct{}),
	}
	go job.observeResize()
	return job, nil
}

func normalizeRunWebTerminalProfile(profile runWebTerminalProfile) runWebTerminalProfile {
	if profile.Cols <= 0 {
		profile.Cols = runWebDefaultTerminalCols
	}
	if profile.Rows <= 0 {
		profile.Rows = runWebDefaultTerminalRows
	}
	if profile.Scrollback <= 0 {
		profile.Scrollback = runWebTerminalScrollback
	}
	if !profile.TTY {
		profile.Term = ""
	}
	return profile
}

func (j *runWebJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished || j.closed {
		return 0, io.ErrClosedPipe
	}

	n, terminalErr := j.stdout.Write(p)
	if n > len(p) {
		n = len(p)
	}
	if n > 0 {
		j.rememberOutputLocked(p[:n])
		if !j.closed && !j.degraded {
			if _, err := j.journal.append(runWebStreamEvent{
				Type:  runWebStreamOutput,
				Chunk: p[:n],
			}, false); err != nil {
				j.degradeLocked()
			}
		}
	}
	if terminalErr != nil {
		return n, terminalErr
	}
	if n != len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

func (j *runWebJob) finish(err error) {
	j.finishOnce.Do(func() {
		j.stopResize()

		j.mu.Lock()
		defer j.mu.Unlock()
		if err != nil {
			j.state = runWebJobFailed
			j.errText = err.Error()
			if !bytes.Contains(j.outputTail, []byte(j.errText)) {
				j.writeFailureLineLocked([]byte("Error: " + j.errText + "\n"))
			}
		} else {
			j.state = runWebJobSucceeded
		}
		if !j.closed {
			if _, appendErr := j.journal.append(runWebStreamEvent{
				Type:  runWebStreamStatus,
				State: j.state,
				Error: j.errText,
			}, true); appendErr != nil {
				j.degradeLocked()
			}
		}
		j.finished = true
		close(j.done)
	})
}

func (j *runWebJob) writeFailureLineLocked(line []byte) {
	n, _ := j.stdout.Write(line)
	if n > len(line) {
		n = len(line)
	}
	if n <= 0 {
		return
	}
	j.rememberOutputLocked(line[:n])
	if j.closed || j.degraded {
		return
	}
	if _, err := j.journal.append(runWebStreamEvent{
		Type:  runWebStreamOutput,
		Chunk: line[:n],
	}, false); err != nil {
		j.degradeLocked()
	}
}

func (j *runWebJob) rememberOutputLocked(p []byte) {
	if len(p) >= runWebOutputTailLimit {
		j.outputTail = append(j.outputTail[:0], p[len(p)-runWebOutputTailLimit:]...)
		return
	}
	excess := len(j.outputTail) + len(p) - runWebOutputTailLimit
	if excess > 0 {
		copy(j.outputTail, j.outputTail[excess:])
		j.outputTail = j.outputTail[:len(j.outputTail)-excess]
	}
	j.outputTail = append(j.outputTail, p...)
}

func (j *runWebJob) degradeLocked() {
	if j.degraded {
		return
	}
	j.degraded = true
	_, _ = j.journal.append(runWebStreamEvent{
		Type:    runWebStreamWarning,
		Warning: runWebJournalDegradedMessage,
	}, true)
}

func (j *runWebJob) observeResize() {
	defer close(j.resizeDone)
	if j.resize == nil {
		return
	}
	for {
		select {
		case <-j.resizeStop:
			return
		case size, ok := <-j.resize:
			if !ok {
				return
			}
			j.recordResize(size)
		}
	}
}

func (j *runWebJob) recordResize(size catchrpc.Resize) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished || j.closed || j.degraded {
		return
	}
	if _, err := j.journal.append(runWebStreamEvent{
		Type: runWebStreamResize,
		Cols: size.Cols,
		Rows: size.Rows,
	}, true); err != nil {
		j.degradeLocked()
	}
}

func (j *runWebJob) stopResize() {
	j.stopOnce.Do(func() {
		close(j.resizeStop)
	})
	<-j.resizeDone
}

func (j *runWebJob) status() runWebJobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return runWebJobStatus{
		ID:       j.id,
		State:    j.state,
		Error:    j.errText,
		Degraded: j.degraded,
	}
}

func (j *runWebJob) prepareAcknowledgement() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.finished || j.state != runWebJobSucceeded || j.degraded {
		return false
	}
	return true
}

func (j *runWebJob) releaseAcknowledgement() {
	j.ackOnce.Do(func() {
		close(j.ack)
	})
}

func (j *runWebJob) acknowledged() <-chan struct{} {
	return j.ack
}

func (j *runWebJob) validateCursor(cursor int64) error {
	_, _, _, _, err := j.journal.readAfter(cursor, 1)
	return err
}

func (j *runWebJob) subscribe(ctx context.Context, lastID int64) (<-chan runWebStreamEvent, <-chan struct{}) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan runWebStreamEvent)
	done := make(chan struct{})

	j.mu.Lock()
	counted := !j.finished && !j.closed
	if counted {
		j.subscribers++
	}
	j.mu.Unlock()

	go j.runSubscription(ctx, lastID, out, done, counted)
	return out, done
}

func (j *runWebJob) runSubscription(ctx context.Context, cursor int64, out chan<- runWebStreamEvent, done chan<- struct{}, counted bool) {
	defer close(done)
	defer close(out)
	defer j.unsubscribe(counted)

	for {
		events, next, wake, sealed, err := j.journal.readAfter(cursor, runWebJournalReadBatch)
		if err != nil {
			return
		}
		var delivered bool
		cursor, delivered = deliverRunWebJournalEvents(ctx, out, cursor, events)
		if !delivered {
			return
		}
		if next > cursor {
			cursor = next
		}
		if len(events) > 0 {
			continue
		}
		if sealed || j.isFinished() {
			return
		}
		if !waitForRunWebJournal(ctx, wake, j.done) {
			return
		}
	}
}

func deliverRunWebJournalEvents(ctx context.Context, out chan<- runWebStreamEvent, cursor int64, events []runWebStreamEvent) (int64, bool) {
	for _, ev := range events {
		select {
		case out <- ev:
			cursor = ev.ID
		case <-ctx.Done():
			return cursor, false
		}
	}
	return cursor, true
}

func waitForRunWebJournal(ctx context.Context, wake, done <-chan struct{}) bool {
	select {
	case <-wake:
		return true
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (j *runWebJob) isFinished() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.finished
}

func (j *runWebJob) unsubscribe(counted bool) {
	if !counted {
		return
	}
	j.mu.Lock()
	if j.subscribers > 0 {
		j.subscribers--
	}
	shouldCheck := j.subscribers == 0
	j.mu.Unlock()
	if shouldCheck {
		j.noteSubscriberClosed()
	}
}

func (j *runWebJob) noteSubscriberClosed() {
	go func() {
		timer := time.NewTimer(runWebSubscriberCloseDebounce)
		defer timer.Stop()
		<-timer.C

		j.mu.Lock()
		shouldNotice := j.subscribers == 0 && !j.finished
		j.mu.Unlock()
		if shouldNotice {
			j.browserClosed()
		}
	}()
}

func (j *runWebJob) close() error {
	j.closeOnce.Do(func() {
		j.stopResize()
		j.mu.Lock()
		j.closed = true
		j.closeErr = j.journal.close()
		j.mu.Unlock()
	})
	return j.closeErr
}

func (j *runWebJob) browserClosed() {
	j.noticeOnce.Do(func() {
		_, _ = io.WriteString(j.notice, runWebBrowserClosedMessage)
	})
}

func (j *runWebJob) terminalFile() *os.File {
	f, ok := j.stdout.(*os.File)
	if !ok {
		return nil
	}
	return f
}

func (ev runWebStreamEvent) ssePayload() (eventName string, data []byte, err error) {
	switch ev.Type {
	case runWebStreamTerminal:
		data, err = json.Marshal(ev.Profile)
	case runWebStreamOutput:
		data, err = json.Marshal(struct {
			Encoding string `json:"encoding"`
			Chunk    string `json:"chunk"`
		}{
			Encoding: "base64",
			Chunk:    base64.StdEncoding.EncodeToString(ev.Chunk),
		})
	case runWebStreamResize:
		data, err = json.Marshal(struct {
			Cols int `json:"cols"`
			Rows int `json:"rows"`
		}{
			Cols: ev.Cols,
			Rows: ev.Rows,
		})
	case runWebStreamWarning:
		data, err = json.Marshal(struct {
			Message string `json:"message"`
		}{
			Message: ev.Warning,
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
