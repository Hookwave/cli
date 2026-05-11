// Package output centralises terminal-aware printing. Auto-disables
// color when stdout is not a TTY, when NO_COLOR is set, or when
// HOOKWAVE_NO_COLOR is set. Centralised so we don't sprinkle ANSI
// codes across the codebase.
package output

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Tone classifies output severity for color selection. Plain "Print"
// text uses no tone.
type Tone int

const (
	None Tone = iota
	Success
	Warn
	Error
	Muted
)

// Printer wraps an io.Writer with color awareness.
type Printer struct {
	w     io.Writer
	color bool
}

// NewStdout returns a Printer that writes to os.Stdout with color
// auto-detected.
func NewStdout() *Printer { return new(os.Stdout) }

// NewStderr returns a Printer that writes to os.Stderr with color
// auto-detected.
func NewStderr() *Printer { return new(os.Stderr) }

func new(f *os.File) *Printer {
	return &Printer{w: f, color: shouldColor(f)}
}

func shouldColor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("HOOKWAVE_NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	return term.IsTerminal(int(f.Fd()))
}

// Println writes a line, optionally tone-colored.
func (p *Printer) Println(tone Tone, args ...any) {
	s := fmt.Sprint(args...)
	fmt.Fprintln(p.w, p.tint(tone, s))
}

// Printf writes formatted text, optionally tone-colored. No newline.
func (p *Printer) Printf(tone Tone, format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	fmt.Fprint(p.w, p.tint(tone, s))
}

// Plain writes raw text with no tinting (handy for tabular output).
func (p *Printer) Plain(s string) { fmt.Fprint(p.w, s) }

func (p *Printer) tint(tone Tone, s string) string {
	if !p.color || tone == None {
		return s
	}
	prefix, suffix := codes(tone)
	if prefix == "" {
		return s
	}
	// Avoid coloring trailing newlines so terminals keep cursor color sane.
	trimmed := strings.TrimRight(s, "\n")
	tail := s[len(trimmed):]
	return prefix + trimmed + suffix + tail
}

func codes(t Tone) (string, string) {
	const reset = "\x1b[0m"
	switch t {
	case Success:
		return "\x1b[32m", reset // green
	case Warn:
		return "\x1b[33m", reset // yellow
	case Error:
		return "\x1b[31m", reset // red
	case Muted:
		return "\x1b[2m", reset // dim
	}
	return "", ""
}

// Stylize returns the input wrapped in ANSI for the given tone, or
// untouched when color is disabled. Useful for inline highlights.
func (p *Printer) Stylize(tone Tone, s string) string { return p.tint(tone, s) }
