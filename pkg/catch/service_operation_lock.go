// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"slices"
	"sync"
)

// serviceOperationLocks serializes mutations which act on the same service.
// Multi-service callers acquire a deduplicated lexical ordering so two batch
// operations cannot deadlock by presenting the same names in different order.
type serviceOperationLocks struct {
	mu    sync.Mutex
	locks map[string]*serviceOperationLock
}

type serviceOperationLock struct {
	mu   sync.Mutex
	refs int
}

func (l *serviceOperationLocks) Lock(names ...string) func() {
	names = uniqueSortedServiceOperationNames(names)
	if len(names) == 0 {
		return func() {}
	}

	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*serviceOperationLock)
	}
	locks := make([]*serviceOperationLock, 0, len(names))
	for _, name := range names {
		lock := l.locks[name]
		if lock == nil {
			lock = &serviceOperationLock{}
			l.locks[name] = lock
		}
		lock.refs++
		locks = append(locks, lock)
	}
	l.mu.Unlock()

	for _, lock := range locks {
		lock.mu.Lock()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(locks) - 1; i >= 0; i-- {
				locks[i].mu.Unlock()
			}
			l.mu.Lock()
			defer l.mu.Unlock()
			for i, name := range names {
				locks[i].refs--
				if locks[i].refs == 0 {
					delete(l.locks, name)
				}
			}
		})
	}
}

func uniqueSortedServiceOperationNames(names []string) []string {
	unique := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != "" {
			unique[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(unique))
	for name := range unique {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
