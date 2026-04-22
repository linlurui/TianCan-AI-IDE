package ai

import "syscall"

func hideWindowAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}

func setPgidAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
