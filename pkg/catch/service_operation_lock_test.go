// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"sync"
	"testing"
	"time"
)

func TestServiceOperationLocksSortAndSerialize(t *testing.T) {
	var locks serviceOperationLocks
	releaseA := locks.Lock("worker", "api", "api")
	done := make(chan struct{})
	go func() {
		release := locks.Lock("api")
		release()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("second lock acquired early")
	case <-time.After(25 * time.Millisecond):
	}
	releaseA()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second lock did not acquire after release")
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.locks) != 0 {
		t.Fatalf("released keyed locks retained %d entries", len(locks.locks))
	}
}

func TestServiceOperationLocksOppositeOrderDoesNotDeadlock(t *testing.T) {
	var locks serviceOperationLocks
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, names := range [][]string{{"worker", "api"}, {"api", "worker"}} {
		go func(names []string) {
			ready.Done()
			<-start
			release := locks.Lock(names...)
			release()
			done <- struct{}{}
		}(names)
	}
	ready.Wait()
	close(start)
	for range 2 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("opposite-order lock acquisition deadlocked")
		}
	}
}

func TestServiceOperationLocksEmptySetIsNoop(t *testing.T) {
	var locks serviceOperationLocks
	release := locks.Lock()
	release()
}
