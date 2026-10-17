package conversation

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/journal"
)

// Segment is a slice of the conversation about one task. The whole point of the
// roadmap is that the prompt only needs the ACTIVE segment verbatim: closed
// segments contribute only their summary, which is what makes "一个项目一个会话，
// 永远也不怕超" achievable.
type Segment struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Summary  string  `json:"summary,omitempty"`
	Status   string  `json:"status"` // "active" | "closed"
	OpenSeq  int64   `json:"openSeq"`
	CloseSeq int64   `json:"closeSeq,omitempty"`
	MsgSeqs  []int64 `json:"msgSeqs,omitempty"`
	Tokens   int     `json:"tokens,omitempty"`
	AutoSeg  bool    `json:"auto,omitempty"` // opened implicitly by the store
}

// Requirement is a captured requirement point (需求点).
type Requirement struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"` // "open" | "done"
	Seq    int64  `json:"seq"`
	Source string `json:"source,omitempty"`
}

// Redline is a hard constraint the user stated (红线): things that must be done,
// must never be done, or must always be watched for. These are injected into every
// prompt and are NEVER summarised away — that is exactly what makes them red lines
// rather than ordinary context.
type Redline struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Seq      int64  `json:"seq"`
	Source   string `json:"source,omitempty"`   // "user" | "extracted"
	Severity string `json:"severity,omitempty"` // "hard" | "soft"
	Hits     int    `json:"hits,omitempty"`     // how often recall surfaced it
}

// Msg is one message in the projection. Seq is the journal seq of the event that
// produced it, which is what makes rollback and audit precise.
type Msg struct {
	Seq       int64  `json:"seq"`
	Role      Role   `json:"role"`
	Content   string `json:"content"`
	Time      int64  `json:"t"`
	SegmentID string `json:"seg,omitempty"`
	Tokens    int    `json:"tok,omitempty"`
}

// SessionState is the projected state of a session: the pure fold of its journal.
//
// It implements journal.State, so it must:
//   - Apply in place and return the same pointer;
//   - round-trip PureJSON -> LoadJSON -> PureJSON stably.
type SessionState struct {
	SessionID   string        `json:"sessionId"`
	ProjectRoot string        `json:"projectRoot,omitempty"`
	Title       string        `json:"title"`
	CreatedAt   int64         `json:"createdAt"`
	UpdatedAt   int64         `json:"updatedAt"`
	HeadSeq     int64         `json:"headSeq"`
	Messages    []Msg         `json:"messages"`
	Segments    []Segment     `json:"segments"`
	Requirement []Requirement `json:"requirements"`
	Redlines    []Redline     `json:"redlines"`
	// Token accounting is best-effort (see EstimateTokens) but it is what drives
	// auto-segmentation, so it must be monotone in the log.
	TotalTokens int `json:"totalTokens"`
	// idCounter keeps generated ids unique within a session state.
	idCounter int64 `json:"-"`
}

// NewSessionState returns an empty state for the given session.
func NewSessionState(sessionID, projectRoot, title string, now int64) *SessionState {
	if title == "" {
		title = "新会话"
	}
	return &SessionState{
		SessionID:   sessionID,
		ProjectRoot: projectRoot,
		Title:       title,
		CreatedAt:   now,
		UpdatedAt:   now,
		Messages:    []Msg{},
		Segments:    []Segment{},
		Requirement: []Requirement{},
		Redlines:    []Redline{},
	}
}

// --- journal.State contract -------------------------------------------------

// Apply folds one event into the receiver IN PLACE and returns the receiver.
// Unknown kinds are ignored (forward compatibility: an older build replaying a
// newer log must not fail, it just cannot interpret the new kind).
func (s *SessionState) Apply(e journal.Event) journal.State {
	switch e.Kind {
	case KindMessage:
		var p MessagePayload
		if err := decodePayload(e, &p); err == nil {
			s.applyMessage(e, p)
		}
	case KindSegOpen:
		var p SegOpenPayload
		if err := decodePayload(e, &p); err == nil {
			s.applySegOpen(e, p)
		}
	case KindSegClose:
		var p SegClosePayload
		if err := decodePayload(e, &p); err == nil {
			s.applySegClose(e, p)
		}
	case KindReqAdd:
		var p ReqAddPayload
		if err := decodePayload(e, &p); err == nil {
			s.applyReqAdd(e, p)
		}
	case KindReqDone:
		var p ReqDonePayload
		if err := decodePayload(e, &p); err == nil {
			for i := range s.Requirement {
				if s.Requirement[i].ID == p.ID {
					s.Requirement[i].Status = "done"
				}
			}
		}
	case KindRedAdd:
		var p ReqAddPayload
		if err := decodePayload(e, &p); err == nil {
			s.applyRedAdd(e, p)
		}
	case KindTitle:
		var p TitlePayload
		if err := decodePayload(e, &p); err == nil && p.Title != "" {
			s.Title = p.Title
		}
	case KindRollback:
		var p RollbackPayload
		if err := decodePayload(e, &p); err == nil {
			s.applyRollback(p)
		}
	case KindRecallHit:
		var p RecallPayload
		if err := decodePayload(e, &p); err == nil {
			for _, seq := range p.Seqs {
				for i := range s.Redlines {
					if s.Redlines[i].Seq == seq {
						s.Redlines[i].Hits++
					}
				}
			}
		}
	}
	if e.Seq > s.HeadSeq {
		s.HeadSeq = e.Seq
	}
	if e.Time > s.UpdatedAt {
		s.UpdatedAt = e.Time
	}
	return s
}

// PureJSON returns the canonical encoding of the state (used for anchors and for
// persisting a snapshot alongside the log).
func (s *SessionState) PureJSON() ([]byte, error) {
	return json.Marshal(s)
}

// LoadJSON restores the state from an encoding produced by PureJSON.
func (s *SessionState) LoadJSON(data []byte) error {
	return json.Unmarshal(data, s)
}

// --- event handlers ---------------------------------------------------------

func (s *SessionState) applyMessage(e journal.Event, p MessagePayload) {
	tok := EstimateTokens(p.Content)
	s.Messages = append(s.Messages, Msg{
		Seq:       e.Seq,
		Role:      p.Role,
		Content:   p.Content,
		Time:      e.Time,
		SegmentID: p.SegmentID,
		Tokens:    tok,
	})
	s.TotalTokens += tok
	if seg := s.activeSegment(); seg != nil {
		seg.MsgSeqs = append(seg.MsgSeqs, e.Seq)
		seg.Tokens += tok
	}
}

func (s *SessionState) applySegOpen(e journal.Event, p SegOpenPayload) {
	// Opening a segment implicitly closes the previous active one (without a
	// summary — the caller is expected to close it properly when it can).
	if prev := s.activeSegment(); prev != nil {
		prev.Status = "closed"
		prev.CloseSeq = e.Seq
		if prev.Summary == "" {
			prev.Summary = autoSummary(s, prev)
		}
	}
	id := p.ID
	if id == "" {
		s.idCounter++
		id = newID("seg", s.idCounter)
	}
	s.Segments = append(s.Segments, Segment{
		ID:      id,
		Title:   p.Title,
		Status:  "active",
		OpenSeq: e.Seq,
	})
}

func (s *SessionState) applySegClose(e journal.Event, p SegClosePayload) {
	for i := range s.Segments {
		if s.Segments[i].ID == p.ID || (p.ID == "" && s.Segments[i].Status == "active") {
			s.Segments[i].Status = "closed"
			s.Segments[i].CloseSeq = e.Seq
			if p.Summary != "" {
				s.Segments[i].Summary = p.Summary
			} else if s.Segments[i].Summary == "" {
				s.Segments[i].Summary = autoSummary(s, &s.Segments[i])
			}
			return
		}
	}
}

func (s *SessionState) applyReqAdd(e journal.Event, p ReqAddPayload) {
	// Idempotent by text: re-stating a requirement must not create duplicates.
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return
	}
	for i := range s.Requirement {
		if s.Requirement[i].Text == text {
			return
		}
	}
	id := p.ID
	if id == "" {
		s.idCounter++
		id = newID("req", s.idCounter)
	}
	s.Requirement = append(s.Requirement, Requirement{
		ID:     id,
		Text:   text,
		Status: "open",
		Seq:    e.Seq,
		Source: p.Source,
	})
}

func (s *SessionState) applyRedAdd(e journal.Event, p ReqAddPayload) {
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return
	}
	for i := range s.Redlines {
		if s.Redlines[i].Text == text {
			return
		}
	}
	id := p.ID
	if id == "" {
		s.idCounter++
		id = newID("red", s.idCounter)
	}
	sev := p.Severity
	if sev == "" {
		sev = "hard"
	}
	src := p.Source
	if src == "" {
		src = "user"
	}
	s.Redlines = append(s.Redlines, Redline{
		ID:       id,
		Text:     text,
		Seq:      e.Seq,
		Source:   src,
		Severity: sev,
	})
}

// applyRollback truncates the projection to the state as of seq To. The rollback
// event itself remains in the log, so the audit trail is never destroyed.
func (s *SessionState) applyRollback(p RollbackPayload) {
	to := p.To
	if to < 0 {
		to = 0
	}
	msgs := s.Messages[:0]
	tokens := 0
	for _, m := range s.Messages {
		if m.Seq <= to {
			msgs = append(msgs, m)
			tokens += m.Tokens
		}
	}
	s.Messages = msgs
	s.TotalTokens = tokens

	reqs := s.Requirement[:0]
	for _, r := range s.Requirement {
		if r.Seq <= to {
			reqs = append(reqs, r)
		}
	}
	s.Requirement = reqs

	reds := s.Redlines[:0]
	for _, r := range s.Redlines {
		if r.Seq <= to {
			reds = append(reds, r)
		}
	}
	s.Redlines = reds

	segs := s.Segments[:0]
	for _, g := range s.Segments {
		switch {
		case g.OpenSeq > to:
			// opened after the rollback point: gone
		case g.Status == "closed" && g.CloseSeq > to:
			// closed after the rollback point: re-open it
			g.Status = "active"
			g.CloseSeq = 0
			segs = append(segs, g)
		default:
			segs = append(segs, g)
		}
	}
	s.Segments = segs
}

// --- helpers ----------------------------------------------------------------

func (s *SessionState) activeSegment() *Segment {
	for i := range s.Segments {
		if s.Segments[i].Status == "active" {
			return &s.Segments[i]
		}
	}
	return nil
}

// ActiveSegment returns a copy of the currently open segment, if any.
func (s *SessionState) ActiveSegment() (Segment, bool) {
	if seg := s.activeSegment(); seg != nil {
		return *seg, true
	}
	return Segment{}, false
}

// MessagesIn returns the messages whose seq is in the given set (used to expand a
// segment or a recall hit back into text).
func (s *SessionState) MessagesIn(seqs []int64) []Msg {
	if len(seqs) == 0 {
		return nil
	}
	want := make(map[int64]bool, len(seqs))
	for _, v := range seqs {
		want[v] = true
	}
	out := make([]Msg, 0, len(seqs))
	for _, m := range s.Messages {
		if want[m.Seq] {
			out = append(out, m)
		}
	}
	return out
}

// Recent returns the last n messages (or all, if n <= 0).
func (s *SessionState) Recent(n int) []Msg {
	if n <= 0 || n >= len(s.Messages) {
		out := make([]Msg, len(s.Messages))
		copy(out, s.Messages)
		return out
	}
	out := make([]Msg, n)
	copy(out, s.Messages[len(s.Messages)-n:])
	return out
}

// autoSummary builds a cheap extractive summary when a segment is closed without
// one: the first user ask plus the last assistant reply, truncated. Summaries are
// the ONLY thing a closed segment contributes to the prompt, so they must be
// short but must preserve the "what was asked / what was produced" pair.
func autoSummary(s *SessionState, seg *Segment) string {
	msgs := s.MessagesIn(seg.MsgSeqs)
	var ask, done string
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			if ask == "" {
				ask = firstLine(m.Content)
			}
		case RoleAssistant:
			if m.Content != "" {
				done = firstLine(m.Content)
			}
		}
	}
	if ask == "" && done == "" {
		return "(空段落)"
	}
	out := ask
	if done != "" {
		if out != "" {
			out += " → "
		}
		out += done
	}
	return truncateRunes(out, 200)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncateRunes(s, 120)
}

func truncateRunes(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// EstimateTokens gives a cheap, deterministic token estimate. It is deliberately
// crude — it only drives budgeting decisions (auto-segmentation, context packing),
// never billing. Chinese/CJK counts ~1 token per rune, latin ~1 per 4 chars.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	runes := []rune(s)
	cjk := 0
	other := 0
	for _, r := range runes {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjk++
		} else {
			other++
		}
	}
	t := cjk + other/4
	if t == 0 && len(s) > 0 {
		t = 1
	}
	return t
}
