package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/journal"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/memdb"
)

// DefaultBaseDirName is the directory under the user config dir that holds the
// unified memory database: ~/.tiancan-ai-ide/sessions/store.sqlite.
const DefaultBaseDirName = "sessions"

// MainSessionID is the well-known id of a project's primary session. The roadmap
// asks for "一个项目一个会话", so a project always has (at least) this one; extra
// sessions can be branched off it.
const MainSessionID = "main"

// AutoSegmentTokens is the budget at which the store opens a new segment by
// itself: the active segment is closed and summarised, and a fresh one starts, so
// the prompt never has to carry an unbounded amount of verbatim history.
const AutoSegmentTokens = 6000

// AnchorEvery is how often (in appended events) the folded state is persisted as a
// tail snapshot. It caps replay cost after a restart: recovery never replays more
// than this many events from the newest tail.
const AnchorEvery = 64

// ProjectHash turns a project root into a stable directory name. Hashing (rather
// than sanitising the path) keeps ids short, filesystem-safe and stable even if
// the project is moved onto a different mount style.
func ProjectHash(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return "noproject"
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(sum[:])[:16]
}

// Store owns every project session and backs them with one shared SQLite database.
// It is safe for concurrent use; individual sessions are opened lazily and kept in
// memory (their authoritative state is the pure fold of the event log, but the
// folded tail is persisted so reopening is O(recent events), not O(all)).
type Store struct {
	baseDir string
	mu      sync.Mutex
	db      *memdb.DB
	open    map[string]*Session // key: projectHash + "/" + sessionID
}

// NewStore creates a store rooted at baseDir (created if missing). If baseDir is
// empty it defaults to ~/.tiancan-ai-ide/sessions.
func NewStore(baseDir string) *Store {
	if baseDir == "" {
		baseDir = filepath.Join(configHome(), DefaultBaseDirName)
	}
	_ = os.MkdirAll(baseDir, 0o755)
	db, err := memdb.Open(filepath.Join(baseDir, "store.sqlite"))
	if err != nil {
		// Defer the error to the first operation; keep the constructor signature.
		db = nil
	}
	return &Store{baseDir: baseDir, db: db, open: map[string]*Session{}}
}

// DefaultStore returns a store under ~/.tiancan-ai-ide/sessions.
func DefaultStore() *Store {
	return NewStore(filepath.Join(configHome(), DefaultBaseDirName))
}

func configHome() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".tiancan-ai-ide")
	}
	return filepath.Join(os.TempDir(), ".tiancan-ai-ide")
}

// BaseDir returns the store root.
func (s *Store) BaseDir() string { return s.baseDir }

// memID returns the global session id used inside the database (project-unique).
func memID(hash, id string) string { return hash + "/" + id }

// Session is one open project session: an event log folded into a live projection,
// with the log persisted to the shared SQLite store.
type Session struct {
	store *Store
	db    *memdb.DB
	hash  string
	id    string
	memID string // hash + "/" + id, the global key in the events/messages tables
	dir   string // per-session directory (for recall index sidecars)

	mu         sync.Mutex
	state      *SessionState
	pending    int // events since the last tail snapshot
	onAppend   func(seq int64, role Role, content string)
	autoSegTok int
}

// Open returns the project's main session, creating it on first use.
func (s *Store) Open(projectRoot string) (*Session, error) {
	return s.OpenID(projectRoot, MainSessionID, "")
}

// OpenID opens (or creates) the named session of a project.
func (s *Store) OpenID(projectRoot, id, title string) (*Session, error) {
	if id == "" {
		id = MainSessionID
	}
	hash := ProjectHash(projectRoot)
	key := hash + "/" + id
	s.mu.Lock()
	if cur, ok := s.open[key]; ok {
		s.mu.Unlock()
		return cur, nil
	}
	// Hold the store lock while building so two racers cannot both create it.
	sess, err := s.openLocked(projectRoot, hash, id, title)
	s.mu.Unlock()
	return sess, err
}

func (s *Store) openLocked(projectRoot, hash, id, title string) (*Session, error) {
	if s.db == nil {
		return nil, fmt.Errorf("conversation: store database not available")
	}
	dir := filepath.Join(s.baseDir, hash, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("conversation: mkdir %s: %w", dir, err)
	}
	if err := s.db.EnsureProject(hash, projectRoot); err != nil {
		return nil, err
	}
	if _, err := s.db.OpenSession(hash, id, title); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	st := NewSessionState(id, projectRoot, title, now)
	// Recover from the folded tail snapshot, then replay only the events after it.
	if tj, tailSeq, ok, err := s.db.GetTail(hash, id); err == nil && ok && tj != "" {
		_ = st.LoadJSON([]byte(tj))
		// Replay tail is the fully folded state at tailSeq; keep HeadSeq sane.
		if tailSeq > st.HeadSeq {
			st.HeadSeq = tailSeq
		}
		st.UpdatedAt = maxInt64(st.UpdatedAt, st.CreatedAt)
	}
	events, err := s.db.EventsAfter(memID(hash, id), st.HeadSeq)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		st.Apply(journal.Event{
			Seq:     e.Seq,
			Kind:    e.Kind,
			Payload: []byte(e.Payload),
			Time:    e.TS,
			Refs:    int32(e.Refs),
		})
	}
	sess := &Session{
		store:     s,
		db:        s.db,
		hash:      hash,
		id:        id,
		memID:     memID(hash, id),
		dir:       dir,
		state:     st,
		autoSegTok: AutoSegmentTokens,
	}
	s.open[hash+"/"+id] = sess
	// A freshly created session should not be titled "新会话" forever.
	if title != "" && st.Title != title {
		_, _ = sess.appendLocked(KindTitle, TitlePayload{Title: title})
	}
	return sess, nil
}

// Close flushes tails and closes every open session, then the database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for k, sess := range s.open {
		if err := sess.close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(s.open, k)
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.db = nil
	}
	return firstErr
}

// --- Session identity -------------------------------------------------------

func (s *Session) ID() string          { return s.id }
func (s *Session) ProjectRoot() string { return s.projectRoot() }
func (s *Session) Dir() string         { return s.dir }

func (s *Session) projectRoot() string {
	if s.state != nil {
		return s.state.ProjectRoot
	}
	return ""
}

// State returns the live projection. The returned pointer is owned by the session
// and must not be mutated by callers (read it, or go through the append methods).
func (s *Session) State() *SessionState { return s.state }

// OnAppend registers a hook called for every appended message, e.g. to feed the
// semantic recall index. Set it before appending; it is called without holding
// the session lock, so it must be re-entrant safe.
func (s *Session) OnAppend(fn func(seq int64, role Role, content string)) {
	s.mu.Lock()
	s.onAppend = fn
	s.mu.Unlock()
}

func (s *Session) hook() func(int64, Role, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onAppend
}

// --- Appending --------------------------------------------------------------

// append folds one event into the projection, persists it, and (when the budget is
// reached) persists a folded tail. Callers must not hold the lock.
func (s *Session) append(kind string, payload interface{}) (journal.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(kind, payload)
}

func (s *Session) appendLocked(kind string, payload interface{}) (journal.Event, error) {
	raw, err := marshalPayload(payload)
	if err != nil {
		return journal.Event{}, err
	}
	now := time.Now().UnixMilli()
	seq, err := s.db.AppendEvent(s.hash, s.memID, kind, string(raw))
	if err != nil {
		return journal.Event{}, err
	}
	e := journal.Event{Seq: seq, Kind: kind, Payload: raw, Time: now}
	s.state.Apply(e)
	s.persistMessage(kind, seq, raw, now)
	_ = s.db.TouchSession(s.hash, s.id, seq)
	s.pending++
	if s.pending >= AnchorEvery {
		s.pending = 0
		s.saveTail()
	}
	return e, nil
}

// persistMessage mirrors a message event into the FTS-indexed messages table so the
// unified store can do lexical recall without replaying the whole log.
func (s *Session) persistMessage(kind string, seq int64, raw json.RawMessage, now int64) {
	if kind != KindMessage {
		return
	}
	var p MessagePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	_ = s.db.UpsertMessage(s.memID, seq, string(p.Role), p.Content, p.SegmentID, now)
}

// AppendMessage records a chat message. It returns the log seq, which is the stable
// handle used by recall, audit and rollback.
func (s *Session) AppendMessage(role Role, content string) (int64, error) {
	if strings.TrimSpace(content) == "" {
		return 0, fmt.Errorf("conversation: empty message content")
	}
	segID := ""
	if seg, ok := s.State().ActiveSegment(); ok {
		segID = seg.ID
	}
	e, err := s.append(KindMessage, MessagePayload{Role: role, Content: content, SegmentID: segID})
	if err != nil {
		return 0, err
	}
	if fn := s.hook(); fn != nil {
		fn(e.Seq, role, content)
	}
	// Auto-segmentation: keep the active slice bounded so the prompt never has to
	// carry the whole history verbatim.
	if st := s.State(); st.TotalTokens > 0 {
		if seg, ok := st.ActiveSegment(); ok && seg.Tokens >= s.autoSegTok {
			_, _ = s.append(KindSegClose, SegClosePayload{ID: seg.ID})
			_, _ = s.append(KindSegOpen, SegOpenPayload{Title: "后续工作"})
		}
	}
	return e.Seq, nil
}

// SetAutoSegmentTokens overrides the auto-segmentation budget (0 disables it).
func (s *Session) SetAutoSegmentTokens(n int) {
	s.mu.Lock()
	s.autoSegTok = n
	s.mu.Unlock()
}

// SetTitle names the session.
func (s *Session) SetTitle(title string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("conversation: empty title")
	}
	_, err := s.append(KindTitle, TitlePayload{Title: title})
	return err
}

// --- Structured state: segments / requirements / red lines ------------------

// OpenSegment starts a new work segment (closing the previous one).
func (s *Session) OpenSegment(title string) (string, error) {
	if title == "" {
		title = "新段落"
	}
	id := newID("seg", time.Now().UnixNano()%100000)
	if _, err := s.append(KindSegOpen, SegOpenPayload{ID: id, Title: title}); err != nil {
		return "", err
	}
	return id, nil
}

// CloseSegment closes the active segment with a summary. Without a summary the
// state builds an extractive one (first ask → last reply).
func (s *Session) CloseSegment(summary string) error {
	st := s.State()
	seg, ok := st.ActiveSegment()
	if !ok {
		return fmt.Errorf("conversation: no active segment")
	}
	if summary == "" {
		summary = autoSummary(st, &seg)
	}
	_, err := s.append(KindSegClose, SegClosePayload{ID: seg.ID, Summary: summary})
	return err
}

// AddRequirement records a requirement point (需求点). Duplicates by text are
// ignored by the projection.
func (s *Session) AddRequirement(text, source string) (int64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, fmt.Errorf("conversation: empty requirement")
	}
	e, err := s.append(KindReqAdd, ReqAddPayload{Text: text, Source: source})
	return e.Seq, err
}

// DoneRequirement marks a requirement satisfied.
func (s *Session) DoneRequirement(id string) error {
	_, err := s.append(KindReqDone, ReqDonePayload{ID: id})
	return err
}

// AddRedline records a red line: something the user said must / must never be done,
// or must always be watched. Red lines are injected into every prompt and are never
// compressed away.
func (s *Session) AddRedline(text, source, severity string) (int64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, fmt.Errorf("conversation: empty redline")
	}
	e, err := s.append(KindRedAdd, ReqAddPayload{Text: text, Source: source, Severity: severity})
	return e.Seq, err
}

// RecordRecall appends an audit record of a semantic recall, so a later review can
// explain why old context entered the prompt.
func (s *Session) RecordRecall(query string, seqs []int64, score []float64) error {
	_, err := s.append(KindRecallHit, RecallPayload{Query: query, Seqs: seqs, Score: score})
	return err
}

// --- Anchors, audit, rollback ----------------------------------------------

// AnchorInfo is the UI-facing view of a folded-state (tail) snapshot.
type AnchorInfo struct {
	Seq       int64  `json:"seq"`
	CoverFrom int64  `json:"coverFrom"`
	Time      int64  `json:"time"`
	Title     string `json:"title"`
	Messages  int    `json:"messages"`
	Tokens    int    `json:"tokens"`
}

// DropAnchor persists the current folded state as a tail snapshot and returns its
// view. Recovery replays only the events after it.
func (s *Session) DropAnchor() (AnchorInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = 0
	s.saveTail()
	st := s.State()
	return AnchorInfo{
		Seq:      st.HeadSeq,
		Time:     st.UpdatedAt,
		Title:    st.Title,
		Messages: len(st.Messages),
		Tokens:   st.TotalTokens,
	}, nil
}

// Anchors lists the persisted tail snapshots (at most one, the current fold).
func (s *Session) Anchors() []AnchorInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	tj, ts, ok, err := s.db.GetTail(s.hash, s.id)
	if err != nil || !ok || tj == "" {
		return nil
	}
	st := NewSessionState(s.id, s.projectRoot(), "", 0)
	if e := st.LoadJSON([]byte(tj)); e != nil {
		return nil
	}
	return []AnchorInfo{{
		Seq:      ts,
		Time:     st.UpdatedAt,
		Title:    st.Title,
		Messages: len(st.Messages),
		Tokens:   st.TotalTokens,
	}}
}

// AuditEntry is one line of the audit trail: the raw event, rendered for humans.
type AuditEntry struct {
	Seq     int64  `json:"seq"`
	Kind    string `json:"kind"`
	Time    int64  `json:"time"`
	Role    string `json:"role,omitempty"`
	Preview string `json:"preview"`
}

// Audit returns the event stream, oldest first, optionally starting after fromSeq
// and capped at limit entries (0 = all). This IS the audit trail: nothing is ever
// rewritten, so it always reflects exactly what happened.
func (s *Session) Audit(fromSeq int64, limit int) []AuditEntry {
	events, err := s.db.GetEvents(s.memID)
	if err != nil {
		return nil
	}
	out := make([]AuditEntry, 0, len(events))
	for _, e := range events {
		if e.Seq <= fromSeq {
			continue
		}
		out = append(out, renderAudit(journal.Event{
			Seq:     e.Seq,
			Kind:    e.Kind,
			Payload: []byte(e.Payload),
			Time:    e.TS,
		}))
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Rollback truncates the projection back to target seq. The rollback itself is
// appended as an event, so the audit trail keeps the fact that it happened (and the
// truncated events remain in the log for post-mortem). After rollback the folded
// tail is persisted so a restart recovers the truncated state directly.
func (s *Session) Rollback(target int64, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if target < 0 {
		target = 0
	}
	if target > s.state.HeadSeq {
		return fmt.Errorf("conversation: rollback target %d is ahead of head %d", target, s.state.HeadSeq)
	}
	if _, err := s.appendLocked(KindRollback, RollbackPayload{To: target, Note: note}); err != nil {
		return err
	}
	s.pending = 0
	s.saveTail()
	return nil
}

// --- Listing ---------------------------------------------------------------

// SessionInfo is the UI-facing summary of a session.
type SessionInfo struct {
	ID          string `json:"id"`
	ProjectRoot string `json:"projectRoot"`
	ProjectHash string `json:"projectHash"`
	Title       string `json:"title"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
	Messages    int    `json:"messages"`
	Tokens      int    `json:"tokens"`
	Segments    int    `json:"segments"`
	OpenReqs    int    `json:"openReqs"`
	Redlines    int    `json:"redlines"`
	HeadSeq     int64  `json:"headSeq"`
	Anchors     int    `json:"anchors"`
}

// Info summarises the current state of this session.
func (s *Session) Info() SessionInfo {
	st := s.State()
	open := 0
	for _, r := range st.Requirement {
		if r.Status != "done" {
			open++
		}
	}
	anchors := 0
	if _, _, ok, _ := s.db.GetTail(s.hash, s.id); ok {
		anchors = 1
	}
	return SessionInfo{
		ID:          s.id,
		ProjectRoot: s.projectRoot(),
		ProjectHash: ProjectHash(s.projectRoot()),
		Title:       st.Title,
		CreatedAt:   st.CreatedAt,
		UpdatedAt:   st.UpdatedAt,
		Messages:    len(st.Messages),
		Tokens:      st.TotalTokens,
		Segments:    len(st.Segments),
		OpenReqs:    open,
		Redlines:    len(st.Redlines),
		HeadSeq:     st.HeadSeq,
		Anchors:     anchors,
	}
}

// ListSessions returns every session of a project, most recently updated first.
// Live (still-open) sessions without a persisted tail are merged in from memory.
func (s *Store) ListSessions(projectRoot string) ([]SessionInfo, error) {
	hash := ProjectHash(projectRoot)
	seen := map[string]bool{}
	var out []SessionInfo
	if rows, err := s.db.ListSessions(hash); err == nil {
		for _, r := range rows {
			var st SessionState
			if r.TailJSON != "" {
				_ = st.LoadJSON([]byte(r.TailJSON))
			}
			if st.Title == "" {
				st.Title = r.Title
			}
			if st.HeadSeq == 0 {
				st.HeadSeq = r.TailSeq
			}
			open := 0
			for _, req := range st.Requirement {
				if req.Status != "done" {
					open++
				}
			}
			out = append(out, SessionInfo{
				ID:          r.ID,
				ProjectRoot: projectRoot,
				ProjectHash: hash,
				Title:       st.Title,
				CreatedAt:   st.CreatedAt,
				UpdatedAt:   r.LastAt,
				Messages:    len(st.Messages),
				Tokens:      st.TotalTokens,
				Segments:    len(st.Segments),
				OpenReqs:    open,
				Redlines:    len(st.Redlines),
				HeadSeq:     st.HeadSeq,
				Anchors:     1,
			})
			seen[r.ID] = true
		}
	}
	s.mu.Lock()
	for key, sess := range s.open {
		if !strings.HasPrefix(key, hash+"/") {
			continue
		}
		id := strings.TrimPrefix(key, hash+"/")
		if seen[id] {
			continue
		}
		out = append(out, sess.Info())
		seen[id] = true
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

// ListProjects returns the distinct project hashes that have sessions.
func (s *Store) ListProjects() []string {
	ps, err := s.db.ListProjects()
	if err != nil {
		return nil
	}
	sort.Strings(ps)
	return ps
}

// DeleteSession removes a session from the database and its directory.
func (s *Store) DeleteSession(projectRoot, id string) error {
	hash := ProjectHash(projectRoot)
	key := hash + "/" + id
	s.mu.Lock()
	if cur, ok := s.open[key]; ok {
		_ = cur.close()
		delete(s.open, key)
	}
	s.mu.Unlock()
	if err := s.db.DeleteSession(hash, id); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.baseDir, hash, id))
}

func (s *Session) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveTail()
	return nil
}

// Close releases this session's state (the shared DB is owned by the store).
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.close()
}

// saveTail persists the folded projection so a restart resumes from here.
func (s *Session) saveTail() {
	if s.db == nil {
		return
	}
	tj, err := s.state.PureJSON()
	if err != nil {
		return
	}
	_ = s.db.SetTail(s.hash, s.id, string(tj), s.state.HeadSeq)
}

// MarshalState exposes the projection as raw JSON (used by bindings / tests).
func (s *Session) MarshalState() (json.RawMessage, error) {
	return s.State().PureJSON()
}

func renderAudit(e journal.Event) AuditEntry {
	ent := AuditEntry{Seq: e.Seq, Kind: e.Kind, Time: e.Time}
	switch e.Kind {
	case KindMessage:
		var p MessagePayload
		if err := decodePayload(e, &p); err == nil {
			ent.Role = string(p.Role)
			ent.Preview = truncateRunes(strings.TrimSpace(p.Content), 120)
		}
	case KindSegOpen:
		var p SegOpenPayload
		if err := decodePayload(e, &p); err == nil {
			ent.Preview = p.Title
		}
	case KindSegClose:
		var p SegClosePayload
		if err := decodePayload(e, &p); err == nil {
			ent.Preview = truncateRunes(p.Summary, 120)
		}
	case KindReqAdd, KindRedAdd:
		var p ReqAddPayload
		if err := decodePayload(e, &p); err == nil {
			ent.Preview = truncateRunes(p.Text, 120)
		}
	case KindReqDone:
		var p ReqDonePayload
		if err := decodePayload(e, &p); err == nil {
			ent.Preview = p.ID
		}
	case KindRollback:
		var p RollbackPayload
		if err := decodePayload(e, &p); err == nil {
			ent.Preview = fmt.Sprintf("回退到 seq=%d %s", p.To, p.Note)
		}
	case KindRecallHit:
		var p RecallPayload
		if err := decodePayload(e, &p); err == nil {
			ent.Preview = "召回 " + truncateRunes(p.Query, 100)
		}
	case KindTitle:
		var p TitlePayload
		if err := decodePayload(e, &p); err == nil {
			ent.Preview = p.Title
		}
	}
	return ent
}

// marshalPayload normalises a payload into json.RawMessage.
func marshalPayload(payload interface{}) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}
	if raw, ok := payload.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(payload)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
