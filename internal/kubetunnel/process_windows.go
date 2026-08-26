//go:build windows

package kubetunnel

import "os/exec"

func configureTunnelProcess(_ *exec.Cmd) {}

func terminateTunnelProcess(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
