// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRunWebJournalReplaysOrderedBinaryEvents(t *testing.T) {
	journal, err := newRunWebJournal(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.close() })

	profile := runWebStreamEvent{
		Type: runWebStreamTerminal,
		Profile: runWebTerminalProfile{
			TTY: true, Cols: 120, Rows: 40, Term: "xterm-256color", Scrollback: 1000,
		},
	}
	profileID := appendRunWebJournalEvent(t, journal, profile, true)
	firstOutput := []byte{0, '\r', 0x1b, '[', '2', 'K', 0xe2}
	firstID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type:  runWebStreamOutput,
		Chunk: firstOutput,
	}, false)
	splitOutput := []byte{0x9c, 0x94, '\n'}
	secondID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type:  runWebStreamOutput,
		Chunk: splitOutput,
	}, false)
	resize := runWebStreamEvent{Type: runWebStreamResize, Cols: 132, Rows: 44}
	resizeID := appendRunWebJournalEvent(t, journal, resize, true)
	invalidOutput := []byte{0xff, 0xfe, 0, '\r'}
	invalidID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type:  runWebStreamOutput,
		Chunk: invalidOutput,
	}, false)

	events, next, _, sealed, err := journal.readAfter(0, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if sealed {
		t.Fatal("journal sealed before final status")
	}
	wantBeforeStatus := []runWebStreamEvent{
		{ID: profileID, Type: runWebStreamTerminal, Profile: profile.Profile},
		{ID: secondID, Type: runWebStreamOutput, Chunk: append(append([]byte(nil), firstOutput...), splitOutput...)},
		{ID: resizeID, Type: runWebStreamResize, Cols: 132, Rows: 44},
		{ID: invalidID, Type: runWebStreamOutput, Chunk: invalidOutput},
	}
	assertRunWebJournalEvents(t, events, wantBeforeStatus)
	if next != invalidID {
		t.Fatalf("next cursor=%d want=%d", next, invalidID)
	}

	statusID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true)

	tests := []struct {
		name   string
		cursor int64
		want   []runWebStreamEvent
	}{
		{
			name:   "from beginning",
			cursor: 0,
			want: append(wantBeforeStatus,
				runWebStreamEvent{ID: statusID, Type: runWebStreamStatus, State: runWebJobSucceeded}),
		},
		{
			name:   "after first output frame",
			cursor: firstID,
			want: []runWebStreamEvent{
				{ID: secondID, Type: runWebStreamOutput, Chunk: splitOutput},
				{ID: resizeID, Type: runWebStreamResize, Cols: 132, Rows: 44},
				{ID: invalidID, Type: runWebStreamOutput, Chunk: invalidOutput},
				{ID: statusID, Type: runWebStreamStatus, State: runWebJobSucceeded},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, next, _, sealed, err := journal.readAfter(tt.cursor, 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			if !sealed {
				t.Fatal("journal not sealed after final status")
			}
			if next != statusID {
				t.Fatalf("next cursor=%d want=%d", next, statusID)
			}
			assertRunWebJournalEvents(t, events, tt.want)
		})
	}
}

func TestRunWebJournalCoalescesOutputWithinBatchAndControlBoundaries(t *testing.T) {
	journal := newRunWebJournalForTest(t, 1<<20)

	firstID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{Type: runWebStreamOutput, Chunk: []byte("ab")}, false)
	secondID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{Type: runWebStreamOutput, Chunk: []byte("cd")}, false)
	resizeID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{Type: runWebStreamResize, Cols: 90, Rows: 30}, true)
	thirdID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{Type: runWebStreamOutput, Chunk: []byte("ef")}, false)
	statusID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{Type: runWebStreamStatus, State: runWebJobSucceeded}, true)

	events, next, _, sealed, err := journal.readAfter(0, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if !sealed || next != statusID {
		t.Fatalf("sealed=%v next=%d want=%d", sealed, next, statusID)
	}
	assertRunWebJournalEvents(t, events, []runWebStreamEvent{
		{ID: secondID, Type: runWebStreamOutput, Chunk: []byte("abcd")},
		{ID: resizeID, Type: runWebStreamResize, Cols: 90, Rows: 30},
		{ID: thirdID, Type: runWebStreamOutput, Chunk: []byte("ef")},
		{ID: statusID, Type: runWebStreamStatus, State: runWebJobSucceeded},
	})

	events, next, _, sealed, err = journal.readAfter(0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !sealed {
		t.Fatal("sealed journal reported unsealed for a partial read")
	}
	assertRunWebJournalEvents(t, events, []runWebStreamEvent{
		{ID: firstID, Type: runWebStreamOutput, Chunk: []byte("ab")},
	})
	if next != firstID {
		t.Fatalf("partial next cursor=%d want=%d", next, firstID)
	}
}

func TestRunWebJournalSlowReaderDoesNotBlockWriter(t *testing.T) {
	const (
		childEnv = "YEET_TEST_RUN_WEB_JOURNAL_SLOW_READER"
		dirEnv   = "YEET_TEST_RUN_WEB_JOURNAL_SLOW_READER_DIR"
	)
	if os.Getenv(childEnv) == "1" {
		testRunWebJournalSlowReaderDoesNotBlockWriter(t, os.Getenv(dirEnv))
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunWebJournalSlowReaderDoesNotBlockWriter$")
	cmd.Env = append(os.Environ(), childEnv+"=1", dirEnv+"="+t.TempDir())
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	appendDone := make(chan error, 1)
	go func() { appendDone <- cmd.Wait() }()

	select {
	case err := <-appendDone:
		if err != nil {
			t.Fatalf("slow-reader subprocess failed: %v\n%s", err, strings.TrimSpace(output.String()))
		}
	case <-time.After(5 * time.Second):
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			<-appendDone
			t.Fatalf("kill blocked slow-reader subprocess: %v\n%s", err, strings.TrimSpace(output.String()))
		}
		<-appendDone
		t.Fatalf("append blocked while readers were idle\n%s", strings.TrimSpace(output.String()))
	}
}

func testRunWebJournalSlowReaderDoesNotBlockWriter(t *testing.T, dir string) {
	t.Helper()
	if dir == "" {
		t.Fatal("missing slow-reader journal directory")
	}
	journal, err := newRunWebJournal(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.close(); err != nil {
			t.Errorf("close journal: %v", err)
		}
	})

	_, _, wakeA, sealed, err := journal.readAfter(0, runWebJournalReadBatch)
	if err != nil {
		t.Fatal(err)
	}
	_, _, wakeB, _, err := journal.readAfter(0, runWebJournalReadBatch)
	if err != nil {
		t.Fatal(err)
	}
	if sealed {
		t.Fatal("new journal is sealed")
	}
	if wakeA != wakeB {
		t.Fatal("idle readers did not share the coalesced wake channel")
	}

	if _, err := journal.append(runWebStreamEvent{
		Type: runWebStreamOutput, Chunk: []byte{'x'},
	}, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wakeA:
	default:
		t.Fatal("append did not wake all readers")
	}
}

func TestRunWebJournalPreservesControlReserveAfterOutputCap(t *testing.T) {
	const outputFrames = int64(3)
	limit := int64(runWebJournalControlReserve) + outputFrames*(runWebJournalHeaderSize+1)
	journal := newRunWebJournalForTest(t, limit)

	var lastOutputID int64
	for i := int64(0); i < outputFrames; i++ {
		lastOutputID = appendRunWebJournalEvent(t, journal, runWebStreamEvent{
			Type: runWebStreamOutput, Chunk: []byte{'x'},
		}, false)
	}
	committed := journal.committed
	if _, err := journal.append(runWebStreamEvent{
		Type: runWebStreamOutput, Chunk: []byte{'y'},
	}, false); !errors.Is(err, errRunWebJournalFull) {
		t.Fatalf("append error=%v want=%v", err, errRunWebJournalFull)
	}
	if journal.committed != committed {
		t.Fatalf("failed append advanced committed offset from %d to %d", committed, journal.committed)
	}

	warning := "Browser replay stopped at the journal limit."
	warningID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamWarning, Warning: warning,
	}, true)
	statusID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true)
	if statusID > limit {
		t.Fatalf("control records exceeded limit: status ID=%d limit=%d", statusID, limit)
	}

	events, next, _, sealed, err := journal.readAfter(lastOutputID, runWebJournalReadBatch)
	if err != nil {
		t.Fatal(err)
	}
	if !sealed || next != statusID {
		t.Fatalf("sealed=%v next=%d want=%d", sealed, next, statusID)
	}
	assertRunWebJournalEvents(t, events, []runWebStreamEvent{
		{ID: warningID, Type: runWebStreamWarning, Warning: warning},
		{ID: statusID, Type: runWebStreamStatus, State: runWebJobSucceeded},
	})
}

func TestRunWebJournalResizeCannotConsumeControlReserve(t *testing.T) {
	limit := int64(runWebJournalControlReserve + runWebJournalHeaderSize + 1)
	journal := newRunWebJournalForTest(t, limit)
	appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamOutput, Chunk: []byte{'x'},
	}, false)
	committed := journal.committed

	if _, err := journal.append(runWebStreamEvent{
		Type: runWebStreamResize, Cols: 132, Rows: 44,
	}, true); !errors.Is(err, errRunWebJournalFull) {
		t.Fatalf("resize append error=%v want=%v", err, errRunWebJournalFull)
	}
	if journal.committed != committed {
		t.Fatalf("failed resize advanced committed offset from %d to %d", committed, journal.committed)
	}

	appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamWarning, Warning: "Browser replay stopped at the journal limit.",
	}, true)
	statusID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true)
	if statusID > limit {
		t.Fatalf("warning and status exceeded limit: status ID=%d limit=%d", statusID, limit)
	}
}

func TestRunWebJournalRejectsInvalidCursors(t *testing.T) {
	journal := newRunWebJournalForTest(t, 1<<20)
	nestedFrame := []byte{
		runWebJournalRecordOutput,
		0, 0, 0, 0, 0, 0, 0, 1,
		'x',
	}
	id := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamOutput, Chunk: nestedFrame,
	}, false)

	for cursor := int64(1); cursor < id; cursor++ {
		if _, _, _, _, err := journal.readAfter(cursor, runWebJournalReadBatch); !errors.Is(err, errRunWebJournalCursor) {
			t.Errorf("cursor %d error=%v want=%v", cursor, err, errRunWebJournalCursor)
		}
	}
	for _, cursor := range []int64{-1, id + 1} {
		if _, _, _, _, err := journal.readAfter(cursor, runWebJournalReadBatch); !errors.Is(err, errRunWebJournalCursor) {
			t.Errorf("out-of-range cursor %d error=%v want=%v", cursor, err, errRunWebJournalCursor)
		}
	}
}

func TestRunWebJournalRejectsCorruptFrames(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(t *testing.T, journal *runWebJournal)
	}{
		{
			name: "truncated frame",
			corrupt: func(t *testing.T, journal *runWebJournal) {
				t.Helper()
				if err := journal.file.Truncate(journal.committed - 1); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invalid event type",
			corrupt: func(t *testing.T, journal *runWebJournal) {
				t.Helper()
				if _, err := journal.file.WriteAt([]byte{0xff}, 0); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "payload beyond committed offset",
			corrupt: func(t *testing.T, journal *runWebJournal) {
				t.Helper()
				var size [8]byte
				binary.BigEndian.PutUint64(size[:], uint64(journal.committed))
				if _, err := journal.file.WriteAt(size[:], 1); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invalid control JSON",
			corrupt: func(t *testing.T, journal *runWebJournal) {
				t.Helper()
				if _, err := journal.file.WriteAt([]byte{'!'}, runWebJournalHeaderSize); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			journal := newRunWebJournalForTest(t, 1<<20)
			appendRunWebJournalEvent(t, journal, runWebStreamEvent{
				Type: runWebStreamStatus, State: runWebJobSucceeded,
			}, true)
			tt.corrupt(t, journal)
			if _, _, _, _, err := journal.readAfter(0, runWebJournalReadBatch); !errors.Is(err, errRunWebJournalCorrupt) {
				t.Fatalf("read error=%v want=%v", err, errRunWebJournalCorrupt)
			}
		})
	}
}

func TestRunWebJournalRejectsShortenedFrameAroundNestedFrameChain(t *testing.T) {
	journal := newRunWebJournalForTest(t, 1<<20)
	nestedFirst := runWebJournalTestFrame(runWebJournalRecordOutput, []byte("nested-a"))
	nestedSecond := runWebJournalTestFrame(runWebJournalRecordOutput, []byte("nested-b"))
	appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamOutput, Chunk: append(nestedFirst, nestedSecond...),
	}, false)
	appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true)

	var shortenedSize [8]byte
	binary.BigEndian.PutUint64(shortenedSize[:], uint64(len(nestedFirst)))
	if _, err := journal.file.WriteAt(shortenedSize[:], 1); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := journal.readAfter(0, runWebJournalReadBatch); !errors.Is(err, errRunWebJournalCorrupt) {
		t.Fatalf("read error=%v want=%v", err, errRunWebJournalCorrupt)
	}
}

func TestRunWebJournalRejectsEnlargedFrameEndingAtLaterBoundary(t *testing.T) {
	journal := newRunWebJournalForTest(t, 1<<20)
	appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamOutput, Chunk: []byte("first"),
	}, false)
	secondID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamOutput, Chunk: []byte("second"),
	}, false)
	appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true)

	var enlargedSize [8]byte
	binary.BigEndian.PutUint64(enlargedSize[:], uint64(secondID-runWebJournalHeaderSize))
	if _, err := journal.file.WriteAt(enlargedSize[:], 1); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := journal.readAfter(0, runWebJournalReadBatch); !errors.Is(err, errRunWebJournalCorrupt) {
		t.Fatalf("read error=%v want=%v", err, errRunWebJournalCorrupt)
	}
}

func TestRunWebJournalRejectsNonterminalDecodedStatus(t *testing.T) {
	journal := newRunWebJournalForTest(t, 1<<20)
	statusID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true)
	mutated := []byte(`{"state":"running"  }`)
	if got, want := len(mutated), int(statusID)-runWebJournalHeaderSize; got != want {
		t.Fatalf("mutation length=%d want=%d", got, want)
	}
	if _, err := journal.file.WriteAt(mutated, runWebJournalHeaderSize); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := journal.readAfter(0, runWebJournalReadBatch); !errors.Is(err, errRunWebJournalCorrupt) {
		t.Fatalf("read error=%v want=%v", err, errRunWebJournalCorrupt)
	}
}

func TestRunWebJournalRejectsStatusBeforeFinalBoundary(t *testing.T) {
	journal := newRunWebJournalForTest(t, 1<<20)
	statusPayload := []byte(`{"state":"succeeded"}`)
	appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamOutput, Chunk: statusPayload,
	}, false)
	appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true)
	if _, err := journal.file.WriteAt([]byte{runWebJournalRecordStatus}, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := journal.readAfter(0, runWebJournalReadBatch); !errors.Is(err, errRunWebJournalCorrupt) {
		t.Fatalf("read error=%v want=%v", err, errRunWebJournalCorrupt)
	}
}

func TestRunWebJournalUsesPrivateFileAndCleansUpIdempotently(t *testing.T) {
	journal, err := newRunWebJournal(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	path := journal.path
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("journal mode=%#o want=0600", got)
	}
	if err := journal.close(); err != nil {
		t.Fatal(err)
	}
	if err := journal.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal path still exists or stat failed: %v", err)
	}
}

func TestRunWebJournalBoundsEscapedStatusWithinControlReserve(t *testing.T) {
	tests := []struct {
		name      string
		errorText string
	}{
		{name: "quotes", errorText: strings.Repeat(`"`, runWebStatusErrorLimit+100)},
		{name: "backslashes", errorText: strings.Repeat(`\`, runWebStatusErrorLimit+100)},
		{name: "C0 controls", errorText: strings.Repeat("\x00", runWebStatusErrorLimit+100)},
		{
			name:      "invalid UTF-8",
			errorText: string(bytes.Repeat([]byte{0xff, 'x'}, runWebStatusErrorLimit)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit := int64(runWebJournalControlReserve + runWebJournalHeaderSize + 1)
			journal := newRunWebJournalForTest(t, limit)
			outputID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
				Type: runWebStreamOutput, Chunk: []byte{'x'},
			}, false)
			warning := "Browser replay stopped at the journal limit."
			warningID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
				Type: runWebStreamWarning, Warning: warning,
			}, true)

			statusID, err := journal.append(runWebStreamEvent{
				Type: runWebStreamStatus, State: runWebJobFailed, Error: tt.errorText,
			}, true)
			if err != nil {
				t.Fatal(err)
			}
			if statusID > limit {
				t.Fatalf("status ID=%d exceeds journal limit=%d", statusID, limit)
			}

			events, next, _, sealed, err := journal.readAfter(outputID, runWebJournalReadBatch)
			if err != nil {
				t.Fatal(err)
			}
			if !sealed || next != statusID {
				t.Fatalf("sealed=%v next=%d want=%d", sealed, next, statusID)
			}
			if len(events) != 2 || events[0].ID != warningID || events[0].Warning != warning {
				t.Fatalf("events=%#v", events)
			}
			got := events[1].Error
			if !utf8.ValidString(got) {
				t.Fatalf("status error is invalid UTF-8: %x", got)
			}
			if len(got) > runWebStatusErrorLimit {
				t.Fatalf("status error length=%d limit=%d", len(got), runWebStatusErrorLimit)
			}
			if !strings.HasSuffix(got, "…") {
				t.Fatalf("shortened status error lacks visible suffix: %q", got[len(got)-16:])
			}
			if tt.name == "invalid UTF-8" && !strings.ContainsRune(got, utf8.RuneError) {
				t.Fatalf("invalid UTF-8 was not normalized: %q", got)
			}
		})
	}
}

func TestRunWebJournalRejectsNonterminalStatus(t *testing.T) {
	for _, state := range []runWebJobState{"", runWebJobRunning, "unknown"} {
		t.Run(string(state), func(t *testing.T) {
			journal := newRunWebJournalForTest(t, 1<<20)
			wake := journal.wake
			if _, err := journal.append(runWebStreamEvent{
				Type: runWebStreamStatus, State: state,
			}, true); !errors.Is(err, errRunWebJournalStatus) {
				t.Fatalf("append error=%v want=%v", err, errRunWebJournalStatus)
			}
			if journal.committed != 0 || journal.sealed || len(journal.boundaries) != 1 {
				t.Fatalf("committed=%d sealed=%v boundaries=%v", journal.committed, journal.sealed, journal.boundaries)
			}
			if journal.wake != wake {
				t.Fatal("rejected status rotated wake channel")
			}
			select {
			case <-wake:
				t.Fatal("rejected status closed wake channel")
			default:
			}
		})
	}
}

func TestRunWebJournalRejectsEveryAppendAfterStatus(t *testing.T) {
	tests := []struct {
		name    string
		event   runWebStreamEvent
		control bool
	}{
		{name: "terminal", event: runWebStreamEvent{Type: runWebStreamTerminal}, control: true},
		{name: "output", event: runWebStreamEvent{Type: runWebStreamOutput, Chunk: []byte("late")}},
		{name: "resize", event: runWebStreamEvent{Type: runWebStreamResize, Cols: 80, Rows: 24}, control: true},
		{name: "warning", event: runWebStreamEvent{Type: runWebStreamWarning, Warning: "late"}, control: true},
		{name: "status", event: runWebStreamEvent{Type: runWebStreamStatus, State: runWebJobFailed}, control: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			journal := newRunWebJournalForTest(t, 1<<20)
			appendRunWebJournalEvent(t, journal, runWebStreamEvent{
				Type: runWebStreamStatus, State: runWebJobSucceeded,
			}, true)
			committed := journal.committed
			boundaries := len(journal.boundaries)
			wake := journal.wake

			if _, err := journal.append(tt.event, tt.control); !errors.Is(err, errRunWebJournalSealed) {
				t.Fatalf("append error=%v want=%v", err, errRunWebJournalSealed)
			}
			if journal.committed != committed || !journal.sealed || len(journal.boundaries) != boundaries {
				t.Fatalf("committed=%d sealed=%v boundaries=%v", journal.committed, journal.sealed, journal.boundaries)
			}
			if journal.wake != wake {
				t.Fatal("post-seal append rotated wake channel")
			}
			select {
			case <-wake:
				t.Fatal("post-seal append closed wake channel")
			default:
			}
		})
	}
}

func TestRunWebJournalFailedWriteDoesNotCommitFrame(t *testing.T) {
	writeErr := errors.New("injected partial write")
	journal := newRunWebJournalForTest(t, 1<<20)
	journal.file = &faultingRunWebJournalFile{
		runWebJournalFile: journal.file,
		writeErr:          writeErr,
		partial:           4,
		failWrites:        1,
	}
	wake := journal.wake

	if _, err := journal.append(runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true); !errors.Is(err, writeErr) {
		t.Fatalf("append error=%v want=%v", err, writeErr)
	}
	if journal.committed != 0 || journal.sealed || len(journal.boundaries) != 1 {
		t.Fatalf("committed=%d sealed=%v boundaries=%v", journal.committed, journal.sealed, journal.boundaries)
	}
	if journal.wake != wake {
		t.Fatal("failed write rotated wake channel")
	}
	assertRunWebJournalWakeOpen(t, wake)
	info, err := journal.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("rolled-back file size=%d want=0", info.Size())
	}
	appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true)
}

func TestRunWebJournalRollbackFailureFaultsJournal(t *testing.T) {
	writeErr := errors.New("injected partial write")
	rollbackErr := errors.New("injected rollback failure")
	journal := newRunWebJournalForTest(t, 1<<20)
	journal.file = &faultingRunWebJournalFile{
		runWebJournalFile: journal.file,
		writeErr:          writeErr,
		truncateErr:       rollbackErr,
		partial:           4,
		failWrites:        1,
	}
	wake := journal.wake

	_, err := journal.append(runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true)
	if !errors.Is(err, writeErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("append error=%v want joined write and rollback errors", err)
	}
	if journal.committed != 0 || journal.sealed || len(journal.boundaries) != 1 {
		t.Fatalf("committed=%d sealed=%v boundaries=%v", journal.committed, journal.sealed, journal.boundaries)
	}
	if journal.wake != wake {
		t.Fatal("failed write rotated wake channel")
	}
	assertRunWebJournalWakeOpen(t, wake)
	info, statErr := journal.file.Stat()
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Size() != 4 {
		t.Fatalf("partial tail size=%d want=4", info.Size())
	}
	events, next, _, sealed, readErr := journal.readAfter(0, runWebJournalReadBatch)
	if readErr != nil || len(events) != 0 || next != 0 || sealed {
		t.Fatalf("events=%#v next=%d sealed=%v error=%v", events, next, sealed, readErr)
	}
	if _, err := journal.append(runWebStreamEvent{
		Type: runWebStreamOutput, Chunk: []byte("must not append"),
	}, false); !errors.Is(err, errRunWebJournalFaulted) {
		t.Fatalf("post-fault append error=%v want=%v", err, errRunWebJournalFaulted)
	}
	if journal.committed != 0 || len(journal.boundaries) != 1 || journal.wake != wake {
		t.Fatalf("post-fault state changed: committed=%d boundaries=%v", journal.committed, journal.boundaries)
	}
}

func TestRunWebJournalFrameOffsetsMatchCommittedBytes(t *testing.T) {
	journal := newRunWebJournalForTest(t, 1<<20)
	firstPayload := []byte{0, 1, 2, 3}
	firstID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamOutput, Chunk: firstPayload,
	}, false)
	if want := int64(runWebJournalHeaderSize + len(firstPayload)); firstID != want {
		t.Fatalf("first ID=%d want=%d", firstID, want)
	}
	secondID := appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true)
	info, err := journal.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if secondID != journal.committed || info.Size() != journal.committed {
		t.Fatalf("second ID=%d file size=%d committed=%d", secondID, info.Size(), journal.committed)
	}
}

func TestRunWebJournalConcurrentReaderAndWriter(t *testing.T) {
	journal := newRunWebJournalForTest(t, 1<<20)
	const writers = 4
	const writesPerWriter = 1000

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for n := 0; n < writesPerWriter; n++ {
				if _, err := journal.append(runWebStreamEvent{
					Type: runWebStreamOutput, Chunk: []byte{'x'},
				}, false); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}()
	}

	readDone := make(chan int, 1)
	go func() {
		cursor := int64(0)
		outputBytes := 0
		for {
			events, next, wake, sealed, err := journal.readAfter(cursor, 1024)
			if err != nil {
				t.Errorf("read: %v", err)
				readDone <- -1
				return
			}
			for _, ev := range events {
				if ev.Type == runWebStreamOutput {
					outputBytes += len(ev.Chunk)
				}
			}
			cursor = next
			if sealed && len(events) == 0 {
				readDone <- outputBytes
				return
			}
			if len(events) == 0 {
				<-wake
			}
		}
	}()

	wg.Wait()
	appendRunWebJournalEvent(t, journal, runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true)
	if got := <-readDone; got != writers*writesPerWriter {
		t.Fatalf("replayed output bytes=%d want=%d", got, writers*writesPerWriter)
	}
}

func newRunWebJournalForTest(t *testing.T, limit int64) *runWebJournal {
	t.Helper()
	journal, err := newRunWebJournal(t.TempDir(), limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.close(); err != nil {
			t.Errorf("close journal: %v", err)
		}
	})
	return journal
}

func appendRunWebJournalEvent(t *testing.T, journal *runWebJournal, ev runWebStreamEvent, control bool) int64 {
	t.Helper()
	id, err := journal.append(ev, control)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func assertRunWebJournalEvents(t *testing.T, got, want []runWebStreamEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event count=%d want=%d\ngot=%#v\nwant=%#v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].ID != want[i].ID ||
			got[i].Type != want[i].Type ||
			!bytes.Equal(got[i].Chunk, want[i].Chunk) ||
			got[i].Profile != want[i].Profile ||
			got[i].Cols != want[i].Cols ||
			got[i].Rows != want[i].Rows ||
			got[i].Warning != want[i].Warning ||
			got[i].State != want[i].State ||
			got[i].Error != want[i].Error {
			t.Errorf("event[%d]=%#v want=%#v", i, got[i], want[i])
		}
	}
}

func assertRunWebJournalWakeOpen(t *testing.T, wake <-chan struct{}) {
	t.Helper()
	select {
	case <-wake:
		t.Fatal("wake channel is closed")
	default:
	}
}

func runWebJournalTestFrame(recordType byte, payload []byte) []byte {
	frame := make([]byte, runWebJournalHeaderSize+len(payload))
	frame[0] = recordType
	binary.BigEndian.PutUint64(frame[1:runWebJournalHeaderSize], uint64(len(payload)))
	copy(frame[runWebJournalHeaderSize:], payload)
	return frame
}

type faultingRunWebJournalFile struct {
	runWebJournalFile
	writeErr    error
	truncateErr error
	partial     int
	failWrites  int
}

func (f *faultingRunWebJournalFile) WriteAt(p []byte, offset int64) (int, error) {
	if f.failWrites == 0 {
		return f.runWebJournalFile.WriteAt(p, offset)
	}
	f.failWrites--
	n := min(f.partial, len(p))
	written, err := f.runWebJournalFile.WriteAt(p[:n], offset)
	if err != nil {
		return written, err
	}
	return written, f.writeErr
}

func (f *faultingRunWebJournalFile) Truncate(size int64) error {
	if f.truncateErr != nil {
		return f.truncateErr
	}
	return f.runWebJournalFile.Truncate(size)
}

var _ interface {
	io.ReaderAt
	io.WriterAt
	Stat() (os.FileInfo, error)
	Truncate(int64) error
	Close() error
} = (*faultingRunWebJournalFile)(nil)
