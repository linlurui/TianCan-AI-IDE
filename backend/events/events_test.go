package events

import (
	"sync"
	"testing"
)

// TestRecorder 表驱动验证 Recorder 的记录/统计契约。
func TestRecorder(t *testing.T) {
	rec := NewRecorder()
	rec.Emit("a", 1)
	rec.Emit("b", nil)
	rec.Emit("a", 2)

	if got := len(rec.Events()); got != 3 {
		t.Fatalf("Events len=%d want 3", got)
	}
	if rec.Count("a") != 2 || rec.Count("b") != 1 || rec.Count("c") != 0 {
		t.Fatalf("Count mismatch: a=%d b=%d c=%d", rec.Count("a"), rec.Count("b"), rec.Count("c"))
	}
	if rec.Events()[0].Payload != 1 {
		t.Fatalf("payload not preserved: %+v", rec.Events()[0])
	}
}

// TestRecorder_ConcurrentRace 并发发射不丢不重（-race 下验证线程安全）。
func TestRecorder_ConcurrentRace(t *testing.T) {
	rec := NewRecorder()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				rec.Emit("evt", j)
			}
		}()
	}
	wg.Wait()
	if got := rec.Count("evt"); got != 800 {
		t.Fatalf("Count=%d want 800 (events lost under concurrency)", got)
	}
}

// TestSetDefault 验证默认 Sink 注入与恢复。
func TestSetDefault(t *testing.T) {
	rec := NewRecorder()
	restore := SetDefault(rec)
	Default().Emit("d1", nil)
	restore()
	Default().Emit("d2", nil) // 恢复后应为 Nop，不再记录

	if rec.Count("d1") != 1 {
		t.Fatalf("default sink did not receive d1")
	}
	// 反证：恢复后 d2 必须不可达——若 SetDefault 的恢复是假的，这里会抓到
	if rec.Count("d2") != 0 {
		t.Fatalf("restore broken: d2 leaked into recorder")
	}
}

// TestSetDefault_NilResetsToNop 反证测试：传 nil 必须回到静默而非 panic。
func TestSetDefault_NilResetsToNop(t *testing.T) {
	restore := SetDefault(nil)
	defer restore()
	// 不应 panic
	Default().Emit("anything", nil)
	if _, ok := Default().(nopSink); !ok {
		t.Fatal("SetDefault(nil) should reset to nopSink")
	}
}

// TestFuncAdapter 验证函数适配器。
func TestFuncAdapter(t *testing.T) {
	var got []string
	var s Sink = Func(func(e string, p interface{}) { got = append(got, e) })
	s.Emit("x", nil)
	s.Emit("y", nil)
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("Func adapter broken: %v", got)
	}
}
