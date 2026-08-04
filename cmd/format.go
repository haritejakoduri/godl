package cmd

import (
	"fmt"
	"time"

	"godl/internal/store"
)

func humanBytes(n int64) string {
	if n < 0 {
		return "?"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func humanSpeed(bps float64) string {
	if bps <= 0 {
		return "-"
	}
	return humanBytes(int64(bps)) + "/s"
}

func humanETA(seconds int64, status store.JobStatus) string {
	if status != store.StatusActive || seconds < 0 {
		return "-"
	}
	d := time.Duration(seconds) * time.Second
	if d > 99*time.Hour {
		return ">99h"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func percent(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	p := float64(done) / float64(total)
	if p > 1 {
		p = 1
	}
	if p < 0 {
		p = 0
	}
	return p
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
