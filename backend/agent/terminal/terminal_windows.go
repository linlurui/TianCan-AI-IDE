//go:build windows

package terminal

import (
	"os"
	"syscall"
)

func setPgidAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}

func killProcessGroup(pid int) {
	// Windows has no process groups or syscall.Kill;
	// just kill the process directly via os.Process.Kill (TerminateProcess).
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
}
