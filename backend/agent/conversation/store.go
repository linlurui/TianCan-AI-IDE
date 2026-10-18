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

	mu          sync.Mutex
	state       *SessionState
	pending     int // events since the last tail snapshot
	onAppend    func(seq int64, role Role, content string)
	autoSegTok  int
	summarizer  func(string) string // optional small-model one-line distiller
	cap         TurnCapture        // optional per-turn file-change recorder
}

// TurnCapture records what files the AI changed during a turn. Implemented by
// turncap.Recorder; declared here as an interface so the conversation package
// stays independent of git/snapshot machinery.
type TurnCapture interface {
	// Begin marks the start of the turn identified by the user message seq.
	Begin(seq int64)
	// Capture persists the turn's file changes (idempotent).
	Capture(seq int64) error
	// CaptureOpen flushes every still-open turn (shutdown).
	CaptureOpen()
}

// SetCapture installs the per-turn file-change recorder. Must be called before
// messages are appended.
func (s *Session) SetCapture(c TurnCapture) {
	s.mu.Lock()
	s.cap = c
	s.mu.Unlock()
}

// DB exposes the shared memory database (used by the turn recorder, which
// stores file versions there).
func (s *Session) DB() *memdb.DB { return s.db }

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
	// 打开会话时补出历史轮次：老会话在此之前没有任何锚点，刻度尺会一直空着。
	sess.backfillTurnAnchors()
	return sess, nil
}

// Close flushes tails and closes every open session, then the database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for k, sess := range s.open {
		if err := sess.Close(); err != nil && firstErr == nil {
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

// SetSummarizer installs a small-model hook that distills a user turn into one
// line for anchor tooltips and archived-context digests. It is called on the
// append path with the turn's text; a nil or failing hook falls back to the
// local heuristic distiller. Must be called before the session is used.
func (s *Session) SetSummarizer(fn func(string) string) {
	s.mu.Lock()
	s.summarizer = fn
	s.mu.Unlock()
}

// distillSummary is the offline fallback: first meaningful line, markdown and
// quote prefixes stripped, hard-capped so tooltips stay one line.
func distillSummary(content string) string {
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		t = strings.TrimLeft(t, "#>*-• ")
		if t == "" {
			continue
		}
		return truncateRunes(t, 40)
	}
	return "（空）"
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
	if role == RoleUser {
		s.closeOpenTurns(e.Seq)
		s.dropTurnAnchor(e.Seq, content)
		s.beginTurn(e.Seq)
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

// closeOpenTurns caps every still-open turn anchor at the seq just before this
// new user message and returns the seqs it closed.
//
// The file capture runs synchronously HERE, before the rows are closed: at this
// moment the worktree still holds exactly the state that turn left behind. An
// async capture would race with the next turn's edits (the agent may already be
// writing files), and a capture after closing would be skipped by the
// open-turn guard and leave the turn with no versions at all.
func (s *Session) closeOpenTurns(before int64) []int64 {
	if s.db == nil || before <= 1 {
		return nil
	}
	rows, err := s.db.ListAnchors(s.hash, s.id)
	if err != nil {
		return nil
	}
	var closed []int64
	for _, a := range rows {
		if a.Kind == "turn" && a.TurnEndSeq == 0 && a.MsgSeq > 0 && a.MsgSeq < before {
			closed = append(closed, a.Seq)
		}
	}
	if len(closed) == 0 {
		return nil
	}
	for _, sq := range closed {
		s.captureTurnNow(sq) // 先抓：轮子还开着，工作区是它的最终态
	}
	_ = s.db.Exec(`UPDATE anchors SET turn_end_seq=? WHERE hash=? AND id=? AND kind='turn' AND turn_end_seq=0 AND msg_seq<?`,
		before-1, s.hash, s.id, before)
	return closed
}

// beginTurn tells the recorder a new turn started at this user message.
func (s *Session) beginTurn(seq int64) {
	s.mu.Lock()
	c := s.cap
	s.mu.Unlock()
	if c != nil {
		c.Begin(seq)
	}
}

// captureTurnNow captures one turn's file changes synchronously. Used at turn
// close, archive and shutdown — every point where the caller expects the
// versions to be there right away, and where the worktree still holds the state
// that turn left behind.
func (s *Session) captureTurnNow(seq int64) {
	s.mu.Lock()
	c := s.cap
	s.mu.Unlock()
	if c != nil {
		_ = c.Capture(seq)
	}
}

// dropTurnAnchor creates this turn's anchor. The summary starts as a local
// heuristic distill so the UI is never empty; if a small-model summarizer is
// installed it refines the line asynchronously off the append path.
func (s *Session) dropTurnAnchor(msgSeq int64, content string) {
	if s.db == nil {
		return
	}
	s.mu.Lock()
	fn := s.summarizer
	s.mu.Unlock()
	summary := distillSummary(content)
	_ = s.db.RecordAnchor(s.hash, s.id, memdb.AnchorRow{
		Seq:        msgSeq,
		Time:       time.Now().UnixMilli(),
		Title:      summary,
		Kind:       "turn",
		Summary:    summary,
		Status:     "active",
		MsgSeq:     msgSeq,
		TurnEndSeq: 0, // closed by the next user turn
	})
	if fn == nil {
		return
	}
	go func() {
		if v := strings.TrimSpace(fn(content)); v != "" {
			_ = s.db.RefineAnchorSummary(s.hash, s.id, msgSeq, truncateRunes(v, 60))
		}
	}()
}

// backfillTurnAnchors rebuilds the timeline for a session that already had
// messages before automatic turn anchors existed. Without it the rail stays
// empty until the next user message, which reads as "the feature is broken"
// for every session the user already has. Existing rows are never touched, so
// it is safe to run on every open.
func (s *Session) backfillTurnAnchors() {
	if s.db == nil {
		return
	}
	// 只有真正的轮次行才算"已经有了"：自动折叠点常常正好落在最后一条用户消息上
	// （退出保存时就是这样），那个点是匿名的，应该让位给带摘要的轮次行。
	existing := map[int64]bool{}
	if rows, err := s.db.ListAnchors(s.hash, s.id); err == nil {
		for _, a := range rows {
			if a.Kind == "turn" {
				existing[a.Seq] = true
			}
		}
	}
	var turns []Msg
	for _, m := range s.state.Messages {
		if m.Role == RoleUser && !existing[m.Seq] {
			turns = append(turns, m)
		}
	}
	for i, m := range turns {
		end := int64(0)
		if i+1 < len(turns) {
			end = turns[i+1].Seq - 1
		}
		t := m.Time
		if t == 0 {
			t = s.state.UpdatedAt
		}
		sum := distillSummary(m.Content)
		_ = s.db.RecordAnchor(s.hash, s.id, memdb.AnchorRow{
			Seq:        m.Seq,
			Time:       t,
			Title:      sum,
			Kind:       "turn",
			Summary:    sum,
			Status:     "active",
			MsgSeq:     m.Seq,
			TurnEndSeq: end,
		})
	}
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
	Kind      string `json:"kind"`              // turn | fold | manual
	Summary   string `json:"summary"`           // one-line digest of the work unit
	Status    string `json:"status"`            // active | archived
	MsgSeq    int64  `json:"msgSeq"`            // first message of the turn
	TurnEndSeq int64 `json:"turnEndSeq"`        // last message of the turn (0 = open)
}

// DropAnchor persists the current folded state as a tail snapshot and returns its
// view. Recovery replays only the events after it.
func (s *Session) DropAnchor() (AnchorInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = 0
	s.saveTail()
	st := s.State()
	return s.recordAnchor(st, 0, true), nil
}

// recordAnchor persists the given fold state as a resumable point in history.
// manual=true is an explicit "mark here" and may overwrite the row sitting on
// that seq; manual=false is the background fold, which must never erase a user
// turn that landed on the same event. Callers must hold the lock.
func (s *Session) recordAnchor(st *SessionState, coverFrom int64, manual bool) AnchorInfo {
	info := AnchorInfo{
		Seq:        st.HeadSeq,
		CoverFrom:  coverFrom,
		Time:       st.UpdatedAt,
		Title:      st.Title,
		Messages:   len(st.Messages),
		Tokens:     st.TotalTokens,
		Kind:       "fold",
		Status:     "active",
	}
	if s.db != nil {
		row := memdb.AnchorRow{
			Seq:       info.Seq,
			CoverFrom: info.CoverFrom,
			Time:      info.Time,
			Title:     info.Title,
			Messages:  info.Messages,
			Tokens:    info.Tokens,
			Kind:      info.Kind,
			Status:    info.Status,
		}
		if manual {
			_ = s.db.RecordAnchor(s.hash, s.id, row)
		} else {
			_ = s.db.RecordFoldAnchor(s.hash, s.id, row)
		}
	}
	return info
}

// Anchors lists every persisted fold snapshot, oldest first.
//
// sessions.tail_json only ever holds the newest fold and is silently overwritten,
// so it cannot support jumping back to an earlier point. The anchors table keeps
// the full history; without it the UI timeline would only ever show one tick.
func (s *Session) Anchors() []AnchorInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	rows, err := s.db.ListAnchors(s.hash, s.id)
	if err != nil {
		return nil
	}
	out := make([]AnchorInfo, 0, len(rows))
	for _, a := range rows {
		out = append(out, AnchorInfo{
			Seq:        a.Seq,
			CoverFrom:  a.CoverFrom,
			Time:       a.Time,
			Title:      a.Title,
			Messages:   a.Messages,
			Tokens:     a.Tokens,
			Kind:       a.Kind,
			Summary:    a.Summary,
			Status:     a.Status,
			MsgSeq:     a.MsgSeq,
			TurnEndSeq: a.TurnEndSeq,
		})
	}
	return out
}

// ArchiveAnchor flips a work unit between active and archived. Archived turns
// stay searchable but their full text no longer feeds the prompt (阶段三接线).
func (s *Session) ArchiveAnchor(seq int64, archived bool) error {
	if s.db == nil {
		return fmt.Errorf("conversation: store unavailable")
	}
	status := "active"
	if archived {
		status = "archived"
	}
	if err := s.db.SetAnchorStatus(s.hash, s.id, seq, status); err != nil {
		return err
	}
	// 归档前把这一轮的文件改动补齐：审查面板要能立刻看到这一轮的版本。
	s.captureTurnNow(seq)
	return nil
}

// AnchorHitView is a search hit mapped back to its work unit.
type AnchorHitView struct {
	Anchor  AnchorInfo `json:"anchor"`
	HitSeq  int64      `json:"hitSeq"`
	Score   float32    `json:"score"`
	Snippet string     `json:"snippet"`
}

// SearchAnchors finds work units by content: FTS over the turn's messages,
// bucketed back to anchors. This is the "像之前那个做法一样" entry point.
func (s *Session) SearchAnchors(query string, limit int) []AnchorHitView {
	if s.db == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	if limit <= 0 {
		limit = 8
	}
	hits, err := s.db.SearchAnchors(s.hash, s.id, query, limit)
	if err != nil {
		return nil
	}
	bySeq := make(map[int64]AnchorInfo, len(hits))
	for _, a := range s.Anchors() {
		bySeq[a.Seq] = a
	}
	out := make([]AnchorHitView, 0, len(hits))
	for _, h := range hits {
		view := AnchorHitView{HitSeq: h.HitSeq, Score: h.Score, Snippet: h.Snippet}
		if a, ok := bySeq[h.Anchor.Seq]; ok {
			view.Anchor = a
		} else {
			view.Anchor = toInfo(h.Anchor)
		}
		out = append(out, view)
	}
	return out
}

func toInfo(a memdb.AnchorRow) AnchorInfo {
	return AnchorInfo{
		Seq: a.Seq, CoverFrom: a.CoverFrom, Time: a.Time, Title: a.Title,
		Messages: a.Messages, Tokens: a.Tokens, Kind: a.Kind,
		Summary: a.Summary, Status: a.Status, MsgSeq: a.MsgSeq, TurnEndSeq: a.TurnEndSeq,
	}
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
	// Points newer than the rollback target describe a branch that no longer
	// exists; leaving them would let the UI offer a "jump back" that cannot work.
	_ = s.db.DeleteAnchorsAfter(s.hash, s.id, target)
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
	// Every kind of work unit counts: the tail column only ever holds the newest
	// fold, so it would always report one no matter how many points exist.
	anchors := 0
	if s.db != nil {
		anchors = s.db.CountAnchors(s.hash, s.id)
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
			Anchors:     s.db.CountAnchors(hash, r.ID),
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
		_ = cur.Close()
		delete(s.open, key)
	}
	s.mu.Unlock()
	if err := s.db.DeleteSession(hash, id); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.baseDir, hash, id))
}

// close flushes the tail. Callers must hold s.mu — Session.Close takes it, and
// taking it twice on the same mutex would hang the caller forever.
func (s *Session) close() error {
	s.saveTail()
	return nil
}

// Close releases this session's state (the shared DB is owned by the store).
func (s *Session) Close() error {
	// 退出前把还开着的轮次的文件改动落库：用户可能看完审查就直接关应用。
	s.mu.Lock()
	c := s.cap
	s.mu.Unlock()
	if c != nil {
		c.CaptureOpen()
	}
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
	// The automatic fold is also a resumable point: without this line every
	// AnchorEvery-boundary would overwrite the previous one and the history of
	// restorable points would keep collapsing to the newest fold.
	s.recordAnchor(s.state, 0, false)
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
