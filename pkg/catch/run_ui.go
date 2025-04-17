// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/yeetrun/yeet/pkg/tui"
)

const (
	runStepUpload  = "Upload payload"
	runStepDetect  = "Detect payload"
	runStepInstall = "Install service"
)

type runUI struct {
	out     io.Writer
	enabled bool

	plain *plainRunUI
	color tui.Colorizer

	mu        sync.Mutex
	stopped   bool
	suspended bool
	current   string
	spinner   *tui.Spinner
}

func newRunUI(out io.Writer, enabled bool, service string) *runUI {
	return &runUI{
		out:     out,
		enabled: enabled,
		plain:   newPlainRunUI(out, service),
		color:   tui.NewColorizer(enabled),
	}
}

func (u *runUI) Start() {
	if u.enabled {
		u.plain.Header()
		return
	}
	u.plain.Header()
}

func (u *runUI) Stop() {
	u.mu.Lock()
	if u.stopped {
		u.mu.Unlock()
		return
	}
	u.stopped = true
	u.mu.Unlock()

	u.stopSpinner(false)
	u.plain.MarkHeaderDone()
}

func (u *runUI) Suspend() {
	u.mu.Lock()
	u.suspended = true
	u.mu.Unlock()
	u.stopSpinner(true)
}

func (u *runUI) StartStep(name string) {
	u.mu.Lock()
	if u.current == name {
		u.mu.Unlock()
		return
	}
	if u.suspended {
		u.mu.Unlock()
		return
	}
	u.current = name
	u.mu.Unlock()

	if u.enabled {
		u.stopSpinner(true)
		u.spinner = u.newSpinner(name)
		return
	}
	u.plain.StartStep(name)
}

func (u *runUI) UpdateDetail(detail string) {
	u.mu.Lock()
	name := u.current
	sp := u.spinner
	suspended := u.suspended
	u.mu.Unlock()

	if !u.enabled || suspended || sp == nil {
		u.plain.UpdateDetail(detail)
		return
	}
	text := name
	if detail != "" {
		text = fmt.Sprintf("%s %s", name, detail)
	}
	sp.Update(text)
}

func (u *runUI) DoneStep(detail string) {
	u.mu.Lock()
	name := u.current
	u.current = ""
	u.mu.Unlock()

	if name == "" {
		return
	}
	u.stopSpinner(true)
	if u.enabled {
		u.printStatus("OK", name, detail)
		return
	}
	u.plain.DoneStep(detail)
}

func (u *runUI) FailStep(detail string) {
	u.mu.Lock()
	name := u.current
	u.current = ""
	u.mu.Unlock()

	if name == "" {
		return
	}
	u.stopSpinner(true)
	if u.enabled {
		u.printStatus("ERR", name, detail)
		return
	}
	u.plain.FailStep(detail)
}

func (u *runUI) Printer(format string, args ...any) {
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if msg == "" {
		return
	}
	if u.handleKnownMessage(msg) {
		return
	}
	if u.enabled {
		return
	}
	u.plain.Header()
	fmt.Fprintln(u.out, msg)
}

func (u *runUI) handleKnownMessage(msg string) bool {
	switch {
	case strings.HasPrefix(msg, "Detected ") && strings.HasSuffix(msg, " file"):
		detail := strings.TrimSuffix(strings.TrimPrefix(msg, "Detected "), " file")
		u.mu.Lock()
		current := u.current
		u.mu.Unlock()
		if current != runStepDetect {
			u.StartStep(runStepDetect)
		}
		u.DoneStep(detail)
		return true
	case msg == "File received":
		return true
	case strings.HasPrefix(msg, "Installing service"):
		u.StartStep(runStepInstall)
		return true
	case strings.HasPrefix(msg, "Service \"") && strings.HasSuffix(msg, "\" installed"):
		u.DoneStep("")
		return true
	case strings.HasPrefix(msg, "Service installed:"):
		return true
	case strings.HasPrefix(msg, "Service restarted:"):
		return true
	default:
		return false
	}
}

func (u *runUI) newSpinner(text string) *tui.Spinner {
	sp := tui.NewSpinner(u.out,
		tui.WithFrames(tui.DefaultFrames),
		tui.WithInterval(120*time.Millisecond),
		tui.WithColor(u.color, tui.ColorYellow),
		tui.WithHideCursor(true),
	)
	sp.Start(text)
	return sp
}

func (u *runUI) stopSpinner(clear bool) {
	u.mu.Lock()
	sp := u.spinner
	u.spinner = nil
	u.mu.Unlock()

	if sp == nil {
		return
	}
	sp.Stop(clear)
}

func (u *runUI) printStatus(status, name, detail string) {
	label := status
	switch status {
	case "OK":
		label = u.color.Wrap(tui.ColorGreen, "✔")
	case "ERR":
		label = u.color.Wrap(tui.ColorRed, "✖")
	}
	line := fmt.Sprintf("%s %s", label, name)
	if detail != "" {
		line = fmt.Sprintf("%s (%s)", line, detail)
	}
	fmt.Fprintln(u.out, line)
}

type plainRunUI struct {
	out        io.Writer
	service    string
	headerDone bool
	current    string
}

func newPlainRunUI(out io.Writer, service string) *plainRunUI {
	return &plainRunUI{out: out, service: service}
}

func (p *plainRunUI) Header() {
	if p.headerDone {
		return
	}
	fmt.Fprintf(p.out, "[+] yeet run %s\n", p.service)
	p.headerDone = true
}

func (p *plainRunUI) MarkHeaderDone() {
	p.headerDone = true
}

func (p *plainRunUI) Info(label string) {
	p.Header()
	fmt.Fprintf(p.out, "[+] %s\n", label)
}

func (p *plainRunUI) StartStep(name string) {
	p.Header()
	p.current = name
	fmt.Fprintf(p.out, "[ ] %s\n", name)
}

func (p *plainRunUI) UpdateDetail(detail string) {
	_ = detail
}

func (p *plainRunUI) DoneStep(detail string) {
	p.Header()
	if p.current == "" {
		return
	}
	line := fmt.Sprintf("[OK] %s", p.current)
	if detail != "" {
		line = fmt.Sprintf("%s (%s)", line, detail)
	}
	fmt.Fprintln(p.out, line)
	p.current = ""
}

func (p *plainRunUI) FailStep(detail string) {
	p.Header()
	if p.current == "" {
		return
	}
	line := fmt.Sprintf("[ERR] %s", p.current)
	if detail != "" {
		line = fmt.Sprintf("%s (%s)", line, detail)
	}
	fmt.Fprintln(p.out, line)
	p.current = ""
}
