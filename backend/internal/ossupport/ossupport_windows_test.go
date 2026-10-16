//go:build windows

package ossupport

import (
	"os"
	"testing"
)

// TestProcessGroupIDReturnsPidOnWindows locks the Windows contract: there is no
// POSIX process group, so the helper returns the pid itself.
func TestProcessGroupIDReturnsPidOnWindows(t *testing.T) {
	got, err := ProcessGroupID(os.Getpid())
	if err != nil {
		t.Fatalf("ProcessGroupID returned error: %v", err)
	}
	if got != os.Getpid() {
		t.Fatalf("expected pgid == pid (%d), got %d", os.Getpid(), got)
	}
}
