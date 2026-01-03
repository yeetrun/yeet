// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"tailscale.com/tstime/rate"
)

const uploadProgressInterval = 120 * time.Millisecond

func uploadCatchBinary(ui *initUI, bin string, binSize int64, userAtRemote string) (string, error) {
	file, err := os.Open(bin)
	if err != nil {
		return "", fmt.Errorf("failed to open catch binary: %w", err)
	}
	defer file.Close()

	progress := newUploadProgress(binSize)
	reader := progress.reader(file)

	cmd := exec.Command("ssh", "-q", "-C", userAtRemote, "cat > ./catch")
	cmd.Stdin = reader
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(uploadProgressInterval)
		defer ticker.Stop()
		ui.UpdateDetail(progress.detail())
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				ui.UpdateDetail(progress.detail())
			}
		}
	}()

	err = cmd.Run()
	close(done)
	if err != nil {
		return "", err
	}
	return progress.finalDetail(), nil
}

type uploadProgress struct {
	total   int64
	sent    atomic.Int64
	rateVal rate.Value
	start   time.Time
}

func newUploadProgress(total int64) *uploadProgress {
	return &uploadProgress{
		total: total,
		rateVal: rate.Value{
			HalfLife: 250 * time.Millisecond,
		},
		start: time.Now(),
	}
}

func (p *uploadProgress) add(n int) {
	if n <= 0 {
		return
	}
	p.sent.Add(int64(n))
	p.rateVal.Add(float64(n))
}

func (p *uploadProgress) reader(r io.Reader) io.Reader {
	return &uploadProgressReader{r: r, progress: p}
}

func (p *uploadProgress) detail() string {
	sent := float64(p.sent.Load())
	total := float64(p.total)
	rate := p.rateVal.Rate()
	return formatUploadDetail(sent, total, rate)
}

func (p *uploadProgress) finalDetail() string {
	var total float64
	if p.total > 0 {
		total = float64(p.total)
	} else {
		total = float64(p.sent.Load())
	}
	if total <= 0 {
		return ""
	}
	detail := humanReadableBytes(total)
	elapsed := time.Since(p.start)
	if elapsed > 0 {
		rate := total / elapsed.Seconds()
		if rate > 0 {
			detail = fmt.Sprintf("%s @ %s/s", detail, humanReadableBytes(rate))
		}
	}
	return detail
}

type uploadProgressReader struct {
	r        io.Reader
	progress *uploadProgress
}

func (r *uploadProgressReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.progress.add(n)
	}
	return n, err
}

func formatUploadDetail(sent, total, rate float64) string {
	if sent < 0 {
		sent = 0
	}
	if total > 0 && sent > total {
		sent = total
	}

	var b strings.Builder
	if total > 0 {
		percent := (sent / total) * 100
		if percent > 100 {
			percent = 100
		}
		fmt.Fprintf(&b, "%3.0f%% %s/%s", percent, humanReadableBytes(sent), humanReadableBytes(total))
	} else {
		b.WriteString(humanReadableBytes(sent))
	}

	if rate > 0 {
		fmt.Fprintf(&b, " @ %s/s", humanReadableBytes(rate))
		if total > 0 {
			remaining := total - sent
			if remaining < 0 {
				remaining = 0
			}
			eta := time.Duration(remaining/rate*float64(time.Second) + 0.5)
			if eta < 0 {
				eta = 0
			}
			fmt.Fprintf(&b, " ETA %s", formatShortDuration(eta))
		}
	}

	return strings.TrimSpace(b.String())
}

func formatShortDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	seconds := int64(d.Seconds() + 0.5)
	hours := seconds / 3600
	seconds %= 3600
	minutes := seconds / 60
	seconds %= 60

	if hours > 0 {
		return fmt.Sprintf("%dh%02dm", hours, minutes)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}
