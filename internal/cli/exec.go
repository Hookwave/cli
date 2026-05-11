package cli

import "os/exec"

// runDetached starts a process without waiting for it. Used to open
// the browser without blocking the CLI.
func runDetached(name string, args ...string) error {
	c := exec.Command(name, args...)
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait() }()
	return nil
}
