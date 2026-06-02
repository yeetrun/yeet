// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"tailscale.com/tstime/rate"
)

const byteProgressInterval = 120 * time.Millisecond

type byteProgress struct {
	total   atomic.Int64
	seen    atomic.Int64
	rateVal rate.Value
	start   time.Time
}

func newByteProgress(total int64) *byteProgress {
	p := &byteProgress{
		rateVal: rate.Value{
			HalfLife: 250 * time.Millisecond,
		},
		start: time.Now(),
	}
	if total > 0 {
		p.total.Store(total)
	}
	return p
}

func (p *byteProgress) add(n int) {
	if p == nil || n <= 0 {
		return
	}
	p.seen.Add(int64(n))
	p.rateVal.Add(float64(n))
}

func (p *byteProgress) reader(r io.Reader) io.Reader {
	if p == nil {
		return r
	}
	return &byteProgressReader{r: r, progress: p}
}

func (p *byteProgress) detail() string {
	if p == nil {
		return ""
	}
	seen := float64(p.seen.Load())
	total := float64(p.total.Load())
	rate := p.rateVal.Rate()
	return formatByteProgressDetail(seen, total, rate)
}

func (p *byteProgress) finalDetail() string {
	if p == nil {
		return ""
	}
	total := float64(p.total.Load())
	seen := float64(p.seen.Load())
	if seen > 0 {
		total = seen
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

type byteProgressReader struct {
	r        io.Reader
	progress *byteProgress
}

func (r *byteProgressReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.progress.add(n)
	}
	return n, err
}

func formatByteProgressDetail(seen, total, rate float64) string {
	if seen < 0 {
		seen = 0
	}
	if total > 0 && seen > total {
		seen = total
	}

	var b strings.Builder
	if total > 0 {
		percent := (seen / total) * 100
		if percent > 100 {
			percent = 100
		}
		fmt.Fprintf(&b, "%3.0f%% %s/%s", percent, humanReadableBytes(seen), humanReadableBytes(total))
	} else {
		b.WriteString(humanReadableBytes(seen))
	}

	if rate > 0 {
		fmt.Fprintf(&b, " @ %s/s", humanReadableBytes(rate))
		if total > 0 {
			remaining := total - seen
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
