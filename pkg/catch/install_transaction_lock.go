// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// AcquireCatchInstallTransactionLock serializes a Catch install with startup
// runtime recovery by locking the already-validated Catch data-root directory.
// Startup takes this lock before the runtime-journal lock, matching installer
// lock order without adding a path that an upgrade could replace.
func AcquireCatchInstallTransactionLock(ctx context.Context, dataRoot string, trustedUID uint32) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := openValidatedCatchInstallRoot(dataRoot, trustedUID)
	if err != nil {
		return nil, err
	}
	for {
		err := unix.Flock(int(dir.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, errors.Join(ctxErr, dir.Close())
			}
			return dir, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return nil, errors.Join(fmt.Errorf("lock Catch data root: %w", err), dir.Close())
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), dir.Close())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func openValidatedCatchInstallRoot(dataRoot string, trustedUID uint32) (*os.File, error) {
	fd, err := unix.Open(dataRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open Catch data root without following symlinks: %w", err)
	}
	dir := os.NewFile(uintptr(fd), dataRoot)
	if dir == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind Catch data root directory descriptor")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, errors.Join(fmt.Errorf("inspect Catch data root: %w", err), dir.Close())
	}
	mode := uint32(stat.Mode)
	if mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, errors.Join(fmt.Errorf("catch data root is not a directory"), dir.Close())
	}
	if stat.Uid != trustedUID {
		return nil, errors.Join(fmt.Errorf("catch data root owner is %d, want %d", stat.Uid, trustedUID), dir.Close())
	}
	if mode&0o022 != 0 {
		return nil, errors.Join(fmt.Errorf("catch data root is writable by group or others: mode %o", mode&0o7777), dir.Close())
	}
	return dir, nil
}
