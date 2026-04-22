//go:build !windows

package terminal

import "syscall"

func setPgidAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(pid int) {
	syscall.Kill(-pid, syscall.SIGTERM)
}
