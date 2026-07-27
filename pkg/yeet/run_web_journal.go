// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const defaultRunWebJournalLimit int64 = 64 << 20

const (
	runWebJournalHeaderSize     = 9
	runWebJournalControlReserve = 64 << 10
	runWebJournalReadBatch      = 64 << 10
	runWebStatusErrorLimit      = 32 << 10
)

var (
	errRunWebJournalFull    = errors.New("web terminal journal is full")
	errRunWebJournalCursor  = errors.New("invalid web terminal journal cursor")
	errRunWebJournalCorrupt = errors.New("web terminal journal is corrupt")
	errRunWebJournalStatus  = errors.New("web terminal journal status must be terminal")
	errRunWebJournalSealed  = errors.New("web terminal journal is sealed")
	errRunWebJournalFaulted = errors.New("web terminal journal is faulted")
)

type runWebEventJournal interface {
	append(runWebStreamEvent, bool) (int64, error)
	readAfter(int64, int) ([]runWebStreamEvent, int64, <-chan struct{}, bool, error)
	close() error
}

type runWebJournalFile interface {
	io.ReaderAt
	io.WriterAt
	Stat() (os.FileInfo, error)
	Truncate(int64) error
	Close() error
}

// Header: one byte event type followed by an unsigned big-endian payload size.
type runWebJournal struct {
	mu         sync.Mutex
	file       runWebJournalFile
	path       string
	committed  int64
	limit      int64
	boundaries []int64
	wake       chan struct{}
	sealed     bool
	faulted    bool
	faultErr   error
	closed     bool
}

const (
	runWebJournalRecordTerminal byte = iota + 1
	runWebJournalRecordOutput
	runWebJournalRecordResize
	runWebJournalRecordWarning
	runWebJournalRecordStatus
)

func newRunWebJournal(dir string, limit int64) (*runWebJournal, error) {
	if limit <= 0 {
		limit = defaultRunWebJournalLimit
	}
	file, err := os.CreateTemp(dir, "yeet-web-run-*.journal")
	if err != nil {
		return nil, fmt.Errorf("create web terminal journal: %w", err)
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("secure web terminal journal: %w", err)
	}
	return &runWebJournal{
		file:       file,
		path:       path,
		limit:      limit,
		boundaries: []int64{0},
		wake:       make(chan struct{}),
	}, nil
}

func (j *runWebJournal) append(ev runWebStreamEvent, control bool) (int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.appendStateErrorLocked(); err != nil {
		return 0, err
	}
	if err := validateRunWebJournalAppend(ev); err != nil {
		return 0, err
	}
	frame, err := j.frameForEventLocked(ev)
	if err != nil {
		return 0, err
	}
	if ev.Type == runWebStreamResize {
		control = false
	}
	next, err := j.writeFrameLocked(frame, control)
	if err != nil {
		return 0, err
	}
	j.commitFrameLocked(ev, next)
	return next, nil
}

func validateRunWebJournalAppend(ev runWebStreamEvent) error {
	if ev.Type == runWebStreamStatus && !isTerminalRunWebJobState(ev.State) {
		return errRunWebJournalStatus
	}
	return nil
}

func (j *runWebJournal) frameForEventLocked(ev runWebStreamEvent) ([]byte, error) {
	recordType, payload, err := encodeRunWebJournalEvent(ev, j.limit-j.committed)
	if err != nil {
		return nil, err
	}
	if len(payload) > math.MaxInt64-runWebJournalHeaderSize {
		return nil, fmt.Errorf("%w: frame too large", errRunWebJournalFull)
	}
	frame := make([]byte, runWebJournalHeaderSize+len(payload))
	frame[0] = recordType
	binary.BigEndian.PutUint64(frame[1:runWebJournalHeaderSize], uint64(len(payload)))
	copy(frame[runWebJournalHeaderSize:], payload)
	return frame, nil
}

func (j *runWebJournal) writeFrameLocked(frame []byte, control bool) (int64, error) {
	next := j.committed + int64(len(frame))
	if next < j.committed || next > j.appendCeiling(control) {
		return 0, errRunWebJournalFull
	}
	if err := writeRunWebJournalFrame(j.file, j.committed, frame); err != nil {
		return 0, j.rollbackFailedAppendLocked(err)
	}
	return next, nil
}

func (j *runWebJournal) appendCeiling(control bool) int64 {
	if control {
		return j.limit
	}
	ceiling := j.limit - runWebJournalControlReserve
	if ceiling < 0 {
		return 0
	}
	return ceiling
}

func (j *runWebJournal) commitFrameLocked(ev runWebStreamEvent, next int64) {
	j.committed = next
	j.boundaries = append(j.boundaries, next)
	if ev.Type == runWebStreamStatus {
		j.sealed = true
	}
	wake := j.wake
	j.wake = make(chan struct{})
	close(wake)
}

func (j *runWebJournal) appendStateErrorLocked() error {
	if j.closed {
		return os.ErrClosed
	}
	if j.faulted {
		return errors.Join(errRunWebJournalFaulted, j.faultErr)
	}
	if j.sealed {
		return errRunWebJournalSealed
	}
	return nil
}

func (j *runWebJournal) rollbackFailedAppendLocked(writeErr error) error {
	rollbackErr := j.file.Truncate(j.committed)
	if rollbackErr == nil {
		return fmt.Errorf("append web terminal journal: %w", writeErr)
	}
	combined := errors.Join(
		fmt.Errorf("write frame: %w", writeErr),
		fmt.Errorf("rollback partial frame: %w", rollbackErr),
	)
	j.faulted = true
	j.faultErr = combined
	return fmt.Errorf("append web terminal journal: %w", combined)
}

func isTerminalRunWebJobState(state runWebJobState) bool {
	return state == runWebJobSucceeded || state == runWebJobFailed
}

func (j *runWebJournal) readAfter(cursor int64, maxOutputBytes int) ([]runWebStreamEvent, int64, <-chan struct{}, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil, cursor, nil, j.sealed, os.ErrClosed
	}
	boundaryIndex, valid := j.boundaryIndexLocked(cursor)
	if !valid {
		return nil, cursor, j.wake, j.sealed, errRunWebJournalCursor
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = runWebJournalReadBatch
	}

	events, next, err := j.readBatchLocked(cursor, boundaryIndex, maxOutputBytes)
	if err != nil {
		return nil, cursor, j.wake, j.sealed, err
	}
	return events, next, j.wake, j.sealed, nil
}

func (j *runWebJournal) boundaryIndexLocked(cursor int64) (int, bool) {
	index := sort.Search(len(j.boundaries), func(i int) bool {
		return j.boundaries[i] >= cursor
	})
	return index, index < len(j.boundaries) && j.boundaries[index] == cursor
}

func (j *runWebJournal) readBatchLocked(cursor int64, boundaryIndex, maxOutputBytes int) ([]runWebStreamEvent, int64, error) {
	events := make([]runWebStreamEvent, 0)
	next := cursor
	outputBytes := 0
	for next < j.committed {
		if boundaryIndex+1 >= len(j.boundaries) {
			return nil, cursor, errRunWebJournalCorrupt
		}
		expectedEnd := j.boundaries[boundaryIndex+1]
		ev, frameEnd, err := j.readEventAtLocked(next, expectedEnd)
		if err != nil {
			return nil, cursor, err
		}
		if runWebJournalBatchFull(ev, outputBytes, maxOutputBytes) {
			break
		}

		ev.ID = frameEnd
		events, outputBytes = appendRunWebJournalReadEvent(events, outputBytes, ev)
		next = frameEnd
		boundaryIndex++
	}
	return events, next, nil
}

func runWebJournalBatchFull(ev runWebStreamEvent, outputBytes, maxOutputBytes int) bool {
	return ev.Type == runWebStreamOutput &&
		outputBytes > 0 &&
		len(ev.Chunk) > maxOutputBytes-outputBytes
}

func appendRunWebJournalReadEvent(events []runWebStreamEvent, outputBytes int, ev runWebStreamEvent) ([]runWebStreamEvent, int) {
	if ev.Type != runWebStreamOutput {
		return append(events, ev), outputBytes
	}
	outputBytes += len(ev.Chunk)
	if len(events) > 0 && events[len(events)-1].Type == runWebStreamOutput {
		events[len(events)-1].Chunk = append(events[len(events)-1].Chunk, ev.Chunk...)
		events[len(events)-1].ID = ev.ID
		return events, outputBytes
	}
	ev.Chunk = append([]byte(nil), ev.Chunk...)
	return append(events, ev), outputBytes
}

func (j *runWebJournal) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	close(j.wake)
	return errors.Join(j.file.Close(), removeRunWebJournal(j.path))
}

func (j *runWebJournal) readEventAtLocked(offset, expectedEnd int64) (runWebStreamEvent, int64, error) {
	recordType, frameEnd, payload, err := j.readFrameAtLocked(offset)
	if err != nil {
		return runWebStreamEvent{}, offset, err
	}
	if frameEnd != expectedEnd {
		return runWebStreamEvent{}, offset, fmt.Errorf("%w: frame does not end at next committed boundary", errRunWebJournalCorrupt)
	}
	ev, err := decodeRunWebJournalEvent(recordType, payload)
	if err != nil {
		return runWebStreamEvent{}, offset, err
	}
	if ev.Type == runWebStreamStatus && frameEnd != j.committed {
		return runWebStreamEvent{}, offset, fmt.Errorf("%w: status is not the final committed frame", errRunWebJournalCorrupt)
	}
	return ev, frameEnd, nil
}

func (j *runWebJournal) readFrameAtLocked(offset int64) (byte, int64, []byte, error) {
	if offset < 0 || offset > j.committed-runWebJournalHeaderSize {
		return 0, offset, nil, errRunWebJournalCorrupt
	}
	var header [runWebJournalHeaderSize]byte
	if _, err := j.file.ReadAt(header[:], offset); err != nil {
		return 0, offset, nil, fmt.Errorf("%w: read frame header: %v", errRunWebJournalCorrupt, err)
	}
	payloadSize := binary.BigEndian.Uint64(header[1:])
	remaining := j.committed - offset - runWebJournalHeaderSize
	if payloadSize > uint64(remaining) || payloadSize > uint64(math.MaxInt) {
		return 0, offset, nil, fmt.Errorf("%w: invalid frame size", errRunWebJournalCorrupt)
	}
	frameEnd := offset + runWebJournalHeaderSize + int64(payloadSize)
	payload := make([]byte, int(payloadSize))
	if len(payload) > 0 {
		if _, err := j.file.ReadAt(payload, offset+runWebJournalHeaderSize); err != nil {
			return 0, offset, nil, fmt.Errorf("%w: read frame payload: %v", errRunWebJournalCorrupt, err)
		}
	}
	return header[0], frameEnd, payload, nil
}

func encodeRunWebJournalEvent(ev runWebStreamEvent, maxFrameBytes int64) (byte, []byte, error) {
	var (
		recordType byte
		value      any
	)
	switch ev.Type {
	case runWebStreamTerminal:
		recordType = runWebJournalRecordTerminal
		value = ev.Profile
	case runWebStreamOutput:
		return runWebJournalRecordOutput, append([]byte(nil), ev.Chunk...), nil
	case runWebStreamResize:
		recordType = runWebJournalRecordResize
		value = struct {
			Cols int `json:"cols"`
			Rows int `json:"rows"`
		}{Cols: ev.Cols, Rows: ev.Rows}
	case runWebStreamWarning:
		recordType = runWebJournalRecordWarning
		value = struct {
			Message string `json:"message"`
		}{Message: ev.Warning}
	case runWebStreamStatus:
		recordType = runWebJournalRecordStatus
		payload, err := encodeRunWebStatusPayload(ev.State, ev.Error, maxFrameBytes)
		return recordType, payload, err
	default:
		return 0, nil, fmt.Errorf("unsupported web terminal journal event type %q", ev.Type)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return 0, nil, fmt.Errorf("encode web terminal journal event: %w", err)
	}
	return recordType, payload, nil
}

type runWebStatusPayload struct {
	State runWebJobState `json:"state"`
	Error string         `json:"error,omitempty"`
}

func encodeRunWebStatusPayload(state runWebJobState, errorText string, maxFrameBytes int64) ([]byte, error) {
	normalized := strings.ToValidUTF8(errorText, string(utf8.RuneError))
	candidate, shortened := truncateRunWebStatusSource(normalized)
	payload, err := json.Marshal(runWebStatusPayload{State: state, Error: candidate})
	if err != nil {
		return nil, fmt.Errorf("encode web terminal journal event: %w", err)
	}
	if runWebStatusFrameFits(payload, maxFrameBytes) {
		return payload, nil
	}
	if errorText == "" {
		return nil, errRunWebJournalFull
	}

	prefix := candidate
	if shortened {
		prefix = strings.TrimSuffix(prefix, "…")
	}
	return fitRunWebStatusPayload(state, prefix, maxFrameBytes)
}

func truncateRunWebStatusSource(value string) (string, bool) {
	if len(value) <= runWebStatusErrorLimit {
		return value, false
	}
	const suffix = "…"
	end := runWebStatusErrorLimit - len(suffix)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + suffix, true
}

func fitRunWebStatusPayload(state runWebJobState, prefix string, maxFrameBytes int64) ([]byte, error) {
	boundaries := runWebStringRuneBoundaries(prefix)
	best := -1
	for low, high := 0, len(boundaries)-1; low <= high; {
		middle := low + (high-low)/2
		payload, err := json.Marshal(runWebStatusPayload{
			State: state,
			Error: prefix[:boundaries[middle]] + "…",
		})
		if err != nil {
			return nil, fmt.Errorf("encode web terminal journal event: %w", err)
		}
		if runWebStatusFrameFits(payload, maxFrameBytes) {
			best = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if best < 0 {
		return nil, errRunWebJournalFull
	}
	payload, err := json.Marshal(runWebStatusPayload{
		State: state,
		Error: prefix[:boundaries[best]] + "…",
	})
	if err != nil {
		return nil, fmt.Errorf("encode web terminal journal event: %w", err)
	}
	return payload, nil
}

func runWebStringRuneBoundaries(value string) []int {
	boundaries := make([]int, 1, utf8.RuneCountInString(value)+1)
	for index := range value {
		if index > 0 {
			boundaries = append(boundaries, index)
		}
	}
	return append(boundaries, len(value))
}

func runWebStatusFrameFits(payload []byte, maxFrameBytes int64) bool {
	return maxFrameBytes >= runWebJournalHeaderSize &&
		int64(len(payload)) <= maxFrameBytes-runWebJournalHeaderSize
}

func decodeRunWebJournalEvent(recordType byte, payload []byte) (runWebStreamEvent, error) {
	var (
		ev        runWebStreamEvent
		decodeErr error
	)
	switch recordType {
	case runWebJournalRecordTerminal:
		ev.Type = runWebStreamTerminal
		decodeErr = json.Unmarshal(payload, &ev.Profile)
	case runWebJournalRecordOutput:
		ev.Type = runWebStreamOutput
		ev.Chunk = payload
	case runWebJournalRecordResize:
		ev.Type = runWebStreamResize
		value := struct {
			Cols int `json:"cols"`
			Rows int `json:"rows"`
		}{}
		decodeErr = json.Unmarshal(payload, &value)
		ev.Cols, ev.Rows = value.Cols, value.Rows
	case runWebJournalRecordWarning:
		ev.Type = runWebStreamWarning
		value := struct {
			Message string `json:"message"`
		}{}
		decodeErr = json.Unmarshal(payload, &value)
		ev.Warning = value.Message
	case runWebJournalRecordStatus:
		ev.Type = runWebStreamStatus
		value := runWebStatusPayload{}
		decodeErr = json.Unmarshal(payload, &value)
		ev.State, ev.Error = value.State, value.Error
	default:
		return runWebStreamEvent{}, fmt.Errorf("%w: invalid event type %d", errRunWebJournalCorrupt, recordType)
	}
	if decodeErr != nil {
		return runWebStreamEvent{}, corruptRunWebJournalPayload(decodeErr)
	}
	if ev.Type == runWebStreamStatus && !isTerminalRunWebJobState(ev.State) {
		return runWebStreamEvent{}, fmt.Errorf("%w: status is not terminal", errRunWebJournalCorrupt)
	}
	return ev, nil
}

func writeRunWebJournalFrame(file runWebJournalFile, offset int64, frame []byte) error {
	for len(frame) > 0 {
		n, err := file.WriteAt(frame, offset)
		offset += int64(n)
		frame = frame[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func corruptRunWebJournalPayload(err error) error {
	return fmt.Errorf("%w: invalid control payload: %v", errRunWebJournalCorrupt, err)
}

func removeRunWebJournal(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

var _ runWebEventJournal = (*runWebJournal)(nil)
