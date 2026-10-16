package toolguard

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Outcome is the recorded result of a tool call, as seen by the detector.
type Outcome int

const (
	OutcomeOK Outcome = iota
	OutcomeDenied
	OutcomeFailed
	OutcomeReminded
)

type callRec struct {
	key     string
	outcome Outcome
}

// Detector tracks recent tool calls to catch a model hammering the same rejected
// call, and produces a redirect reminder.
//
// KV-cache / loop-safety contract: the detector only inspects (tool, args) and
// never serializes the full conversation, so it can be called every tool
// iteration without touching the model context.
type Detector struct {
	mu        sync.Mutex
	window    int
	threshold int
	recent    []callRec
}

// NewDetector builds a Detector. window<=0 defaults to 12, threshold<=0 defaults
// to 3 — the (threshold+1)-th identical rejected call is redirected to a reminder
// (i.e. the model gets a few real denials before the redirect kicks in).
func NewDetector(window, threshold int) *Detector {
	if window <= 0 {
		window = 12
	}
	if threshold <= 0 {
		threshold = 3
	}
	return &Detector{window: window, threshold: threshold}
}

func keyOf(toolName string, args map[string]interface{}) string {
	ah, _ := json.Marshal(args)
	return toolName + "\x00" + string(ah)
}

// Record stores the outcome of a tool call so future identical calls can be
// judged. The recent window is bounded to avoid unbounded growth.
func (d *Detector) Record(toolName string, args map[string]interface{}, o Outcome) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.recent = append(d.recent, callRec{key: keyOf(toolName, args), outcome: o})
	if len(d.recent) > d.window {
		d.recent = d.recent[len(d.recent)-d.window:]
	}
}

// ShouldRemind reports whether this identical call should be redirected because
// the model keeps hammering a rejected call. It inspects the most recent prior
// record with the same (tool,args): if the same key has been attempted at least
// `threshold` times AND the latest prior attempt was a rejection (denied/failed/
// reminded), it returns true with a reminder message.
//
// It does NOT record the call — callers must Record the actual outcome afterwards
// (typically OutcomeReminded when this returns true, so the redirect persists).
func (d *Detector) ShouldRemind(toolName string, args map[string]interface{}) (bool, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := keyOf(toolName, args)
	count := 0
	last := Outcome(-1)
	for i := len(d.recent) - 1; i >= 0; i-- {
		if d.recent[i].key == key {
			count++
			if last == -1 {
				last = d.recent[i].outcome
			}
		}
	}
	if count < d.threshold || last == OutcomeOK {
		return false, ""
	}
	verb := "失败"
	switch last {
	case OutcomeDenied:
		verb = "被拒绝"
	case OutcomeReminded:
		verb = "被提醒后仍未改变策略"
	}
	msg := fmt.Sprintf("[重复调用提醒] 工具「%s」在最近 %d 次调用中以完全相同的参数被重复调用了 %d 次，且最近一次%s。"+
		"请停止反复重试同一调用，改为先说明你想要达成的目标，再换一种策略（如先检查前置条件、缩小范围或拆分子任务）。",
		toolName, d.window, count, verb)
	return true, msg
}
