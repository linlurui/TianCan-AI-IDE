package journal

import (
	"encoding/json"
	"sync"
	"testing"
)

// --- concrete State implementations used by the tests ------------------------

// counter folds "inc" payloads into a running count. It is the canonical
// well-behaved State: Apply mutates in place and returns the same pointer, and
// PureJSON/LoadJSON round-trip losslessly.
type counter struct {
	Count int `json:"count"`
}

func (c *counter) Apply(e Event) State {
	if len(e.Payload) > 0 {
		var d struct {
			Inc int `json:"inc"`
		}
		_ = json.Unmarshal(e.Payload, &d)
		c.Count += d.Inc
	}
	return c
}

func (c *counter) PureJSON() ([]byte, error) { return json.Marshal(c) }
func (c *counter) LoadJSON(b []byte) error   { return json.Unmarshal(b, c) }

// msgList folds "msg" payloads, used to prove projection of structured payloads.
type msgList struct {
	Items []string `json:"items"`
}

func (m *msgList) Apply(e Event) State {
	if len(e.Payload) > 0 {
		var d struct {
			Msg string `json:"msg"`
		}
		_ = json.Unmarshal(e.Payload, &d)
		if d.Msg != "" {
			m.Items = append(m.Items, d.Msg)
		}
	}
	return m
}
func (m *msgList) PureJSON() ([]byte, error) { return json.Marshal(m) }
func (m *msgList) LoadJSON(b []byte) error   { return json.Unmarshal(b, m) }

// badState deliberately violates the "apply returns same pointer" invariant: it
// allocates a fresh value on every Apply. Used by the 反证 test to prove the
// Projector rejects diverging projections.
type badState struct{ Count int }

func (b *badState) Apply(e Event) State { return &badState{} } // different pointer!
func (b *badState) PureJSON() ([]byte, error) {
	return json.Marshal(map[string]int{"count": b.Count})
}
func (b *badState) LoadJSON(d []byte) error {
	var m map[string]int
	if err := json.Unmarshal(d, &m); err != nil {
		return err
	}
	b.Count = m["count"]
	return nil
}

// lossyState deliberately violates the "pure JSON round-trip" invariant: its
// LoadJSON drops field B. Used by the 反证 test to prove the round-trip check is
// meaningful (it does not trivially always pass).
type lossyState struct {
	A int `json:"a"`
	B int `json:"b"`
}

func (s *lossyState) Apply(e Event) State {
	if len(e.Payload) > 0 {
		var d struct {
			Inc int `json:"inc"`
		}
		_ = json.Unmarshal(e.Payload, &d)
		s.A += d.Inc
	}
	return s
}
func (s *lossyState) PureJSON() ([]byte, error) { return json.Marshal(s) }
func (s *lossyState) LoadJSON(d []byte) error {
	// Intentionally drop B — a broken projection would lose data on reload.
	var m map[string]int
	if err := json.Unmarshal(d, &m); err != nil {
		return err
	}
	s.A = m["a"]
	s.B = 0
	return nil
}

// --- basic Append / fold ------------------------------------------------------

func TestAppendAndApplyFoldsInPlace(t *testing.T) {
	log := NewLog()
	p, err := NewProjector(&counter{}, log)
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := p.AppendAndApply("inc", map[string]int{"inc": 1}); err != nil {
			t.Fatalf("AppendAndApply: %v", err)
		}
	}
	if got := p.State().(*counter).Count; got != 10 {
		t.Fatalf("count = %d, want 10", got)
	}
	if log.Len() != 10 || log.HeadSeq() != 10 {
		t.Fatalf("log len=%d head=%d, want 10/10", log.Len(), log.HeadSeq())
	}
}

func TestAppendNilPayload(t *testing.T) {
	log := NewLog()
	p, _ := NewProjector(&counter{}, log)
	if _, err := p.AppendAndApply("noop", nil); err != nil {
		t.Fatalf("AppendAndApply(nil): %v", err)
	}
	if log.Len() != 1 {
		t.Fatalf("len = %d, want 1", log.Len())
	}
}

func TestAppendRawMessagePayload(t *testing.T) {
	log := NewLog()
	p, _ := NewProjector(&msgList{}, log)
	if _, err := p.AppendAndApply("msg", json.RawMessage(`{"msg":"hello"}`)); err != nil {
		t.Fatalf("AppendAndApply raw: %v", err)
	}
	if got := p.State().(*msgList).Items; len(got) != 1 || got[0] != "hello" {
		t.Fatalf("items = %v, want [hello]", got)
	}
}

// NewProjector must replay a log that was populated before construction (the
// "restore from disk" path).
func TestNewProjectorReplaysExistingLog(t *testing.T) {
	log := NewLog()
	for i := 0; i < 5; i++ {
		if _, err := log.Append("inc", map[string]int{"inc": 1}); err != nil {
			t.Fatalf("log.Append: %v", err)
		}
	}
	p, err := NewProjector(&counter{}, log)
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	if got := p.State().(*counter).Count; got != 5 {
		t.Fatalf("replayed count = %d, want 5", got)
	}
}

func TestNewProjectorNilGuards(t *testing.T) {
	log := NewLog()
	if _, err := NewProjector(nil, log); err == nil {
		t.Fatal("expected error for nil initial state")
	}
	if _, err := NewProjector(&counter{}, nil); err == nil {
		t.Fatal("expected error for nil log")
	}
}

// --- 反证 1: apply must return the same pointer -------------------------------

func TestSamePointerInvariantRejectsDivergingProjection(t *testing.T) {
	log := NewLog()
	p, err := NewProjector(&badState{}, log)
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	// A projection that returns a fresh pointer must be rejected, never silently
	// diverging from the log.
	if _, err := p.AppendAndApply("inc", map[string]int{"inc": 1}); err == nil {
		t.Fatal("expected error for projection returning a different pointer")
	}

	// And the SAME check must NOT false-positive on a well-behaved projection.
	log2 := NewLog()
	p2, err := NewProjector(&counter{}, log2)
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	if _, err := p2.AppendAndApply("inc", map[string]int{"inc": 1}); err != nil {
		t.Fatalf("well-behaved projection wrongly rejected: %v", err)
	}
}

// --- 反证 2: state must be pure JSON (lossless round-trip) --------------------

func TestPureJSONRoundTripInvariant(t *testing.T) {
	// Well-behaved state round-trips identically.
	good := &counter{Count: 7}
	raw1, _ := good.PureJSON()
	loaded := &counter{}
	if err := loaded.LoadJSON(raw1); err != nil {
		t.Fatalf("load: %v", err)
	}
	raw2, _ := loaded.PureJSON()
	if string(raw1) != string(raw2) {
		t.Fatalf("good round-trip drift: %s != %s", raw1, raw2)
	}

	// A broken projection that drops a field must be detected by the round-trip.
	bad := &lossyState{A: 3, B: 5}
	braw1, _ := bad.PureJSON()
	bloaded := &lossyState{}
	if err := bloaded.LoadJSON(braw1); err != nil {
		t.Fatalf("load: %v", err)
	}
	braw2, _ := bloaded.PureJSON()
	if string(braw1) == string(braw2) {
		t.Fatal("lossy round-trip went undetected — the invariant check is trivial")
	}
}

// --- 反证 3: anchors + refs counting + low-level recovery --------------------

func TestAnchorRefsAndLowLevelRecovery(t *testing.T) {
	log := NewLog()
	p, err := NewProjector(&counter{}, log)
	if err != nil {
		t.Fatalf("NewProjector: %v", err)
	}
	// 10 events -> count 10, seq 10.
	for i := 0; i < 10; i++ {
		p.AppendAndApply("inc", map[string]int{"inc": 1})
	}
	// Anchor at head. Events 1..10 now referenced (refs=1).
	anch10, err := p.Anchor()
	if err != nil {
		t.Fatalf("Anchor: %v", err)
	}
	if anch10.Seq != 10 {
		t.Fatalf("anchor seq = %d, want 10", anch10.Seq)
	}
	var snap struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(anch10.Snapshot, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap.Count != 10 {
		t.Fatalf("anchor snapshot count = %d, want 10", snap.Count)
	}

	// 5 more events -> count 15, seq 15.
	for i := 0; i < 5; i++ {
		p.AppendAndApply("inc", map[string]int{"inc": 1})
	}
	anch15, err := p.Anchor()
	if err != nil {
		t.Fatalf("Anchor: %v", err)
	}
	if anch15.Seq != 15 {
		t.Fatalf("anchor seq = %d, want 15", anch15.Seq)
	}

	// refs: events 1..10 covered by anchor@10, 11..15 by anchor@15.
	for _, e := range log.Events() {
		if e.Refs != 1 {
			t.Fatalf("event seq=%d refs=%d, want 1", e.Seq, e.Refs)
		}
	}

	// Recovery from the newest anchor (one level down) reconstructs the live state.
	live := p.State().(*counter).Count
	if err := p.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := p.State().(*counter).Count; got != live {
		t.Fatalf("recover-from-newest count = %d, want %d", got, live)
	}

	// Recovery to the OLDER anchor + replay tail also reconstructs the same live state.
	if err := p.RecoverTo(10); err != nil {
		t.Fatalf("RecoverTo(10): %v", err)
	}
	if got := p.State().(*counter).Count; got != live {
		t.Fatalf("recover-to-10 count = %d, want %d", got, live)
	}

	// Drop the older anchor: events 1..10 fall to refs 0.
	if err := log.DropAnchor(10); err != nil {
		t.Fatalf("DropAnchor(10): %v", err)
	}
	for _, e := range log.Events() {
		want := int32(1)
		if e.Seq <= 10 {
			want = 0
		}
		if e.Refs != want {
			t.Fatalf("event seq=%d refs=%d, want %d", e.Seq, e.Refs, want)
		}
	}
	// Prune the dead prefix (events <= RetainFloor with refs 0).
	removed := log.Prune()
	if removed != 10 {
		t.Fatalf("Prune removed %d, want 10", removed)
	}
	if log.Len() != 5 {
		t.Fatalf("log len after prune = %d, want 5", log.Len())
	}
	// Recovery still works after pruning the pre-anchor tail — proof that recovery
	// is anchored, not replayed from seq 1.
	if err := p.Recover(); err != nil {
		t.Fatalf("Recover after prune: %v", err)
	}
	if got := p.State().(*counter).Count; got != live {
		t.Fatalf("post-prune recover count = %d, want %d", got, live)
	}
}

// Corrupting an anchor snapshot must change the recovered state — otherwise the
// recovery path would not actually be reading the anchor. This 反证 proves the
// anchor is genuinely the recovery base.
func TestCorruptAnchorChangesRecovery(t *testing.T) {
	log := NewLog()
	p, _ := NewProjector(&counter{}, log)
	for i := 0; i < 10; i++ {
		p.AppendAndApply("inc", map[string]int{"inc": 1})
	}
	if _, err := p.Anchor(); err != nil { // anchor @10, count 10
		t.Fatalf("Anchor: %v", err)
	}
	for i := 0; i < 5; i++ {
		p.AppendAndApply("inc", map[string]int{"inc": 1})
	}
	anch, ok := log.NewestAnchor()
	if !ok || anch.Seq != 10 {
		t.Fatalf("want newest anchor @10, got %+v", anch)
	}

	// Mutate the anchor snapshot in the log to a wrong value (count=999).
	log.anchors[len(log.anchors)-1].Snapshot = json.RawMessage(`{"count":999}`)

	live := p.State().(*counter).Count // 15
	if err := p.RecoverTo(10); err != nil {
		t.Fatalf("RecoverTo(10) with corrupt snapshot: %v", err)
	}
	got := p.State().(*counter).Count // 999 + replay 11..15 (5) = 1004
	if got == live {
		t.Fatalf("corrupt anchor did not change recovered state (%d); recovery is not anchored", got)
	}
}

func TestRecoverNoAnchorFails(t *testing.T) {
	log := NewLog()
	p, _ := NewProjector(&counter{}, log)
	if err := p.Recover(); err == nil {
		t.Fatal("expected error when no anchor exists")
	}
}

// --- concurrency / race -------------------------------------------------------

func TestConcurrentAppendNoRace(t *testing.T) {
	log := NewLog()
	p, _ := NewProjector(&counter{}, log)
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, err := p.AppendAndApply("inc", map[string]int{"inc": 1}); err != nil {
				t.Errorf("AppendAndApply: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := p.State().(*counter).Count; got != n {
		t.Fatalf("count = %d, want %d", got, n)
	}
	if log.Len() != n {
		t.Fatalf("log len = %d, want %d", log.Len(), n)
	}
}

func TestConcurrentAnchorNoRace(t *testing.T) {
	log := NewLog()
	p, _ := NewProjector(&counter{}, log)
	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			p.AppendAndApply("inc", map[string]int{"inc": 1})
			p.Anchor() // may race with AppendAndApply on the log; both lock the log.
		}()
	}
	wg.Wait()
	if got := p.State().(*counter).Count; got != n {
		t.Fatalf("count = %d, want %d", got, n)
	}
}
