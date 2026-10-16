//go:build !windows

package ossupport

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// TestSetProcessGroupMustPlaceChildInOwnGroup locks the invariant that the
// unix wrapper is behavior-preserving: it must request a new process group via
// Setpgid, exactly like the inline syscall code it replaced.
func TestSetProcessGroupMustPlaceChildInOwnGroup(t *testing.T) {
	cmd := exec.Command("true")
	SetProcessGroup(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("SetProcessGroup left SysProcAttr nil")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Fatal("SetProcessGroup did not set Setpgid=true")
	}
}

// TestProcessGroupIDReturnsValidGroup verifies the thin Getpgid wrapper returns
// the same value as calling syscall.Getpgid directly, proving it is a faithful
// wrapper (and not, say, returning the pid).
func TestProcessGroupIDReturnsValidGroup(t *testing.T) {
	pid := os.Getpid()
	want, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("syscall.Getpgid returned error: %v", err)
	}
	if want <= 0 {
		t.Fatalf("syscall.Getpgid returned non-positive pgid: %d", want)
	}
	got, err := ProcessGroupID(pid)
	if err != nil {
		t.Fatalf("ProcessGroupID returned error: %v", err)
	}
	if got != want {
		t.Fatalf("ProcessGroupID=%d, want syscall.Getpgid=%d", got, want)
	}
}

// TestKillProcessGroupPropagatesSignal is a negative-control: passing a
// non-existent group must surface an error rather than silently succeeding,
// proving the syscall.Kill(-pgid, sig) path is really exercised.
func TestKillProcessGroupPropagatesSignal(t *testing.T) {
	// A pgid that is extremely unlikely to exist; ESRCH is the expected outcome.
	_, err := ProcessGroupID(os.Getpid())
	_ = err
	if err := KillProcessGroup(999999999, syscall.SIGKILL); err == nil {
		t.Fatal("KillProcessGroup on a missing group unexpectedly succeeded")
	}
}
