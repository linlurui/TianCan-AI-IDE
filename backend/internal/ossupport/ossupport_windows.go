//go:build windows

// Package ossupport abstracts the small set of POSIX-only process-group
// operations that the backend relies on to run child processes (debuggers,
// linters, shells, ...) and to terminate them as a group.
//
// Windows has no POSIX process groups. Each child is started without any
// special group placement; ProcessGroupID returns the process id itself so the
// same call sites keep working, and KillProcessGroup terminates the process
// tree rooted at that id via taskkill. The goal of this build tag is a clean
// cross-compile, not behavioral parity with POSIX process groups.
package ossupport

import (
	"os/exec"
	"strconv"
	"syscall"
)

// SetProcessGroup is a no-op on Windows: there is no Setpgid concept in
// syscall.SysProcAttr.
func SetProcessGroup(cmd *exec.Cmd) {}

// ProcessGroupID returns pid itself on Windows, since there is no separate
// process group id to query.
func ProcessGroupID(pid int) (int, error) {
	return pid, nil
}

// KillProcessGroup terminates the process tree rooted at pid on Windows using
// taskkill. The sig argument is accepted for API parity but unused, because
// Windows process termination is not signal-driven.
func KillProcessGroup(pid int, sig syscall.Signal) error {
	return exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
}
