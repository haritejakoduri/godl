package cmd

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// statusLine prints the running commentary from a long install —
// downloading yt-dlp, unpacking ffmpeg — in a way that suits wherever
// it is going.
//
// On a terminal each line replaces the one before it, so a progress
// bar moves in place instead of scrolling a thousand copies of itself
// past. Anywhere else — a pipe, a file, CI — the same lines are
// printed one per line, because carriage returns in a log are just
// noise, and the throttling in internal/progress already keeps that
// readable.
//
// Finish must be called before printing anything else, since on a
// terminal the last line was left without a newline.
type statusLine struct {
	w      io.Writer
	tty    bool
	indent string
	open   bool // a line is on screen with no newline after it
}

func newStatusLine(indent string) *statusLine {
	return &statusLine{
		w:      os.Stdout,
		tty:    term.IsTerminal(int(os.Stdout.Fd())),
		indent: indent,
	}
}

func (s *statusLine) Print(msg string) {
	if !s.tty {
		fmt.Fprintln(s.w, s.indent+msg)
		return
	}
	// \r returns to the start, \033[2K clears what was there — needed
	// because the new line can be shorter than the old one, and
	// without it the tail of the previous line stays on screen.
	fmt.Fprint(s.w, "\r\033[2K"+s.indent+msg)
	s.open = true
}

func (s *statusLine) Finish() {
	if s.open {
		fmt.Fprintln(s.w)
		s.open = false
	}
}
