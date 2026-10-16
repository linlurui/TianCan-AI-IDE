//go:build !windows

// Package ossupport abstracts the small set of POSIX-only process-group
// operations that the backend relies on to run child processes (debuggers,
// linters, shells, ...) and to terminate them as a group.
//
// On POSIX systems this is a thin, behavior-preserving wrapper around the
// original syscall calls; the unix implementation must stay byte-equivalent
// to the inline code it replaced.
package ossupport

import (
	"os/exec"
	"syscall"
)

// SetProcessGroup configures cmd so that the started child is placed into its
// own process group. This lets the caller later signal the whole group (the
// child plus any of its own descendants) with a single kill.
func SetProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// ProcessGroupID returns the id of the process group that pid belongs to.
func ProcessGroupID(pid int) (int, error) {
	return syscall.Getpgid(pid)
}

// KillProcessGroup sends sig to the process group identified by pgid.
// The negative sign mirrors the POSIX convention for signalling a group.
func KillProcessGroup(pgid int, sig syscall.Signal) error {
	return syscall.Kill(-pgid, sig)
}
