package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// Piped: plain lines. Carriage returns in a log or a CI transcript are
// noise, and anything parsing the output would have to strip them.
func TestStatusLineWhenNotATerminal(t *testing.T) {
	var buf bytes.Buffer
	s := &statusLine{w: &buf, tty: false, indent: "  "}
	s.Print("downloading 10%")
	s.Print("downloading 90%")
	s.Finish()

	got := buf.String()
	if strings.ContainsAny(got, "\r\033") {
		t.Errorf("output = %q, want no carriage returns or escapes when piped", got)
	}
	if lines := strings.Count(got, "\n"); lines != 2 {
		t.Errorf("output = %q: %d lines, want one per Print", got, lines)
	}
	if !strings.Contains(got, "  downloading 10%") {
		t.Errorf("output = %q, want the indent applied", got)
	}
}

// A terminal: each line replaces the one before, so a bar moves in
// place. The clear matters as much as the carriage return — without
// it a shorter line leaves the tail of the longer one behind.
func TestStatusLineOnATerminal(t *testing.T) {
	var buf bytes.Buffer
	s := &statusLine{w: &buf, tty: true, indent: "  "}
	s.Print("downloading a very long line indeed")
	s.Print("done")

	got := buf.String()
	if strings.Count(got, "\r") != 2 {
		t.Errorf("output = %q, want a carriage return before each line", got)
	}
	if strings.Count(got, "\033[2K") != 2 {
		t.Errorf("output = %q, want the line cleared each time, or a shorter line leaves the previous tail on screen", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("output = %q, want no newline until Finish", got)
	}
}

// Finish closes the open line, so whatever prints next does not land
// on the end of the progress line.
func TestStatusLineFinish(t *testing.T) {
	var buf bytes.Buffer
	s := &statusLine{w: &buf, tty: true, indent: ""}
	s.Print("working")
	s.Finish()
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("output = %q, want a trailing newline after Finish", buf.String())
	}

	// Nothing printed means nothing to close — no stray blank line.
	var buf2 bytes.Buffer
	s2 := &statusLine{w: &buf2, tty: true, indent: ""}
	s2.Finish()
	if buf2.Len() != 0 {
		t.Errorf("Finish with nothing printed wrote %q, want nothing", buf2.String())
	}

	// And it is idempotent: two Finishes must not add two newlines.
	s.Finish()
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Errorf("two Finishes produced %d newlines, want 1", n)
	}
}
