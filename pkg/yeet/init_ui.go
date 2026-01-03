// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shayne/yeet/pkg/catchrpc"
	"github.com/shayne/yeet/pkg/tui"
)

func initProgressSettings(mode catchrpc.ProgressMode, isTTY bool) (enabled bool, quiet bool) {
	switch mode {
	case catchrpc.ProgressTTY:
		return true, false
	case catchrpc.ProgressPlain:
		return false, false
	case catchrpc.ProgressQuiet:
		return false, true
	default:
		return isTTY, false
	}
}

type initUI struct {
	out     io.Writer
	enabled bool
	quiet   bool

	host    string
	remote  string
	service string

	plain *plainInitUI
	color tui.Colorizer

	mu        sync.Mutex
	stopped   bool
	suspended bool
	current   string
	spinner   *tui.Spinner
}

func newInitUI(out io.Writer, enabled bool, quiet bool, host, remote, service string) *initUI {
	return &initUI{
		out:     out,
		enabled: enabled,
		quiet:   quiet,
		host:    host,
		remote:  remote,
		service: service,
		plain:   newPlainInitUI(out, host, remote, service),
		color:   tui.NewColorizer(enabled),
	}
}

func (u *initUI) Start() {
	if u.quiet {
		return
	}
	if u.enabled {
		u.printHeader()
		return
	}
	u.plain.Header()
}

func (u *initUI) Stop() {
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

func (u *initUI) Suspend() {
	if u.quiet {
		return
	}
	u.mu.Lock()
	u.suspended = true
	u.mu.Unlock()
	u.stopSpinner(true)
}

func (u *initUI) StartStep(name string) {
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

func (u *initUI) UpdateDetail(detail string) {
	if u.quiet {
		return
	}
	u.mu.Lock()
	name := u.current
	sp := u.spinner
	suspended := u.suspended
	u.mu.Unlock()

	if !u.enabled || suspended || sp == nil {
		return
	}
	text := name
	if detail != "" {
		text = fmt.Sprintf("%s %s", name, detail)
	}
	sp.Update(text)
}

func (u *initUI) DoneStep(detail string) {
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

func (u *initUI) FailStep(detail string) {
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

func (u *initUI) Info(msg string) {
	if u.quiet {
		return
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	u.stopSpinner(true)
	if u.enabled {
		fmt.Fprintln(u.out, msg)
		return
	}
	u.plain.Info(msg)
}

func (u *initUI) Warn(msg string) {
	if u.quiet {
		return
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	u.stopSpinner(true)
	if u.enabled {
		fmt.Fprintln(u.out, u.color.Wrap(tui.ColorYellow, msg))
		return
	}
	u.plain.Warn(msg)
}

func (u *initUI) printHeader() {
	label := "yeet init"
	remote := strings.TrimSpace(u.remote)
	host := strings.TrimSpace(u.host)
	if remote != "" {
		label = fmt.Sprintf("%s %s", label, remote)
	}
	if host != "" && host != remote {
		label = fmt.Sprintf("%s (host=%s)", label, host)
	}
	fmt.Fprintf(u.out, "[+] %s\n", strings.TrimSpace(label))
}

func (u *initUI) newSpinner(text string) *tui.Spinner {
	sp := tui.NewSpinner(u.out,
		tui.WithFrames(tui.DefaultFrames),
		tui.WithInterval(120*time.Millisecond),
		tui.WithColor(u.color, tui.ColorYellow),
		tui.WithHideCursor(true),
	)
	sp.Start(text)
	return sp
}

func (u *initUI) stopSpinner(clear bool) {
	u.mu.Lock()
	sp := u.spinner
	u.spinner = nil
	u.mu.Unlock()

	if sp == nil {
		return
	}
	sp.Stop(clear)
}

func (u *initUI) printStatus(status, name, detail string) {
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

type plainInitUI struct {
	out        io.Writer
	host       string
	remote     string
	service    string
	headerDone bool
	current    string
}

func newPlainInitUI(out io.Writer, host, remote, service string) *plainInitUI {
	return &plainInitUI{out: out, host: host, remote: remote, service: service}
}

func (p *plainInitUI) Header() {
	p.headerDone = true
}

func (p *plainInitUI) MarkHeaderDone() {
	p.headerDone = true
}

func (p *plainInitUI) Info(label string) {
	p.Header()
	fmt.Fprintln(p.out, p.line("info", "", label))
}

func (p *plainInitUI) Warn(label string) {
	p.Header()
	fmt.Fprintln(p.out, p.line("warn", "", label))
}

func (p *plainInitUI) StartStep(name string) {
	p.Header()
	p.current = name
	fmt.Fprintln(p.out, p.line("running", name, ""))
}

func (p *plainInitUI) DoneStep(detail string) {
	p.Header()
	if p.current == "" {
		return
	}
	fmt.Fprintln(p.out, p.line("ok", p.current, detail))
	p.current = ""
}

func (p *plainInitUI) FailStep(detail string) {
	p.Header()
	if p.current == "" {
		return
	}
	fmt.Fprintln(p.out, p.line("err", p.current, detail))
	p.current = ""
}

func (p *plainInitUI) line(status, step, detail string) string {
	parts := []string{
		"action", "init",
		"host", p.host,
		"remote", p.remote,
		"service", p.service,
		"status", status,
	}
	if step != "" {
		parts = append(parts, "step", step)
	}
	if detail != "" {
		parts = append(parts, "detail", detail)
	}
	return formatInitKV(parts...)
}

func formatInitKV(parts ...string) string {
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
		b.WriteString(quoteInitKV(val))
	}
	return b.String()
}

func quoteInitKV(val string) string {
	if initNeedsQuote(val) {
		return strconv.Quote(val)
	}
	return val
}

func initNeedsQuote(val string) bool {
	for _, r := range val {
		switch r {
		case ' ', '\t', '\n', '"', '=':
			return true
		}
	}
	return false
}

func humanReadableBytes(bts float64) string {
	const unit = 1024
	if bts <= unit {
		return fmt.Sprintf("%.2f B", bts)
	}
	const prefix = "KMGTPE"
	n := bts
	i := -1
	for n > unit {
		i++
		n = n / unit
	}

	return fmt.Sprintf("%.2f %cB", n, prefix[i])
}
