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
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

func TestRunWebJobWritesTerminalOutputAndReplaysProfileOutputAndStatus(t *testing.T) {
	var terminal bytes.Buffer
	profile := runWebTerminalProfile{
		TTY: true, Cols: 100, Rows: 30, Term: "xterm-256color", Scrollback: runWebTerminalScrollback,
	}
	job := mustNewRunWebJob(t, runWebJobConfig{Stdout: &terminal, Profile: profile})

	if n, err := job.Write([]byte("deploying\n")); err != nil || n != len("deploying\n") {
		t.Fatalf("Write = %d, %v; want full write and nil error", n, err)
	}
	job.finish(nil)

	if terminal.String() != "deploying\n" {
		t.Fatalf("terminal output = %q, want deploying", terminal.String())
	}
	events := collectRunWebJobEvents(t, job, 0)
	if len(events) != 3 {
		t.Fatalf("events len = %d, want profile, output, status: %#v", len(events), events)
	}
	if events[0].Type != runWebStreamTerminal || events[0].Profile != profile {
		t.Fatalf("first event = %#v, want terminal profile %#v", events[0], profile)
	}
	if events[1].Type != runWebStreamOutput || string(events[1].Chunk) != "deploying\n" {
		t.Fatalf("second event = %#v, want output chunk", events[1])
	}
	if events[2].Type != runWebStreamStatus || events[2].State != runWebJobSucceeded {
		t.Fatalf("third event = %#v, want succeeded status", events[2])
	}
}

func TestRunWebJobWriteErrorDoesNotJournalOutput(t *testing.T) {
	job := mustNewRunWebJob(t, runWebJobConfig{
		Stdout: errRunWebJobWriter{err: errors.New("terminal failed")},
	})

	n, err := job.Write([]byte("deploying\n"))
	if err == nil || err.Error() != "terminal failed" {
		t.Fatalf("Write error = %v, want terminal failed", err)
	}
	if n != 0 {
		t.Fatalf("Write n = %d, want 0", n)
	}
	job.finish(nil)

	events := collectRunWebJobEvents(t, job, 0)
	if len(events) != 2 || events[0].Type != runWebStreamTerminal || events[1].Type != runWebStreamStatus {
		t.Fatalf("events = %#v, want terminal profile and succeeded status only", events)
	}
}

func TestRunWebJobShortTerminalWriteJournalsExactPrefixAndReturnsShortWrite(t *testing.T) {
	var terminal bytes.Buffer
	job := mustNewRunWebJob(t, runWebJobConfig{
		Stdout: runWebShortWriter{writer: &terminal, n: 3},
	})

	n, err := job.Write([]byte{0xff, '\r', '\n', 0x00, 'x'})
	if n != 3 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Write = %d, %v; want 3, io.ErrShortWrite", n, err)
	}
	job.finish(nil)

	events := collectRunWebJobEvents(t, job, 0)
	if len(events) != 3 {
		t.Fatalf("events = %#v, want terminal, exact output prefix, status", events)
	}
	want := []byte{0xff, '\r', '\n'}
	if !bytes.Equal(events[1].Chunk, want) || !bytes.Equal(terminal.Bytes(), want) {
		t.Fatalf("browser=%v terminal=%v, want %v", events[1].Chunk, terminal.Bytes(), want)
	}
}

func TestRunWebJobNilStdoutAndNoticeAreSafe(t *testing.T) {
	job := mustNewRunWebJob(t, runWebJobConfig{})

	if n, err := job.Write([]byte("deploying\n")); err != nil || n != len("deploying\n") {
		t.Fatalf("Write with nil stdout = %d, %v; want full write and nil error", n, err)
	}
	job.finish(nil)
	job.browserClosed()

	status := job.status()
	if status.State != runWebJobSucceeded || status.Error != "" || status.Degraded {
		t.Fatalf("status = %#v, want non-degraded success", status)
	}
}

func TestRunWebJobAcknowledgementEligibilityAndIdempotency(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*runWebJob)
		want    bool
	}{
		{name: "running", want: false},
		{
			name: "failed",
			prepare: func(job *runWebJob) {
				job.finish(errors.New("deploy failed"))
			},
			want: false,
		},
		{
			name: "degraded success",
			prepare: func(job *runWebJob) {
				job.mu.Lock()
				job.degraded = true
				job.mu.Unlock()
				job.finish(nil)
			},
			want: false,
		},
		{
			name: "success",
			prepare: func(job *runWebJob) {
				job.finish(nil)
			},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job := mustNewRunWebJob(t, runWebJobConfig{})
			if tc.prepare != nil {
				tc.prepare(job)
			}

			if got := job.prepareAcknowledgement(); got != tc.want {
				t.Fatalf("prepareAcknowledgement = %v, want %v", got, tc.want)
			}
			if got := job.prepareAcknowledgement(); got != tc.want {
				t.Fatalf("duplicate prepareAcknowledgement = %v, want %v", got, tc.want)
			}
			if tc.want {
				job.releaseAcknowledgement()
				job.releaseAcknowledgement()
			}

			select {
			case <-job.acknowledged():
				if !tc.want {
					t.Fatal("acknowledged channel closed for ineligible job")
				}
			default:
				if tc.want {
					t.Fatal("acknowledged channel remained open for eligible job")
				}
			}
		})
	}
}

func TestRunWebJobConstructorReturnsJournalCreationError(t *testing.T) {
	want := errors.New("journal creation failed")
	job, err := newRunWebJob("job-a", runWebJobConfig{
		NewJournal: func(string, int64) (runWebEventJournal, error) {
			return nil, want
		},
	})
	if job != nil || !errors.Is(err, want) {
		t.Fatalf("newRunWebJob = %#v, %v; want nil, creation error", job, err)
	}
}

func TestRunWebJobSubscribeReplaysOnlyEventsAfterLastID(t *testing.T) {
	job := mustNewRunWebJob(t, runWebJobConfig{})
	if _, err := job.Write([]byte("first\n")); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if _, err := job.Write([]byte("second\n")); err != nil {
		t.Fatalf("write second: %v", err)
	}
	job.finish(nil)

	allEvents := collectRunWebJobEvents(t, job, 0)
	if len(allEvents) != 3 {
		t.Fatalf("all events len = %d, want terminal, combined output, status: %#v", len(allEvents), allEvents)
	}
	events := collectRunWebJobEvents(t, job, allEvents[0].ID)
	if len(events) != 2 {
		t.Fatalf("filtered events len = %d, want output and status: %#v", len(events), events)
	}
	if string(events[0].Chunk) != "first\nsecond\n" || events[1].Type != runWebStreamStatus {
		t.Fatalf("filtered events = %#v, want exact output then status", events)
	}
}

func TestRunWebJobLiveSubscriberReceivesOutputAndClosesAfterFinish(t *testing.T) {
	job := mustNewRunWebJob(t, runWebJobConfig{})
	ch, done := job.subscribe(context.Background(), 0)

	if ev := receiveRunWebJobEvent(t, ch); ev.Type != runWebStreamTerminal {
		t.Fatalf("first event = %#v, want terminal profile", ev)
	}
	if _, err := job.Write([]byte("live\n")); err != nil {
		t.Fatalf("write live: %v", err)
	}
	ev := receiveRunWebJobEvent(t, ch)
	if ev.Type != runWebStreamOutput || string(ev.Chunk) != "live\n" {
		t.Fatalf("live event = %#v, want live output", ev)
	}

	job.finish(nil)
	ev = receiveRunWebJobEvent(t, ch)
	if ev.Type != runWebStreamStatus || ev.State != runWebJobSucceeded {
		t.Fatalf("finish event = %#v, want succeeded status", ev)
	}
	assertRunWebJobSubscriptionClosed(t, ch, done)
}

func TestRunWebJobSubscribeCancelClosesDoneAndChannel(t *testing.T) {
	job := mustNewRunWebJob(t, runWebJobConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	ch, done := job.subscribe(ctx, 0)
	cancel()
	assertRunWebJobSubscriptionClosed(t, ch, done)
}

func TestRunWebJobSlowSubscriberReplaysEveryByteWithoutBlockingWriter(t *testing.T) {
	var terminal bytes.Buffer
	job := mustNewRunWebJob(t, runWebJobConfig{
		Stdout: &terminal,
		Profile: runWebTerminalProfile{
			TTY: true, Cols: 100, Rows: 30, Term: "xterm-256color", Scrollback: runWebTerminalScrollback,
		},
	})

	ch, done := job.subscribe(context.Background(), 0)
	wrote := make(chan error, 1)
	go func() {
		for i := 0; i < 512; i++ {
			if _, err := job.Write([]byte("x")); err != nil {
				wrote <- err
				return
			}
		}
		wrote <- nil
	}()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer blocked behind slow subscriber")
	}
	job.finish(nil)

	var output []byte
	for ev := range ch {
		if ev.Type == runWebStreamOutput {
			output = append(output, ev.Chunk...)
		}
	}
	<-done
	if len(output) != 512 || terminal.Len() != 512 {
		t.Fatalf("browser=%d terminal=%d", len(output), terminal.Len())
	}
}

func TestRunWebJobFinishWaitsForInFlightTerminalWriteAndKeepsStatusLast(t *testing.T) {
	writer := &runWebGatedWriter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	job := mustNewRunWebJob(t, runWebJobConfig{Stdout: writer})
	writeDone := make(chan error, 1)
	go func() {
		_, err := job.Write([]byte("in flight"))
		writeDone <- err
	}()
	<-writer.entered

	finishDone := make(chan struct{})
	go func() {
		job.finish(nil)
		close(finishDone)
	}()
	select {
	case <-finishDone:
		t.Fatal("finish completed while a terminal write was still in flight")
	case <-time.After(25 * time.Millisecond):
	}
	close(writer.release)
	if err := <-writeDone; err != nil {
		t.Fatalf("Write: %v", err)
	}
	<-finishDone

	events := collectRunWebJobEvents(t, job, 0)
	if len(events) != 3 ||
		events[1].Type != runWebStreamOutput || string(events[1].Chunk) != "in flight" ||
		events[2].Type != runWebStreamStatus {
		t.Fatalf("events = %#v, want terminal, in-flight output, final status", events)
	}
}

func TestRunWebJobWriteQueuedBehindFinishIsRejectedWithoutSideEffects(t *testing.T) {
	var terminal bytes.Buffer
	memory := newRunWebMemoryJournal()
	journal := &runWebBlockingStatusJournal{
		runWebEventJournal: memory,
		entered:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	job := mustNewRunWebJob(t, runWebJobConfig{
		Stdout: &terminal,
		NewJournal: func(string, int64) (runWebEventJournal, error) {
			return journal, nil
		},
	})

	finishDone := make(chan struct{})
	go func() {
		job.finish(nil)
		close(finishDone)
	}()
	<-journal.entered

	writeStarted := make(chan struct{})
	writeDone := make(chan runWebWriteResult, 1)
	go func() {
		close(writeStarted)
		n, err := job.Write([]byte("too late"))
		writeDone <- runWebWriteResult{n: n, err: err}
	}()
	<-writeStarted
	select {
	case result := <-writeDone:
		t.Fatalf("queued Write completed before finish released: %#v", result)
	case <-time.After(25 * time.Millisecond):
	}

	close(journal.release)
	<-finishDone
	result := <-writeDone
	if result.n != 0 || !errors.Is(result.err, io.ErrClosedPipe) {
		t.Fatalf("queued Write = %d, %v; want 0, io.ErrClosedPipe", result.n, result.err)
	}
	assertRunWebJobRejectedWriteHadNoSideEffects(t, job, memory, &terminal, 2)
}

func TestRunWebJobWriteAfterFinishIsRejectedWithoutSideEffects(t *testing.T) {
	var terminal bytes.Buffer
	memory := newRunWebMemoryJournal()
	job := mustNewRunWebJob(t, runWebJobConfig{
		Stdout: &terminal,
		NewJournal: func(string, int64) (runWebEventJournal, error) {
			return memory, nil
		},
	})
	job.finish(nil)

	n, err := job.Write([]byte("too late"))
	if n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Write after finish = %d, %v; want 0, io.ErrClosedPipe", n, err)
	}
	assertRunWebJobRejectedWriteHadNoSideEffects(t, job, memory, &terminal, 2)
}

func TestRunWebJobWritesQueuedBehindAndAfterCloseAreRejectedWithoutSideEffects(t *testing.T) {
	var terminal bytes.Buffer
	memory := newRunWebMemoryJournal()
	journal := &runWebBlockingCloseJournal{
		runWebEventJournal: memory,
		entered:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	job := mustNewRunWebJob(t, runWebJobConfig{
		Stdout: &terminal,
		NewJournal: func(string, int64) (runWebEventJournal, error) {
			return journal, nil
		},
	})

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- job.close()
	}()
	<-journal.entered

	writeStarted := make(chan struct{})
	writeDone := make(chan runWebWriteResult, 1)
	go func() {
		close(writeStarted)
		n, err := job.Write([]byte("queued after close"))
		writeDone <- runWebWriteResult{n: n, err: err}
	}()
	<-writeStarted
	select {
	case result := <-writeDone:
		t.Fatalf("queued Write completed before close released: %#v", result)
	case <-time.After(25 * time.Millisecond):
	}

	close(journal.release)
	if err := <-closeDone; err != nil {
		t.Fatalf("close: %v", err)
	}
	result := <-writeDone
	if result.n != 0 || !errors.Is(result.err, io.ErrClosedPipe) {
		t.Fatalf("queued Write = %d, %v; want 0, io.ErrClosedPipe", result.n, result.err)
	}
	n, err := job.Write([]byte("directly after close"))
	if n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Write after close = %d, %v; want 0, io.ErrClosedPipe", n, err)
	}
	assertRunWebJobRejectedWriteHadNoSideEffects(t, job, memory, &terminal, 1)
}

func TestRunWebJobSubscriberStopsWhenHardJournalFailurePreventsSeal(t *testing.T) {
	fake := &runWebFailAfterTerminalJournal{
		runWebEventJournal: newRunWebMemoryJournal(),
		err:                errors.New("journal unavailable"),
	}
	job := mustNewRunWebJob(t, runWebJobConfig{
		NewJournal: func(string, int64) (runWebEventJournal, error) {
			return fake, nil
		},
	})
	ch, done := job.subscribe(context.Background(), 0)
	if ev := receiveRunWebJobEvent(t, ch); ev.Type != runWebStreamTerminal {
		t.Fatalf("first event = %#v, want terminal", ev)
	}
	if _, err := job.Write([]byte("terminal only")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	job.finish(nil)
	assertRunWebJobSubscriptionClosed(t, ch, done)
}

func TestRunWebJobRecordsTerminalResizeInOutputOrder(t *testing.T) {
	resizes := make(chan catchrpc.Resize)
	profile := runWebTerminalProfile{
		TTY: true, Cols: 120, Rows: 40, Term: "xterm-256color", Scrollback: runWebTerminalScrollback,
	}
	job := mustNewRunWebJob(t, runWebJobConfig{Profile: profile, Resize: resizes})
	ch, done := job.subscribe(context.Background(), 0)
	var events []runWebStreamEvent

	if _, err := job.Write([]byte("before")); err != nil {
		t.Fatal(err)
	}
	resizes <- catchrpc.Resize{Cols: 132, Rows: 44}
	for {
		ev := receiveRunWebJobEvent(t, ch)
		events = append(events, ev)
		if ev.Type == runWebStreamResize {
			break
		}
	}
	if _, err := job.Write([]byte("after")); err != nil {
		t.Fatal(err)
	}
	close(resizes)
	job.finish(nil)

	for ev := range ch {
		events = append(events, ev)
	}
	<-done
	if len(events) != 5 {
		t.Fatalf("events = %#v, want terminal, output, resize, output, status", events)
	}
	if events[0].Type != runWebStreamTerminal || events[0].Profile != profile ||
		events[1].Type != runWebStreamOutput || string(events[1].Chunk) != "before" ||
		events[2].Type != runWebStreamResize || events[2].Cols != 132 || events[2].Rows != 44 ||
		events[3].Type != runWebStreamOutput || string(events[3].Chunk) != "after" ||
		events[4].Type != runWebStreamStatus || events[4].State != runWebJobSucceeded {
		t.Fatalf("events = %#v, want exact terminal/output/resize/output/status ordering", events)
	}
}

func TestRunWebJobJournalFullDegradesBrowserWithoutFailingDeployment(t *testing.T) {
	var terminal bytes.Buffer
	job := mustNewRunWebJob(t, runWebJobConfig{
		Stdout:       &terminal,
		JournalLimit: runWebJournalControlReserve + 100,
	})
	if _, err := job.Write([]byte("a")); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if _, err := job.Write(bytes.Repeat([]byte("b"), 256)); err != nil {
		t.Fatalf("full-journal Write = %v, want deployment to continue", err)
	}
	if _, err := job.Write([]byte("still terminal")); err != nil {
		t.Fatalf("degraded Write = %v, want deployment to continue", err)
	}
	job.finish(nil)

	events := collectRunWebJobEvents(t, job, 0)
	var warningIndex = -1
	var browserOutput []byte
	for i, ev := range events {
		switch ev.Type {
		case runWebStreamOutput:
			browserOutput = append(browserOutput, ev.Chunk...)
		case runWebStreamWarning:
			warningIndex = i
			if ev.Warning == "" {
				t.Fatal("degraded warning message is empty")
			}
		}
	}
	if warningIndex < 0 || warningIndex >= len(events)-1 || events[len(events)-1].Type != runWebStreamStatus {
		t.Fatalf("events = %#v, want warning before final status", events)
	}
	if string(browserOutput) != "a" {
		t.Fatalf("browser output = %q, want output only before journal filled", browserOutput)
	}
	wantTerminal := "a" + string(bytes.Repeat([]byte("b"), 256)) + "still terminal"
	if terminal.String() != wantTerminal {
		t.Fatalf("terminal length = %d, want %d exact bytes", terminal.Len(), len(wantTerminal))
	}
	status := job.status()
	if status.State != runWebJobSucceeded || !status.Degraded {
		t.Fatalf("status = %#v, want degraded success", status)
	}
}

func TestRunWebJobJournalWriteErrorIsNotReturnedThroughWrite(t *testing.T) {
	appendErr := errors.New("disk write failed")
	var terminal bytes.Buffer
	job := mustNewRunWebJob(t, runWebJobConfig{
		Stdout: &terminal,
		NewJournal: func(dir string, limit int64) (runWebEventJournal, error) {
			real, err := newRunWebJournal(dir, limit)
			if err != nil {
				return nil, err
			}
			return &runWebFailOutputJournal{runWebEventJournal: real, err: appendErr}, nil
		},
	})

	n, err := job.Write([]byte("exact bytes"))
	if n != len("exact bytes") || err != nil {
		t.Fatalf("Write = %d, %v; want full terminal write and nil", n, err)
	}
	job.finish(nil)

	events := collectRunWebJobEvents(t, job, 0)
	if len(events) != 3 ||
		events[0].Type != runWebStreamTerminal ||
		events[1].Type != runWebStreamWarning ||
		events[2].Type != runWebStreamStatus {
		t.Fatalf("events = %#v, want terminal, warning, status", events)
	}
	if terminal.String() != "exact bytes" {
		t.Fatalf("terminal = %q, want exact bytes", terminal.String())
	}
	if status := job.status(); status.State != runWebJobSucceeded || !status.Degraded {
		t.Fatalf("status = %#v, want degraded success", status)
	}
}

func TestRunWebJobFailedFinishAppendsErrorOutputOnlyWhenMissingFromTail(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		job := mustNewRunWebJob(t, runWebJobConfig{})
		job.finish(errors.New("deploy failed"))
		events := collectRunWebJobEvents(t, job, 0)
		if len(events) != 3 ||
			events[1].Type != runWebStreamOutput || string(events[1].Chunk) != "Error: deploy failed\n" ||
			events[2].Type != runWebStreamStatus || events[2].State != runWebJobFailed {
			t.Fatalf("events = %#v, want profile, appended error, failed status", events)
		}
	})

	t.Run("present in bounded tail", func(t *testing.T) {
		job := mustNewRunWebJob(t, runWebJobConfig{})
		if _, err := job.Write([]byte("deploy failed\n")); err != nil {
			t.Fatal(err)
		}
		job.finish(errors.New("deploy failed"))
		events := collectRunWebJobEvents(t, job, 0)
		if len(events) != 3 || string(events[1].Chunk) != "deploy failed\n" || events[2].Type != runWebStreamStatus {
			t.Fatalf("events = %#v, want existing output and failed status without duplicate", events)
		}
	})

	t.Run("outside bounded tail", func(t *testing.T) {
		job := mustNewRunWebJob(t, runWebJobConfig{})
		if _, err := job.Write([]byte("old failure\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := job.Write(bytes.Repeat([]byte("x"), runWebOutputTailLimit+1)); err != nil {
			t.Fatal(err)
		}
		job.finish(errors.New("old failure"))
		events := collectRunWebJobEvents(t, job, 0)
		var output []byte
		for _, ev := range events {
			if ev.Type == runWebStreamOutput {
				output = append(output, ev.Chunk...)
			}
		}
		if !bytes.HasSuffix(output, []byte("Error: old failure\n")) {
			t.Fatalf("events = %#v output length = %d status=%#v; suffix missing repeated failure line after it aged out of tail", events, len(output), job.status())
		}
	})
}

func TestRunWebJobFinishAndCloseAreIdempotentAndStopResizeObserver(t *testing.T) {
	resizes := make(chan catchrpc.Resize)
	fake := newRunWebMemoryJournal()
	job := mustNewRunWebJob(t, runWebJobConfig{
		Resize: resizes,
		NewJournal: func(string, int64) (runWebEventJournal, error) {
			return fake, nil
		},
	})
	job.finish(nil)
	job.finish(errors.New("second finish"))
	if err := job.close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := job.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	close(resizes)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.closeCalls != 1 {
		t.Fatalf("journal close calls = %d, want 1", fake.closeCalls)
	}
	statuses := 0
	for _, ev := range fake.events {
		if ev.Type == runWebStreamStatus {
			statuses++
		}
	}
	if statuses != 1 {
		t.Fatalf("status records = %d, want exactly 1", statuses)
	}
}

func TestRunWebJobCloseRemovesJournalAfterFailedRetryAndSuccessfulCompletion(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "failed retry", err: errors.New("deploy failed")},
		{name: "successful completion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			job := mustNewRunWebJob(t, runWebJobConfig{
				NewJournal: func(dir string, limit int64) (runWebEventJournal, error) {
					journal, err := newRunWebJournal(dir, limit)
					if err == nil {
						path = journal.path
					}
					return journal, err
				},
			})
			job.finish(tc.err)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("journal before close: %v", err)
			}
			if err := job.close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("journal after close error = %v, want not exist", err)
			}
		})
	}
}

func TestRunWebJobSubscriberCloseNoticeBehavior(t *testing.T) {
	t.Run("eventually exactly once", func(t *testing.T) {
		var notice runWebLockedBuffer
		job := mustNewRunWebJob(t, runWebJobConfig{Notice: &notice})
		ctx, cancel := context.WithCancel(context.Background())
		ch, done := job.subscribe(ctx, 0)
		cancel()
		assertRunWebJobSubscriptionClosed(t, ch, done)
		waitRunWebNotice(t, &notice, runWebBrowserClosedMessage)
		job.browserClosed()
		if got := notice.String(); got != runWebBrowserClosedMessage {
			t.Fatalf("notice = %q, want exactly once", got)
		}
	})

	t.Run("not after success", func(t *testing.T) {
		var notice runWebLockedBuffer
		job := mustNewRunWebJob(t, runWebJobConfig{Notice: &notice})
		ctx, cancel := context.WithCancel(context.Background())
		ch, done := job.subscribe(ctx, 0)
		cancel()
		assertRunWebJobSubscriptionClosed(t, ch, done)
		job.finish(nil)
		assertNoRunWebNoticeAfter(t, &notice, 850*time.Millisecond)
	})
}

func TestRunWebJobStreamEventSSEPayload(t *testing.T) {
	profile := runWebTerminalProfile{
		TTY: true, Cols: 120, Rows: 40, Term: "xterm-256color", Scrollback: runWebTerminalScrollback,
	}
	tests := []struct {
		name string
		ev   runWebStreamEvent
		want string
	}{
		{
			name: "terminal",
			ev:   runWebStreamEvent{Type: runWebStreamTerminal, Profile: profile},
			want: `{"tty":true,"cols":120,"rows":40,"term":"xterm-256color","scrollback":1000}`,
		},
		{
			name: "output preserves raw bytes",
			ev:   runWebStreamEvent{Type: runWebStreamOutput, Chunk: []byte{0xff, '\r', '\n', 0x00}},
			want: `{"encoding":"base64","chunk":"` + base64.StdEncoding.EncodeToString([]byte{0xff, '\r', '\n', 0x00}) + `"}`,
		},
		{
			name: "resize",
			ev:   runWebStreamEvent{Type: runWebStreamResize, Cols: 132, Rows: 44},
			want: `{"cols":132,"rows":44}`,
		},
		{
			name: "warning",
			ev:   runWebStreamEvent{Type: runWebStreamWarning, Warning: "browser replay stopped"},
			want: `{"message":"browser replay stopped"}`,
		},
		{
			name: "failed status",
			ev:   runWebStreamEvent{Type: runWebStreamStatus, State: runWebJobFailed, Error: "boom"},
			want: `{"state":"failed","error":"boom"}`,
		},
		{
			name: "successful status",
			ev:   runWebStreamEvent{Type: runWebStreamStatus, State: runWebJobSucceeded},
			want: `{"state":"succeeded"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eventName, data, err := tc.ev.ssePayload()
			if err != nil {
				t.Fatal(err)
			}
			if eventName != string(tc.ev.Type) || string(data) != tc.want {
				t.Fatalf("ssePayload = %q, %s; want %q, %s", eventName, data, tc.ev.Type, tc.want)
			}
			var valid any
			if err := json.Unmarshal(data, &valid); err != nil {
				t.Fatalf("payload is not JSON: %v", err)
			}
		})
	}
}

func mustNewRunWebJob(t *testing.T, cfg runWebJobConfig) *runWebJob {
	t.Helper()
	if cfg.JournalDir == "" {
		cfg.JournalDir = t.TempDir()
	}
	job, err := newRunWebJob("job-a", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := job.close(); err != nil {
			t.Errorf("close run web job: %v", err)
		}
	})
	return job
}

func collectRunWebJobEvents(t *testing.T, job *runWebJob, lastID int64) []runWebStreamEvent {
	t.Helper()
	ch, done := job.subscribe(context.Background(), lastID)
	var events []runWebStreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	<-done
	return events
}

func receiveRunWebJobEvent(t *testing.T, ch <-chan runWebStreamEvent) runWebStreamEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("subscription channel closed before event")
		}
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscription event")
	}
	return runWebStreamEvent{}
}

func assertRunWebJobSubscriptionClosed(t *testing.T, ch <-chan runWebStreamEvent, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscription done")
	}
	for range ch {
	}
}

func waitRunWebNotice(t *testing.T, notice interface{ String() string }, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if got := notice.String(); got == want {
			return
		} else if got != "" && got != want {
			t.Fatalf("notice = %q, want %q", got, want)
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("timed out waiting for notice %q; got %q", want, notice.String())
		}
	}
}

func assertNoRunWebNoticeAfter(t *testing.T, notice interface{ String() string }, d time.Duration) {
	t.Helper()
	time.Sleep(d)
	if got := notice.String(); got != "" {
		t.Fatalf("notice = %q, want none", got)
	}
}

type errRunWebJobWriter struct {
	err error
}

func (w errRunWebJobWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type runWebShortWriter struct {
	writer io.Writer
	n      int
}

func (w runWebShortWriter) Write(p []byte) (int, error) {
	return w.writer.Write(p[:min(w.n, len(p))])
}

type runWebGatedWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	buf     bytes.Buffer
}

func (w *runWebGatedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.buf.Write(p)
}

type runWebWriteResult struct {
	n   int
	err error
}

type runWebBlockingStatusJournal struct {
	runWebEventJournal
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (j *runWebBlockingStatusJournal) append(ev runWebStreamEvent, control bool) (int64, error) {
	if ev.Type == runWebStreamStatus {
		j.once.Do(func() { close(j.entered) })
		<-j.release
	}
	return j.runWebEventJournal.append(ev, control)
}

type runWebBlockingCloseJournal struct {
	runWebEventJournal
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (j *runWebBlockingCloseJournal) close() error {
	j.once.Do(func() { close(j.entered) })
	<-j.release
	return j.runWebEventJournal.close()
}

type runWebFailOutputJournal struct {
	runWebEventJournal
	err  error
	once sync.Once
}

func (j *runWebFailOutputJournal) append(ev runWebStreamEvent, control bool) (int64, error) {
	var err error
	if ev.Type == runWebStreamOutput {
		j.once.Do(func() { err = j.err })
	}
	if err != nil {
		return 0, err
	}
	return j.runWebEventJournal.append(ev, control)
}

type runWebFailAfterTerminalJournal struct {
	runWebEventJournal
	err      error
	appended int
}

func (j *runWebFailAfterTerminalJournal) append(ev runWebStreamEvent, control bool) (int64, error) {
	j.appended++
	if j.appended > 1 {
		return 0, j.err
	}
	return j.runWebEventJournal.append(ev, control)
}

type runWebMemoryJournal struct {
	mu         sync.Mutex
	events     []runWebStreamEvent
	wake       chan struct{}
	sealed     bool
	closed     bool
	closeCalls int
}

func newRunWebMemoryJournal() *runWebMemoryJournal {
	return &runWebMemoryJournal{wake: make(chan struct{})}
}

func (j *runWebMemoryJournal) append(ev runWebStreamEvent, _ bool) (int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return 0, os.ErrClosed
	}
	ev.ID = int64(len(j.events) + 1)
	j.events = append(j.events, ev)
	if ev.Type == runWebStreamStatus {
		j.sealed = true
	}
	close(j.wake)
	j.wake = make(chan struct{})
	return ev.ID, nil
}

func (j *runWebMemoryJournal) readAfter(cursor int64, _ int) ([]runWebStreamEvent, int64, <-chan struct{}, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil, cursor, nil, j.sealed, os.ErrClosed
	}
	if cursor < 0 || cursor > int64(len(j.events)) {
		return nil, cursor, j.wake, j.sealed, errRunWebJournalCursor
	}
	events := append([]runWebStreamEvent(nil), j.events[cursor:]...)
	next := int64(len(j.events))
	return events, next, j.wake, j.sealed, nil
}

func (j *runWebMemoryJournal) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.closeCalls++
	if j.closed {
		return nil
	}
	j.closed = true
	close(j.wake)
	return nil
}

func assertRunWebJobRejectedWriteHadNoSideEffects(t *testing.T, job *runWebJob, journal *runWebMemoryJournal, terminal *bytes.Buffer, wantEvents int) {
	t.Helper()
	if terminal.Len() != 0 {
		t.Fatalf("terminal output = %q, want none", terminal.String())
	}
	if status := job.status(); status.Degraded {
		t.Fatalf("status = %#v, want Degraded unchanged", status)
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if len(journal.events) != wantEvents {
		t.Fatalf("journal events = %#v, want %d unchanged events", journal.events, wantEvents)
	}
	for _, ev := range journal.events {
		if ev.Type == runWebStreamOutput || ev.Type == runWebStreamWarning {
			t.Fatalf("journal events = %#v, want no rejected-write output or warning", journal.events)
		}
	}
}

type runWebLockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *runWebLockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *runWebLockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
