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
	"strings"
	"testing"
	"time"
)

func TestRunWebJobWritesTerminalOutputAndReplaysOutputAndStatus(t *testing.T) {
	var terminal bytes.Buffer
	job := newRunWebJob("job-a", runWebJobConfig{Stdout: &terminal})

	if n, err := job.Write([]byte("deploying\n")); err != nil || n != len("deploying\n") {
		t.Fatalf("Write = %d, %v; want full write and nil error", n, err)
	}
	job.finish(nil)

	if terminal.String() != "deploying\n" {
		t.Fatalf("terminal output = %q, want deploying", terminal.String())
	}

	events := collectRunWebJobEvents(t, job, 0)
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2: %#v", len(events), events)
	}
	if events[0].Type != runWebStreamOutput || string(events[0].Chunk) != "deploying\n" {
		t.Fatalf("first event = %#v, want output chunk", events[0])
	}
	if events[1].Type != runWebStreamStatus || events[1].State != runWebJobSucceeded {
		t.Fatalf("second event = %#v, want succeeded status", events[1])
	}
}

func TestRunWebJobWriteErrorDoesNotBroadcastOutput(t *testing.T) {
	job := newRunWebJob("job-a", runWebJobConfig{Stdout: errRunWebJobWriter{err: errors.New("terminal failed")}})

	n, err := job.Write([]byte("deploying\n"))
	if err == nil || err.Error() != "terminal failed" {
		t.Fatalf("Write error = %v, want terminal failed", err)
	}
	if n != 0 {
		t.Fatalf("Write n = %d, want 0", n)
	}
	job.finish(nil)

	events := collectRunWebJobEvents(t, job, 0)
	if len(events) != 1 {
		t.Fatalf("events len = %d, want only status: %#v", len(events), events)
	}
	if events[0].Type != runWebStreamStatus || events[0].State != runWebJobSucceeded {
		t.Fatalf("event = %#v, want succeeded status only", events[0])
	}
}

func TestRunWebJobNilStdoutAndNoticeAreSafe(t *testing.T) {
	job := newRunWebJob("job-a", runWebJobConfig{})

	if n, err := job.Write([]byte("deploying\n")); err != nil || n != len("deploying\n") {
		t.Fatalf("Write with nil stdout = %d, %v; want full write and nil error", n, err)
	}
	job.finish(nil)
	job.browserClosed()

	status := job.status()
	if status.State != runWebJobSucceeded || status.Error != "" {
		t.Fatalf("status = %#v, want succeeded without error", status)
	}
}

func TestRunWebJobFailedStatusContainsErrorText(t *testing.T) {
	job := newRunWebJob("job-a", runWebJobConfig{})

	job.finish(errors.New("deploy failed"))

	status := job.status()
	if status.ID != "job-a" || status.State != runWebJobFailed || status.Error != "deploy failed" {
		t.Fatalf("status = %#v, want failed status with error text", status)
	}
}

func TestRunWebJobSubscribeReplaysOnlyEventsAfterLastID(t *testing.T) {
	job := newRunWebJob("job-a", runWebJobConfig{})
	if _, err := job.Write([]byte("first\n")); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if _, err := job.Write([]byte("second\n")); err != nil {
		t.Fatalf("write second: %v", err)
	}
	job.finish(nil)

	allEvents := collectRunWebJobEvents(t, job, 0)
	if len(allEvents) != 3 {
		t.Fatalf("all events len = %d, want 3: %#v", len(allEvents), allEvents)
	}
	events := collectRunWebJobEvents(t, job, allEvents[0].ID)
	if len(events) != 2 {
		t.Fatalf("filtered events len = %d, want second output and status: %#v", len(events), events)
	}
	if string(events[0].Chunk) != "second\n" || events[1].Type != runWebStreamStatus {
		t.Fatalf("filtered events = %#v, want second output then status", events)
	}
}

func TestRunWebJobLiveSubscriberReceivesOutputAndClosesAfterFinish(t *testing.T) {
	job := newRunWebJob("job-a", runWebJobConfig{})
	ch, done := job.subscribe(context.Background(), 0)

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
	job := newRunWebJob("job-a", runWebJobConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	ch, done := job.subscribe(ctx, 0)

	cancel()

	assertRunWebJobSubscriptionClosed(t, ch, done)
}

func TestRunWebJobFinishIsIdempotent(t *testing.T) {
	job := newRunWebJob("job-a", runWebJobConfig{})

	job.finish(nil)
	job.finish(errors.New("second finish"))

	events := collectRunWebJobEvents(t, job, 0)
	if len(events) != 1 {
		t.Fatalf("events len = %d, want one status: %#v", len(events), events)
	}
	if events[0].Type != runWebStreamStatus || events[0].State != runWebJobSucceeded || events[0].Error != "" {
		t.Fatalf("event = %#v, want original succeeded status", events[0])
	}
}

func TestRunWebJobFailedFinishAppendsErrorOutputWhenMissing(t *testing.T) {
	job := newRunWebJob("job-a", runWebJobConfig{})

	job.finish(errors.New("deploy failed"))

	events := collectRunWebJobEvents(t, job, 0)
	if len(events) != 2 {
		t.Fatalf("events len = %d, want error output and status: %#v", len(events), events)
	}
	if events[0].Type != runWebStreamOutput || string(events[0].Chunk) != "Error: deploy failed\n" {
		t.Fatalf("first event = %#v, want appended error output", events[0])
	}
	if events[1].Type != runWebStreamStatus || events[1].State != runWebJobFailed || events[1].Error != "deploy failed" {
		t.Fatalf("second event = %#v, want failed status", events[1])
	}
}

func TestRunWebJobFailedFinishDoesNotDuplicateExistingErrorOutput(t *testing.T) {
	job := newRunWebJob("job-a", runWebJobConfig{})
	if _, err := job.Write([]byte("deploy failed\n")); err != nil {
		t.Fatalf("write existing error: %v", err)
	}

	job.finish(errors.New("deploy failed"))

	events := collectRunWebJobEvents(t, job, 0)
	if len(events) != 2 {
		t.Fatalf("events len = %d, want existing output and status: %#v", len(events), events)
	}
	if events[0].Type != runWebStreamOutput || string(events[0].Chunk) != "deploy failed\n" {
		t.Fatalf("first event = %#v, want existing output", events[0])
	}
	if events[1].Type != runWebStreamStatus || events[1].State != runWebJobFailed || events[1].Error != "deploy failed" {
		t.Fatalf("second event = %#v, want failed status", events[1])
	}
}

func TestRunWebJobBufferLimitCreatesOmissionEventAndRetainsNewestOutput(t *testing.T) {
	job := newRunWebJob("job-a", runWebJobConfig{BufferLimit: 7})

	if _, err := job.Write([]byte("first\n")); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if _, err := job.Write([]byte("second\n")); err != nil {
		t.Fatalf("write second: %v", err)
	}
	job.finish(nil)

	events := collectRunWebJobEvents(t, job, 0)
	if len(events) != 3 {
		t.Fatalf("events len = %d, want omission, output, status: %#v", len(events), events)
	}
	if events[0].Type != runWebStreamOutput || string(events[0].Chunk) != runWebOutputOmittedMessage {
		t.Fatalf("omission event = %#v, want omitted output message", events[0])
	}
	if events[1].Type != runWebStreamOutput || string(events[1].Chunk) != "second\n" {
		t.Fatalf("retained event = %#v, want newest output", events[1])
	}
}

func TestRunWebJobPartialTrimDoesNotReplaySuffixAfterOriginalEventID(t *testing.T) {
	job := newRunWebJob("job-a", runWebJobConfig{BufferLimit: 5})

	if _, err := job.Write([]byte("abcdefgh")); err != nil {
		t.Fatalf("write chunk: %v", err)
	}
	job.finish(nil)

	events := collectRunWebJobEvents(t, job, 1)
	for _, ev := range events {
		if ev.Type == runWebStreamOutput && string(ev.Chunk) == "defgh" {
			t.Fatalf("replayed suffix from already-delivered event: %#v", events)
		}
	}
}

func TestRunWebJobPartialBufferTrimDropsSuffixAndReplaysLaterOutputWithUniqueIncreasingEventIDs(t *testing.T) {
	job := newRunWebJob("job-a", runWebJobConfig{BufferLimit: 10})

	if _, err := job.Write([]byte("abcdefgh")); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if _, err := job.Write([]byte("1234567")); err != nil {
		t.Fatalf("write second: %v", err)
	}
	job.finish(nil)

	events := collectRunWebJobEvents(t, job, 0)
	if len(events) != 3 {
		t.Fatalf("events len = %d, want omission, newest output, status: %#v", len(events), events)
	}
	for i := 1; i < len(events); i++ {
		if events[i].ID <= events[i-1].ID {
			t.Fatalf("event IDs are not strictly increasing: %#v", events)
		}
	}
	if events[0].Type != runWebStreamOutput || string(events[0].Chunk) != runWebOutputOmittedMessage {
		t.Fatalf("omission event = %#v, want omitted output message", events[0])
	}
	if events[1].Type != runWebStreamOutput || string(events[1].Chunk) != "1234567" {
		t.Fatalf("retained event = %#v, want later full output", events[1])
	}

	afterOmission := collectRunWebJobEvents(t, job, events[0].ID)
	if len(afterOmission) != 2 {
		t.Fatalf("events after omission len = %d, want newest output and status: %#v", len(afterOmission), afterOmission)
	}
	if afterOmission[0].ID != events[1].ID || string(afterOmission[0].Chunk) != "1234567" {
		t.Fatalf("events after omission start = %#v, want later full output event %#v", afterOmission[0], events[1])
	}
}

func TestRunWebJobSlowSubscriberDoesNotBlockWrite(t *testing.T) {
	job := newRunWebJob("job-a", runWebJobConfig{SubscriberBuffer: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, done := job.subscribe(ctx, 0)
	defer func() {
		cancel()
		<-done
	}()

	if _, err := job.Write([]byte("fill\n")); err != nil {
		t.Fatalf("write fill: %v", err)
	}
	deadline := time.After(250 * time.Millisecond)
	wrote := make(chan error, 1)
	go func() {
		_, err := job.Write([]byte("drop if needed\n"))
		wrote <- err
	}()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatalf("write with slow subscriber: %v", err)
		}
	case <-deadline:
		t.Fatal("Write blocked behind slow subscriber")
	}
	select {
	case <-ch:
	default:
	}
}

func TestRunWebJobBrowserCloseNoticePrintsExactlyOnce(t *testing.T) {
	var notice bytes.Buffer
	job := newRunWebJob("job-a", runWebJobConfig{Notice: &notice})

	job.browserClosed()
	job.browserClosed()

	if notice.String() != runWebBrowserClosedMessage {
		t.Fatalf("notice = %q, want one browser close message", notice.String())
	}
}

func TestRunWebJobStreamEventSSEPayload(t *testing.T) {
	output := runWebStreamEvent{Type: runWebStreamOutput, Chunk: []byte("hello\n")}
	eventName, data, err := output.ssePayload()
	if err != nil {
		t.Fatalf("output ssePayload: %v", err)
	}
	if eventName != string(runWebStreamOutput) {
		t.Fatalf("output eventName = %q, want output", eventName)
	}
	var outputPayload struct {
		Encoding string `json:"encoding"`
		Chunk    string `json:"chunk"`
	}
	if err := json.Unmarshal(data, &outputPayload); err != nil {
		t.Fatalf("unmarshal output payload: %v", err)
	}
	if outputPayload.Encoding != "base64" || outputPayload.Chunk != base64.StdEncoding.EncodeToString([]byte("hello\n")) {
		t.Fatalf("output payload = %#v, want base64 chunk", outputPayload)
	}

	status := runWebStreamEvent{Type: runWebStreamStatus, State: runWebJobFailed, Error: "boom"}
	eventName, data, err = status.ssePayload()
	if err != nil {
		t.Fatalf("status ssePayload: %v", err)
	}
	if eventName != string(runWebStreamStatus) {
		t.Fatalf("status eventName = %q, want status", eventName)
	}
	if !strings.Contains(string(data), `"state":"failed"`) || !strings.Contains(string(data), `"error":"boom"`) {
		t.Fatalf("status payload = %s, want state and error", data)
	}

	status = runWebStreamEvent{Type: runWebStreamStatus, State: runWebJobSucceeded}
	eventName, data, err = status.ssePayload()
	if err != nil {
		t.Fatalf("success status ssePayload: %v", err)
	}
	if eventName != string(runWebStreamStatus) {
		t.Fatalf("success status eventName = %q, want status", eventName)
	}
	var statusPayload map[string]any
	if err := json.Unmarshal(data, &statusPayload); err != nil {
		t.Fatalf("unmarshal success status payload: %v", err)
	}
	if statusPayload["state"] != string(runWebJobSucceeded) {
		t.Fatalf("success status payload = %#v, want succeeded state", statusPayload)
	}
	if _, ok := statusPayload["error"]; ok {
		t.Fatalf("success status payload = %#v, want no error field", statusPayload)
	}
}

func collectRunWebJobEvents(t *testing.T, job *runWebJob, lastID int64) []runWebStreamEvent {
	t.Helper()
	ch, done := job.subscribe(context.Background(), lastID)
	<-done
	var events []runWebStreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
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
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("subscription channel is still open")
		}
	default:
		t.Fatal("subscription channel did not close")
	}
}

type errRunWebJobWriter struct {
	err error
}

func (w errRunWebJobWriter) Write([]byte) (int, error) {
	return 0, w.err
}
