package toolguard

import (
	"strings"
	"testing"
)

// TestDetector_NoRemindUnderThreshold 阈值以下的相同被拒调用不应触发提醒。
func TestDetector_NoRemindUnderThreshold(t *testing.T) {
	d := NewDetector(12, 3)
	args := map[string]interface{}{"command": "rm -rf /"}
	for i := 0; i < 2; i++ {
		if remind, _ := d.ShouldRemind("bash", args); remind {
			t.Fatalf("iteration %d: should NOT remind below threshold", i)
		}
		d.Record("bash", args, OutcomeDenied)
	}
}

// TestDetector_RemindAfterThreshold 达到阈值的相同被拒调用应触发提醒，且文案含工具名与标记。
func TestDetector_RemindAfterThreshold(t *testing.T) {
	d := NewDetector(12, 3)
	args := map[string]interface{}{"command": "rm -rf /"}
	for i := 0; i < 3; i++ {
		d.Record("bash", args, OutcomeDenied)
	}
	remind, msg := d.ShouldRemind("bash", args)
	if !remind {
		t.Fatalf("expected remind after 3 identical denied calls")
	}
	if !strings.Contains(msg, "bash") || !strings.Contains(msg, "重复调用提醒") {
		t.Fatalf("reminder missing key info: %q", msg)
	}
}

// TestDetector_DifferentArgsNoRemind 同一工具但不同参数 → 各自的重复计数互不干扰，
// 单个参数未达到阈值 → 不提醒。
func TestDetector_DifferentArgsNoRemind(t *testing.T) {
	d := NewDetector(12, 2)
	d.Record("bash", map[string]interface{}{"command": "ls /"}, OutcomeDenied)
	d.Record("bash", map[string]interface{}{"command": "rm -rf /"}, OutcomeDenied)
	d.Record("bash", map[string]interface{}{"command": "cat x"}, OutcomeDenied)
	// 单个参数 (bash, "ls /") 仅出现 1 次，未达阈值 2
	if remind, _ := d.ShouldRemind("bash", map[string]interface{}{"command": "ls /"}); remind {
		t.Fatalf("different-arg calls must not be treated as repeated")
	}
}

// TestDetector_DifferentToolNoRemind 不同工具即使参数相同也各自计数，
// 单个工具未达阈值 → 不提醒（证明 key 含工具名）。
func TestDetector_DifferentToolNoRemind(t *testing.T) {
	d := NewDetector(12, 2)
	args := map[string]interface{}{"path": "/etc/passwd"}
	d.Record("read", args, OutcomeDenied)
	d.Record("read", args, OutcomeDenied) // read 自身已达阈值
	d.Record("write", args, OutcomeDenied) // write 仅 1 次
	// write 的计数独立于 read，未达阈值
	if remind, _ := d.ShouldRemind("write", args); remind {
		t.Fatalf("different tools must not share the repeat count")
	}
}

// TestDetector_RepeatedSuccessNoRemind 反证：相同参数的"成功"调用不应被提醒——
// 只针对被拒/失败的重试，避免对合法重复读取误报。
func TestDetector_RepeatedSuccessNoRemind(t *testing.T) {
	d := NewDetector(12, 2)
	args := map[string]interface{}{"path": "README.md"}
	for i := 0; i < 4; i++ {
		d.Record("read", args, OutcomeOK)
	}
	if remind, _ := d.ShouldRemind("read", args); remind {
		t.Fatalf("repeated successful calls must NOT trigger a reminder")
	}
}

// TestDetector_DistinctToolsNoRemind 反证：N 个不同工具各调用一次 → 不触发提醒，
// 证明提醒基于单工具单参数的重复，而非全局调用次数。
func TestDetector_DistinctToolsNoRemind(t *testing.T) {
	d := NewDetector(12, 3)
	d.Record("read", map[string]interface{}{"a": 1}, OutcomeDenied)
	d.Record("write", map[string]interface{}{"b": 2}, OutcomeDenied)
	d.Record("grep", map[string]interface{}{"c": 3}, OutcomeDenied)
	if remind, _ := d.ShouldRemind("read", map[string]interface{}{"a": 1}); remind {
		t.Fatalf("a single call per distinct tool must not be flagged as hammering")
	}
}

// TestDetector_PersistsAfterReminder 提醒一旦触发，继续记录为 OutcomeReminded，
// 后续相同调用仍被提醒（维持重定向，不回退到裸拒绝）。
func TestDetector_PersistsAfterReminder(t *testing.T) {
	d := NewDetector(12, 2)
	args := map[string]interface{}{"command": "rm -rf /"}
	d.Record("bash", args, OutcomeDenied)
	d.Record("bash", args, OutcomeDenied)
	remind1, _ := d.ShouldRemind("bash", args)
	if !remind1 {
		t.Fatalf("expected first reminder")
	}
	d.Record("bash", args, OutcomeReminded)
	remind2, _ := d.ShouldRemind("bash", args)
	if !remind2 {
		t.Fatalf("reminder should persist after being redirected once")
	}
}
