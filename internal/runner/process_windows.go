//go:build windows

package runner

import "os/exec"

func configureProcessGroup(_ *exec.Cmd) {}

// os.Process.Kill is the only portable non-interactive termination primitive
// available to a Windows service. CommandContext provides the same behavior.
func terminateProcessGroup(command *exec.Cmd, _ bool) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
