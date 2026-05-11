package tui

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// copyToClipboard writes s to the OS clipboard. On macOS uses pbcopy,
// on Linux tries xclip then wl-copy, on Windows uses clip. Returns
// the first error if no backend is available.
func copyToClipboard(s string) error {
	switch runtime.GOOS {
	case "darwin":
		return runWithStdin("pbcopy", []string{}, s)
	case "windows":
		return runWithStdin("clip", []string{}, s)
	default:
		// Linux / BSD — try xclip first, then wl-copy.
		if err := runWithStdin("xclip", []string{"-selection", "clipboard"}, s); err == nil {
			return nil
		}
		if err := runWithStdin("wl-copy", []string{}, s); err == nil {
			return nil
		}
		return errors.New("no clipboard backend (install xclip or wl-clipboard)")
	}
}

func runWithStdin(name string, args []string, stdin string) error {
	c := exec.Command(name, args...)
	c.Stdin = strings.NewReader(stdin)
	return c.Run()
}

// openInBrowser launches the user's default browser at url. Same
// detection pattern as the login command — kept local to avoid a
// cross-package dependency.
func openInBrowser(url string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait() }()
	return nil
}
