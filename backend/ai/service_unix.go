//go:build !windows

package ai

import "syscall"

func hideWindowAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

func setPgidAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
