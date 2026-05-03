// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestTryRunDockerDecisions(t *testing.T) {
	oldService := serviceOverride
	oldImageExists := imageExistsFn
	oldPushImage := pushImageFn
	oldExecRemote := execRemoteDirectFn
	defer func() {
		serviceOverride = oldService
		imageExistsFn = oldImageExists
		pushImageFn = oldPushImage
		execRemoteDirectFn = oldExecRemote
	}()

	serviceOverride = "svc-a"
	pushErr := errors.New("push failed")
	stageErr := errors.New("stage failed")
	commitErr := errors.New("commit failed")

	tests := []struct {
		name      string
		exists    bool
		args      []string
		pushErr   error
		execErrAt int
		execErr   error
		wantOK    bool
		wantErr   string
		wantPush  bool
		wantExec  [][]string
	}{
		{
			name:   "missing image falls through",
			exists: false,
		},
		{
			name:     "push failure stops before remote calls",
			exists:   true,
			pushErr:  pushErr,
			wantErr:  "failed to push image",
			wantPush: true,
		},
		{
			name:     "stage args then commit",
			exists:   true,
			args:     []string{"--", "extra"},
			wantOK:   true,
			wantPush: true,
			wantExec: [][]string{{"stage", "--", "extra"}, {"stage", "commit"}},
		},
		{
			name:      "stage failure is returned",
			exists:    true,
			args:      []string{"extra"},
			execErrAt: 1,
			execErr:   stageErr,
			wantErr:   "failed to stage args",
			wantPush:  true,
			wantExec:  [][]string{{"stage", "extra"}},
		},
		{
			name:      "commit failure is returned",
			exists:    true,
			execErrAt: 1,
			execErr:   commitErr,
			wantErr:   "failed to run service",
			wantPush:  true,
			wantExec:  [][]string{{"stage", "commit"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pushCalled := false
			var execCalls [][]string
			imageExistsFn = func(image string) bool {
				if image != "app:latest" {
					t.Fatalf("imageExists image = %q, want app:latest", image)
				}
				return tt.exists
			}
			pushImageFn = func(ctx context.Context, service, image, tag string) error {
				pushCalled = true
				if service != "svc-a" || image != "app:latest" || tag != "latest" {
					t.Fatalf("pushImage = (%q, %q, %q), want (svc-a, app:latest, latest)", service, image, tag)
				}
				return tt.pushErr
			}
			execRemoteDirectFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
				if service != "svc-a" {
					t.Fatalf("exec service = %q, want svc-a", service)
				}
				if stdin != nil {
					t.Fatalf("expected nil stdin")
				}
				if !tty {
					t.Fatalf("expected tty=true")
				}
				execCalls = append(execCalls, append([]string{}, args...))
				if tt.execErrAt > 0 && len(execCalls) == tt.execErrAt {
					return tt.execErr
				}
				return nil
			}

			ok, err := tryRunDocker("app:latest", tt.args)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
			}
			if pushCalled != tt.wantPush {
				t.Fatalf("pushCalled = %v, want %v", pushCalled, tt.wantPush)
			}
			if !reflect.DeepEqual(execCalls, tt.wantExec) {
				t.Fatalf("execCalls = %#v, want %#v", execCalls, tt.wantExec)
			}
		})
	}
}
