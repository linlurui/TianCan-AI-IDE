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
)

// DefaultBaseDirName is the directory under the user config dir that holds every
// project session: ~/.tiancan-ai-ide/sessions/<projectHash>/<sessionID>/.
const DefaultBaseDirName = "sessions"

// MainSessionID is the well-known id of a project's primary session. The roadmap
// asks for "一个项目一个会话", so a project always has (at least) this one; extra
// sessions can be branched off it.
const MainSessionID = "main"

// AutoSegmentTokens is the budget at which the store opens a new segment by
// itself: the active segment is closed and summarised, and a fresh one starts, so
// the prompt never has to carry an unbounded amount of verbatim history.
const AutoSegmentTokens = 6000

// AnchorEvery is how often (in appended events) a state snapshot is dropped. It
// caps replay cost after a crash: recovery never replays more than this many
// events from the newest anchor.
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

// Store owns every project session under one base directory. It is safe for
// concurrent use; individual sessions are opened lazily and kept in memory.
type Store struct {
	baseDir string
	mu      sync.Mutex
	open    map[string]*Session // key: projectHash + "/" + sessionID
}

// NewStore creates a store rooted at baseDir (created if missing).
func NewStore(baseDir string) *Store {
	if baseDir == "" {
		baseDir = filepath.Join(configHome(), DefaultBaseDirName)
	}
	_ = os.MkdirAll(baseDir, 0o755)
	return &Store{baseDir: baseDir, open: map[string]*Session{}}
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

// Session is one open project session: a journal plus its live projection.
type Session struct {
	store *Store
	log   *journal.Log
	proj  *journal.Projector

	id          string
	projectRoot string
	dir         string

	mu         sync.Mutex
	pending    int // events since the last anchor
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
	dir := filepath.Join(s.baseDir, hash, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("conversation: mkdir %s: %w", dir, err)
	}
	logPath := filepath.Join(dir, "journal.jsonl")
	lg, err := journal.OpenLog(logPath)
	if err != nil {
		return nil, fmt.Errorf("conversation: open journal: %w", err)
	}
	now := time.Now().UnixMilli()
	initial := NewSessionState(id, projectRoot, title, now)
	proj, err := journal.NewProjector(initial, lg)
	if err != nil {
		_ = lg.Close()
		return nil, fmt.Errorf("conversation: replay journal: %w", err)
	}
	sess := &Session{
		store:       s,
		log:         lg,
		proj:        proj,
		id:          id,
		projectRoot: projectRoot,
		dir:         dir,
		autoSegTok:  AutoSegmentTokens,
	}
	s.open[hash+"/"+id] = sess
	// A freshly created session should not be titled "新会话" forever.
	if title != "" && sess.State().Title != title {
		_, _ = sess.append(KindTitle, TitlePayload{Title: title})
	}
	return sess, nil
}

// Close flushes and closes every open session.
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
	return firstErr
}

// --- Session identity -------------------------------------------------------

func (s *Session) ID() string          { return s.id }
func (s *Session) ProjectRoot() string { return s.projectRoot }
func (s *Session) Dir() string         { return s.dir }
func (s *Session) Log() *journal.Log   { return s.log }

// State returns the live projection. The returned pointer is owned by the session
// and must not be mutated by callers (read it, or go through the append methods).
func (s *Session) State() *SessionState {
	st, _ := s.proj.State().(*SessionState)
	return st
}

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

// append folds one event into the projection and persists it. Callers must hold
// no lock (it takes the session lock itself).
func (s *Session) append(kind string, payload interface{}) (journal.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(kind, payload)
}

func (s *Session) appendLocked(kind string, payload interface{}) (journal.Event, error) {
	e, err := s.proj.AppendAndApply(kind, payload)
	if err != nil {
		return e, err
	}
	s.pending++
	if s.pending >= AnchorEvery {
		if _, err := s.proj.Anchor(); err == nil {
			s.pending = 0
			s.log.Prune()
		}
	}
	return e, nil
}

// AppendMessage records a chat message. It returns the journal seq, which is the
// stable handle used by recall, audit and rollback.
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
	e, err := s.append(KindSegOpen, SegOpenPayload{ID: id, Title: title})
	if err != nil {
		return "", err
	}
	_ = e
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

// AddRedline records a red line: something the user said must / must never be
// done, or must always be watched. Red lines are injected into every prompt and
// are never compressed away.
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

// AnchorInfo is the UI-facing view of a journal anchor.
type AnchorInfo struct {
	Seq       int64  `json:"seq"`
	CoverFrom int64  `json:"coverFrom"`
	Time      int64  `json:"time"`
	Title     string `json:"title"`
	Messages  int    `json:"messages"`
	Tokens    int    `json:"tokens"`
}

// DropAnchor snapshots the current state. Returns the anchor info.
func (s *Session) DropAnchor() (AnchorInfo, error) {
	a, err := s.proj.Anchor()
	if err != nil {
		return AnchorInfo{}, err
	}
	s.mu.Lock()
	s.pending = 0
	s.mu.Unlock()
	s.log.Prune()
	return s.describeAnchor(a), nil
}

// Anchors lists every anchor, newest last.
func (s *Session) Anchors() []AnchorInfo {
	list := s.log.Anchors()
	out := make([]AnchorInfo, 0, len(list))
	for _, a := range list {
		out = append(out, s.describeAnchor(a))
	}
	return out
}

func (s *Session) describeAnchor(a journal.Anchor) AnchorInfo {
	info := AnchorInfo{Seq: a.Seq, CoverFrom: a.CoverFrom}
	var st SessionState
	if err := st.LoadJSON(a.Snapshot); err == nil {
		info.Title = st.Title
		info.Messages = len(st.Messages)
		info.Tokens = st.TotalTokens
		info.Time = st.UpdatedAt
	}
	if info.Time == 0 {
		for _, e := range s.log.Events() {
			if e.Seq == a.Seq {
				info.Time = e.Time
				break
			}
		}
	}
	return info
}

// AuditEntry is one line of the audit trail: the raw event, rendered for humans.
type AuditEntry struct {
	Seq     int64  `json:"seq"`
	Kind    string `json:"kind"`
	Time    int64  `json:"time"`
	Role    string `json:"role,omitempty"`
	Preview string `json:"preview"`
}

// Audit returns the event stream, oldest first, optionally starting after
// fromSeq and capped at limit entries (0 = all). This IS the audit trail: nothing
// is ever rewritten, so it always reflects exactly what happened.
func (s *Session) Audit(fromSeq int64, limit int) []AuditEntry {
	events := s.log.Events()
	out := make([]AuditEntry, 0, len(events))
	for _, e := range events {
		if e.Seq <= fromSeq {
			continue
		}
		out = append(out, renderAudit(e))
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
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

// Rollback truncates the projection back to target seq. The rollback itself is
// appended as an event, so the audit trail keeps the fact that it happened (and
// the truncated events remain in the log file for post-mortem).
func (s *Session) Rollback(target int64, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if target < 0 {
		target = 0
	}
	if target > s.log.HeadSeq() {
		return fmt.Errorf("conversation: rollback target %d is ahead of head %d", target, s.log.HeadSeq())
	}
	if _, err := s.appendLocked(KindRollback, RollbackPayload{To: target, Note: note}); err != nil {
		return err
	}
	if _, err := s.proj.Anchor(); err == nil {
		s.pending = 0
	}
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
	return SessionInfo{
		ID:          s.id,
		ProjectRoot: s.projectRoot,
		ProjectHash: ProjectHash(s.projectRoot),
		Title:       st.Title,
		CreatedAt:   st.CreatedAt,
		UpdatedAt:   st.UpdatedAt,
		Messages:    len(st.Messages),
		Tokens:      st.TotalTokens,
		Segments:    len(st.Segments),
		OpenReqs:    open,
		Redlines:    len(st.Redlines),
		HeadSeq:     st.HeadSeq,
		Anchors:     len(s.log.Anchors()),
	}
}

// ListSessions returns every session of a project, most recently updated first.
// Sessions that are not currently open are read from disk by replaying their
// journal (cheap, and it is the same code path used after a crash).
func (s *Store) ListSessions(projectRoot string) ([]SessionInfo, error) {
	dir := filepath.Join(s.baseDir, ProjectHash(projectRoot))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SessionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		s.mu.Lock()
		cur, cached := s.open[ProjectHash(projectRoot)+"/"+id]
		s.mu.Unlock()
		if cached {
			out = append(out, cur.Info())
			continue
		}
		info, err := s.peek(ProjectHash(projectRoot), id, projectRoot)
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

// ListProjects returns the distinct project hashes that have sessions.
func (s *Store) ListProjects() []string {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// peek replays a session journal read-only to summarise it without keeping it open.
func (s *Store) peek(hash, id, projectRoot string) (SessionInfo, error) {
	lg, err := journal.OpenLog(filepath.Join(s.baseDir, hash, id, "journal.jsonl"))
	if err != nil {
		return SessionInfo{}, err
	}
	defer lg.Close()
	proj, err := journal.NewProjector(NewSessionState(id, projectRoot, "", time.Now().UnixMilli()), lg)
	if err != nil {
		return SessionInfo{}, err
	}
	st, _ := proj.State().(*SessionState)
	open := 0
	for _, r := range st.Requirement {
		if r.Status != "done" {
			open++
		}
	}
	return SessionInfo{
		ID:          id,
		ProjectRoot: projectRoot,
		ProjectHash: hash,
		Title:       st.Title,
		CreatedAt:   st.CreatedAt,
		UpdatedAt:   st.UpdatedAt,
		Messages:    len(st.Messages),
		Tokens:      st.TotalTokens,
		Segments:    len(st.Segments),
		OpenReqs:    open,
		Redlines:    len(st.Redlines),
		HeadSeq:     st.HeadSeq,
		Anchors:     len(lg.Anchors()),
	}, nil
}

// DeleteSession removes a session directory from disk (and from the open cache).
func (s *Store) DeleteSession(projectRoot, id string) error {
	hash := ProjectHash(projectRoot)
	key := hash + "/" + id
	s.mu.Lock()
	if cur, ok := s.open[key]; ok {
		_ = cur.close()
		delete(s.open, key)
	}
	s.mu.Unlock()
	return os.RemoveAll(filepath.Join(s.baseDir, hash, id))
}

func (s *Session) close() error {
	return s.log.Close()
}

// Close releases this session's file handle.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.close()
}

// MarshalState exposes the projection as raw JSON (used by bindings / tests).
func (s *Session) MarshalState() (json.RawMessage, error) {
	return s.State().PureJSON()
}
