// Package journal implements the Session Event Log + Projection framework — the
// foundation (地基) for 流 G's 路线 A main-loop rework (G15).
//
// Core idea (per the plan §3 / G14): "状态 = 事件日志的纯折叠" — state is the pure
// fold of an append-only event log. A Projector keeps the live state in memory and
// folds every new event into it in place. The full state can always be rebuilt by
// replaying the log from scratch (or, more cheaply, from the most recent anchor —
// "低一格锚点恢复"), which is the basis for replay / fork / crash recovery.
//
// Invariants enforced by this package (each has a 反证 test):
//
//  1. apply 必须返回同一指针. State.Apply must mutate the receiver IN PLACE and
//     return the SAME pointer. The Projector detects (and rejects) a projection
//     that allocates a fresh value, so a forked state pointer can never silently
//     diverge from the log.
//  2. state 纯 JSON. State.PureJSON -> LoadJSON -> PureJSON must be a stable
//     round-trip, so any snapshot (anchor) is a faithful, serializable checkpoint.
//  3. refs 计数 + 低一格锚点恢复. Events carry a reference count; an Anchor
//     snapshots the projected state at a seq and bumps the refs of the events it
//     depends on, so events no longer needed by any anchor (or the live state)
//     can be pruned without losing recoverability. Recovery rebuilds state from the
//     newest anchor and replays only the tail after it.
//
// The package depends only on the standard library and on no other tiancan package,
// so it follows the leaf-package discipline (no import cycles, easy to unit-test).
package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
)

// Event is one immutable entry in the session event log. It is pure JSON: every
// field is exported and json-serializable, and Payload is an opaque json.RawMessage
// so arbitrary event kinds can be stored without losing fidelity.
type Event struct {
	Seq     int64           `json:"seq"`               // monotonic, assigned by Log.Append
	Kind    string          `json:"kind"`              // event kind, e.g. "turn.begin", "tool.call"
	Payload json.RawMessage `json:"payload,omitempty"` // kind-specific data
	Time    int64           `json:"t"`                 // unix millis, assigned by the Log
	Refs    int32           `json:"refs"`              // reference count (see Log anchors / Prune)
}

// State is a value that can be folded from events by a projection.
//
// Contract:
//   - Apply folds e INTO THE RECEIVER in place and returns the receiver (the SAME
//     pointer). Returning a freshly-allocated value is a contract violation and is
//     detected by Projector.AppendAndApply / Projector.Recover.
//   - PureJSON must return a canonical pure-JSON encoding of the receiver.
//     LoadJSON must restore the receiver losslessly from that encoding. Round
//     tripping must be stable: PureJSON(LoadJSON(PureJSON(s))) == PureJSON(s).
//
// Implementations MUST be pointer types (so "same pointer" is meaningful).
type State interface {
	Apply(e Event) State
	PureJSON() ([]byte, error)
	LoadJSON(data []byte) error
}

// samePointer reports whether a and b are the same underlying pointer. State
// implementations must be pointers, so a non-pointer receiver (or a mismatched
// type) is reported as "different" and rejected by the Projector.
func samePointer(a, b State) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	va := reflect.ValueOf(a)
	vb := reflect.ValueOf(b)
	if va.Type() != vb.Type() || va.Kind() != reflect.Ptr {
		return false
	}
	return va.Pointer() == vb.Pointer()
}

// Anchor is a periodic snapshot of the projected state at a point in the log. It
// lets recovery start "one level down" (from this anchor) instead of replaying the
// entire log from seq 1.
type Anchor struct {
	Seq       int64           `json:"seq"`       // head seq covered by this anchor
	CoverFrom int64           `json:"coverFrom"` // first seq this anchor depends on (exclusive)
	Snapshot  json.RawMessage `json:"snapshot"`  // PureJSON() of the state at Seq
}

// Log is an append-only, concurrency-safe session event log.
type Log struct {
	mu      sync.Mutex
	events  []Event
	seq     int64
	anchors []Anchor // stack, oldest first
}

// NewLog returns an empty log.
func NewLog() *Log { return &Log{} }

// Append creates and appends a new event with the given kind and payload. payload
// may be nil, a json.RawMessage, or any value that json.Marshal can encode. The
// Log assigns the monotonic Seq and the timestamp.
func (l *Log) Append(kind string, payload interface{}) (Event, error) {
	raw, err := marshalPayload(payload)
	if err != nil {
		return Event{}, fmt.Errorf("journal: marshal payload: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e := Event{
		Seq:     l.seq,
		Kind:    kind,
		Payload: raw,
		Time:    time.Now().UnixMilli(),
	}
	l.events = append(l.events, e)
	return e, nil
}

// Events returns a copy of the current events slice.
func (l *Log) Events() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, len(l.events))
	copy(out, l.events)
	return out
}

// EventsAfter returns a copy of events with Seq strictly greater than seq.
func (l *Log) EventsAfter(seq int64) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, 0, max(0, len(l.events)-int(seq)))
	for _, e := range l.events {
		if e.Seq > seq {
			out = append(out, e)
		}
	}
	return out
}

// Len returns the number of events currently in the log (after any pruning).
func (l *Log) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.events)
}

// HeadSeq returns the seq of the most recently appended event (0 if empty).
func (l *Log) HeadSeq() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// Anchor snapshots the projected state at the current head and records it. It bumps
// the reference count of every event in (prevAnchor.Seq, head] so those events are
// retained until this anchor (or a newer one) is dropped.
func (l *Log) Anchor(snapshot json.RawMessage) (Anchor, error) {
	if len(snapshot) == 0 {
		return Anchor{}, errors.New("journal: empty anchor snapshot")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	coverFrom := int64(0)
	if n := len(l.anchors); n > 0 {
		coverFrom = l.anchors[n-1].Seq
	}
	a := Anchor{Seq: l.seq, CoverFrom: coverFrom, Snapshot: snapshot}
	for i := range l.events {
		if l.events[i].Seq > coverFrom {
			l.events[i].Refs++
		}
	}
	l.anchors = append(l.anchors, a)
	return a, nil
}

// DropAnchor removes the anchor at seq and decrements the reference count of the
// events it covered. Events whose refs fall to zero become eligible for Prune.
func (l *Log) DropAnchor(seq int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	idx := -1
	for i, a := range l.anchors {
		if a.Seq == seq {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("journal: no anchor at seq %d", seq)
	}
	a := l.anchors[idx]
	for i := range l.events {
		if l.events[i].Seq > a.CoverFrom && l.events[i].Seq <= a.Seq {
			l.events[i].Refs--
		}
	}
	l.anchors = append(l.anchors[:idx], l.anchors[idx+1:]...)
	return nil
}

// NewestAnchor returns the most recent anchor (the "one level down" base for
// recovery), if any.
func (l *Log) NewestAnchor() (Anchor, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := len(l.anchors); n > 0 {
		return l.anchors[n-1], true
	}
	return Anchor{}, false
}

// Anchors returns a copy of all current anchors, oldest first.
func (l *Log) Anchors() []Anchor {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Anchor, len(l.anchors))
	copy(out, l.anchors)
	return out
}

// RetainFloor returns the seq below which events are eligible for pruning: the
// CoverFrom of the oldest retained anchor (0 if none). Events with Seq <= floor and
// Refs == 0 are no longer needed by any anchor or the live state.
func (l *Log) RetainFloor() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := len(l.anchors); n > 0 {
		return l.anchors[0].CoverFrom
	}
	return 0
}

// Prune removes leading events whose Seq <= RetainFloor() AND whose Refs == 0.
// It returns the number of events removed.
func (l *Log) Prune() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	floor := int64(0)
	if n := len(l.anchors); n > 0 {
		floor = l.anchors[0].CoverFrom
	}
	drop := 0
	for _, e := range l.events {
		if e.Seq <= floor && e.Refs == 0 {
			drop++
		} else {
			break // events are ordered; floor is a hard prefix boundary
		}
	}
	if drop > 0 {
		l.events = l.events[drop:]
	}
	return drop
}

// Projector folds a Log into a live State. It owns the current state pointer and is
// the single writer of both state and log, so it is concurrency-safe.
type Projector struct {
	mu    sync.Mutex
	state State
	log   *Log
}

// NewProjector builds a projector over initial state and log. Any events already in
// the log are replayed onto initial (enforcing the same-pointer invariant), so the
// projector is consistent with a log that was restored from disk.
func NewProjector(initial State, log *Log) (*Projector, error) {
	if initial == nil {
		return nil, errors.New("journal: nil initial state")
	}
	if log == nil {
		return nil, errors.New("journal: nil log")
	}
	p := &Projector{state: initial, log: log}
	if err := p.replay(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Projector) replay() error {
	for _, e := range p.log.Events() {
		next := p.state.Apply(e)
		if !samePointer(p.state, next) {
			return fmt.Errorf("journal: State.Apply returned a different pointer at seq %d (must mutate in place)", e.Seq)
		}
	}
	return nil
}

// AppendAndApply appends a new event to the log and folds it into the live state in
// one critical section. The apply-must-return-same-pointer invariant is enforced
// here: a projection that returns a freshly-allocated value is rejected with an
// error rather than silently diverging.
func (p *Projector) AppendAndApply(kind string, payload interface{}) (Event, error) {
	raw, err := marshalPayload(payload)
	if err != nil {
		return Event{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, err := p.log.Append(kind, raw)
	if err != nil {
		return Event{}, err
	}
	next := p.state.Apply(e)
	if !samePointer(p.state, next) {
		return Event{}, fmt.Errorf("journal: State.Apply returned a different pointer at seq %d (projection must mutate in place)", e.Seq)
	}
	return e, nil
}

// ApplyEvent folds an already-logged event into the live state (used when replaying
// events that were appended by another writer / loaded from disk).
func (p *Projector) ApplyEvent(e Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.state.Apply(e)
	if !samePointer(p.state, next) {
		return fmt.Errorf("journal: State.Apply returned a different pointer at seq %d (must mutate in place)", e.Seq)
	}
	return nil
}

// State returns the current live state (nil-safe: returns the held state).
func (p *Projector) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Log returns the underlying log.
func (p *Projector) Log() *Log { return p.log }

// Anchor snapshots the current live state into the log and returns the anchor.
func (p *Projector) Anchor() (Anchor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	raw, err := p.state.PureJSON()
	if err != nil {
		return Anchor{}, err
	}
	return p.log.Anchor(raw)
}

// Recover rebuilds the live state from the newest anchor ("one level down") and
// replays the events after it. It replaces p.state. If there is no anchor, it
// errors (there is nothing to recover to below the live tip).
func (p *Projector) Recover() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	anch, ok := p.log.NewestAnchor()
	if !ok {
		return errors.New("journal: no anchor to recover from")
	}
	if err := p.state.LoadJSON(anch.Snapshot); err != nil {
		return fmt.Errorf("journal: load anchor snapshot: %w", err)
	}
	for _, e := range p.log.EventsAfter(anch.Seq) {
		next := p.state.Apply(e)
		if !samePointer(p.state, next) {
			return fmt.Errorf("journal: State.Apply returned a different pointer at seq %d during recovery", e.Seq)
		}
	}
	return nil
}

// RecoverTo rebuilds live state from the anchor at seq (falling back to the newest
// anchor when seq <= 0) and replays the tail after it. Useful to fork/replay to a
// chosen checkpoint.
func (p *Projector) RecoverTo(seq int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if seq <= 0 {
		anch, ok := p.log.NewestAnchor()
		if !ok {
			return errors.New("journal: no anchor to recover from")
		}
		seq = anch.Seq
	}
	var target *Anchor
	for i := range p.log.anchors {
		if p.log.anchors[i].Seq == seq {
			target = &p.log.anchors[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("journal: no anchor at seq %d", seq)
	}
	if err := p.state.LoadJSON(target.Snapshot); err != nil {
		return fmt.Errorf("journal: load anchor snapshot: %w", err)
	}
	for _, e := range p.log.EventsAfter(target.Seq) {
		next := p.state.Apply(e)
		if !samePointer(p.state, next) {
			return fmt.Errorf("journal: State.Apply returned a different pointer at seq %d during recovery", e.Seq)
		}
	}
	return nil
}

// marshalPayload normalizes a payload into json.RawMessage.
func marshalPayload(payload interface{}) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}
	if raw, ok := payload.(json.RawMessage); ok {
		return raw, nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return b, nil
}
