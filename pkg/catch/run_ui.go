// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shayne/yeet/pkg/tui"
)

const (
	runStepUpload  = "Upload payload"
	runStepDetect  = "Detect payload"
	runStepInstall = "Install service"
)

type ProgressUI interface {
	Start()
	Stop()
	Suspend()
	StartStep(name string)
	UpdateDetail(detail string)
	DoneStep(detail string)
	FailStep(detail string)
	Printer(format string, args ...any)
}

type runUI struct {
	out     io.Writer
	enabled bool
	quiet   bool

	action  string
	service string

	plain *plainRunUI
	color tui.Colorizer

	mu        sync.Mutex
	stopped   bool
	suspended bool
	current   string
	spinner   *tui.Spinner
}

func newRunUI(out io.Writer, enabled bool, quiet bool, action, service string) *runUI {
	return &runUI{
		out:     out,
		enabled: enabled,
		quiet:   quiet,
		action:  action,
		service: service,
		plain:   newPlainRunUI(out, action, service),
		color:   tui.NewColorizer(enabled),
	}
}

func (u *runUI) Start() {
	if u.quiet {
		return
	}
	if u.enabled {
		u.printHeader()
		return
	}
	u.plain.Header()
}

func (u *runUI) Stop() {
	if u.quiet {
		return
	}
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
	if u.quiet {
		return
	}
	u.mu.Lock()
	u.suspended = true
	u.mu.Unlock()
	u.stopSpinner(true)
}

func (u *runUI) StartStep(name string) {
	if u.quiet {
		return
	}
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
	if u.quiet {
		return
	}
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
	if u.quiet {
		return
	}
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
	if u.quiet {
		return
	}
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
	if u.quiet {
		return
	}
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
	u.plain.Info(msg)
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

func (u *runUI) printHeader() {
	label := "yeet"
	if u.action != "" {
		label = fmt.Sprintf("%s %s", label, u.action)
	}
	if u.service != "" {
		label = fmt.Sprintf("%s %s", label, u.service)
	}
	fmt.Fprintf(u.out, "[+] %s\n", strings.TrimSpace(label))
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
	action     string
	service    string
	headerDone bool
	current    string
}

func newPlainRunUI(out io.Writer, action, service string) *plainRunUI {
	return &plainRunUI{out: out, action: action, service: service}
}

func (p *plainRunUI) Header() {
	p.headerDone = true
}

func (p *plainRunUI) MarkHeaderDone() {
	p.headerDone = true
}

func (p *plainRunUI) Info(label string) {
	p.Header()
	fmt.Fprintln(p.out, p.line("info", "", label))
}

func (p *plainRunUI) StartStep(name string) {
	p.Header()
	p.current = name
	fmt.Fprintln(p.out, p.line("running", name, ""))
}

func (p *plainRunUI) UpdateDetail(detail string) {
	_ = detail
}

func (p *plainRunUI) DoneStep(detail string) {
	p.Header()
	if p.current == "" {
		return
	}
	fmt.Fprintln(p.out, p.line("ok", p.current, detail))
	p.current = ""
}

func (p *plainRunUI) FailStep(detail string) {
	p.Header()
	if p.current == "" {
		return
	}
	fmt.Fprintln(p.out, p.line("err", p.current, detail))
	p.current = ""
}

func (p *plainRunUI) line(status, step, detail string) string {
	parts := []string{
		"action", p.action,
		"service", p.service,
		"status", status,
	}
	if step != "" {
		parts = append(parts, "step", step)
	}
	if detail != "" {
		parts = append(parts, "detail", detail)
	}
	return formatKV(parts...)
}

func formatKV(parts ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(parts); i += 2 {
		key := strings.TrimSpace(parts[i])
		val := strings.TrimSpace(parts[i+1])
		if key == "" || val == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(quoteKV(val))
	}
	return b.String()
}

func quoteKV(val string) string {
	if needsQuote(val) {
		return strconv.Quote(val)
	}
	return val
}

func needsQuote(val string) bool {
	for _, r := range val {
		switch r {
		case ' ', '\t', '\n', '"', '=':
			return true
		}
	}
	return false
}

var _ ProgressUI = (*runUI)(nil)
