// Package conversation implements project-scoped, persistent AI sessions.
//
// It is the P0 layer of the "intelligent memory / context" roadmap:
//
//	P0  项目级会话持久化 —— 消息不再只活在前端内存，后端按项目落盘。
//	P1  结构化状态分区   —— 段落(segment) / 需求点(requirement) / 红线(redline)。
//	P2  语义召回         —— 见 backend/agent/recall。
//
// Design: a session IS an event log. Instead of rewriting a big JSON blob on every
// message (the old backend/agent/session.go behaviour, which loses history and
// cannot be audited), every change is appended as an immutable journal event and
// the in-memory state is the pure fold of that log. That gives us, for free:
//
//   - crash safety: reopen the file and replay;
//   - anchors: periodic state snapshots, so recovery starts "one level down";
//   - audit: the full event stream is the audit trail (who said what, when);
//   - rollback: appending a rollback event truncates the projection while keeping
//     the audit trail intact (we never rewrite history, we append to it).
//
// The package imports only the standard library plus backend/agent/journal, so it
// follows the leaf-package discipline (no import cycles, trivially unit-testable).
package conversation

import (
	"encoding/json"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/journal"
)

// Event kinds. These are the vocabulary of a session's history; each kind has a
// matching payload type below and a branch in SessionState.Apply.
const (
	KindMessage   = "msg"       // a chat message (user / assistant / tool / system)
	KindSegOpen   = "seg.open"  // a new work segment (task slice) starts
	KindSegClose  = "seg.close" // a segment is finished and summarised
	KindReqAdd    = "req.add"   // a requirement point was captured
	KindReqDone   = "req.done"  // a requirement point is satisfied
	KindRedAdd    = "red.add"   // a red line (hard constraint) was recorded
	KindRollback  = "rollback"  // roll the projection back to an earlier seq
	KindRecallHit = "recall"    // a semantic recall happened (audit trail)
	KindTitle     = "title"     // session title was (re)set
)

// Role mirrors the chat roles used by the AI service. Kept as a plain string so
// the payload stays pure JSON without importing the agent types package.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSystem    Role = "system"
)

// MessagePayload is the payload of KindMessage.
type MessagePayload struct {
	Role      Role   `json:"role"`
	Content   string `json:"content"`
	SegmentID string `json:"seg,omitempty"` // which segment this message belongs to
}

// SegOpenPayload is the payload of KindSegOpen.
type SegOpenPayload struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// SegClosePayload is the payload of KindSegClose.
type SegClosePayload struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

// ReqAddPayload is the payload of KindReqAdd.
type ReqAddPayload struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Source   string `json:"source,omitempty"`   // "user" | "extracted" | "recall"
	Severity string `json:"severity,omitempty"` // for redlines: "hard" | "soft"
}

// ReqDonePayload is the payload of KindReqDone.
type ReqDonePayload struct {
	ID string `json:"id"`
}

// RollbackPayload is the payload of KindRollback. The projection is truncated to
// seq To; the event itself stays in the log so the rollback is auditable.
type RollbackPayload struct {
	To   int64  `json:"to"`
	Note string `json:"note,omitempty"`
}

// RecallPayload is the payload of KindRecallHit — recorded so a later audit can
// explain WHY a piece of old context was pulled into the prompt.
type RecallPayload struct {
	Query string    `json:"query"`
	Seqs  []int64   `json:"seqs"`
	Score []float64 `json:"score,omitempty"`
}

// TitlePayload is the payload of KindTitle.
type TitlePayload struct {
	Title string `json:"title"`
}

// newID builds a short, sortable, collision-resistant id from the wall clock and
// a monotonic counter, e.g. "m-1l2x-3". Ids are opaque to callers.
func newID(prefix string, salt int64) string {
	return prefix + "-" + formatBase36(time.Now().UnixMilli()) + "-" + formatBase36(salt)
}

func formatBase36(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%36]
		v /= 36
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// decodePayload is a small helper used by the state machine: it unmarshals an
// event payload into the typed struct for its kind.
func decodePayload(e journal.Event, out interface{}) error {
	if len(e.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(e.Payload, out)
}
