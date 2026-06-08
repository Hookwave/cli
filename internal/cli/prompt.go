package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hookwave/cli/internal/output"
)

// promptReader is a tiny stdin wrapper for the init flow. We avoid
// pulling a heavyweight prompt lib because init's needs are modest:
// "type a value, hit enter" + "pick from a list."
type promptReader struct {
	r *bufio.Reader
}

func newPromptReader() *promptReader {
	return &promptReader{r: bufio.NewReader(os.Stdin)}
}

// askDefault prints `Question [default]: ` and returns the user's
// response (or default on empty).
func (p *promptReader) askDefault(out *output.Printer, question, def string) (string, error) {
	out.Printf(output.None, "%s ", question)
	out.Printf(output.Muted, "[%s]", def)
	out.Printf(output.None, ": ")
	line, err := p.readLine()
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// choose prints a numbered list and accepts either a number or the
// option text. Empty input returns the default.
func (p *promptReader) choose(out *output.Printer, question string, options []string, def string) (string, error) {
	out.Printf(output.None, "%s\n", question)
	for i, o := range options {
		marker := " "
		if o == def {
			marker = "·"
		}
		out.Printf(output.None, "  %s %d) %s\n", marker, i+1, o)
	}
	out.Printf(output.None, "Choose ")
	if def != "" {
		out.Printf(output.Muted, "[%s]", def)
	}
	out.Printf(output.None, ": ")
	line, err := p.readLine()
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	// Numeric pick?
	for i, o := range options {
		if line == fmt.Sprintf("%d", i+1) {
			return o, nil
		}
		if strings.EqualFold(line, o) {
			return o, nil
		}
	}
	return "", fmt.Errorf("%q is not one of: %s", line, strings.Join(options, ", "))
}

func (p *promptReader) readLine() (string, error) {
	s, err := p.r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return s, nil
}
