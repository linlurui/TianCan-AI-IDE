package memdb

import (
	"database/sql"
	"fmt"
	"time"
)

// EventRow is the SQL-backed shape of one journal event (kept journal-compatible).
type EventRow struct {
	SessionID string
	Seq       int64
	Kind      string
	TS        int64
	Payload   string
	Refs      int
}

// EnsureProject records a project hash -> root mapping (idempotent).
func (d *DB) EnsureProject(hash, root string) error {
	now := time.Now().UnixMilli()
	_, err := d.sql.Exec(
		`INSERT INTO projects(hash,root,created_at,last_at) VALUES(?,?,?,?) ON CONFLICT(hash) DO UPDATE SET root=excluded.root, last_at=excluded.last_at`,
		hash, root, now, now)
	return err
}

// OpenSession creates the session row if absent and returns whether it was new.
func (d *DB) OpenSession(hash, id, title string) (bool, error) {
	now := time.Now().UnixMilli()
	res, err := d.sql.Exec(
		`INSERT INTO sessions(hash,id,title,created_at,last_at,last_seq) VALUES(?,?,?,?,?,0) ON CONFLICT(hash,id) DO NOTHING`,
		hash, id, title, now, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TouchSession records the latest event seq + visit time for the session.
func (d *DB) TouchSession(hash, id string, lastSeq int64) error {
	_, err := d.sql.Exec(
		`UPDATE sessions SET last_at=?, last_seq=? WHERE hash=? AND id=?`,
		time.Now().UnixMilli(), lastSeq, hash, id)
	return err
}

// GetTail returns the persisted folded-state snapshot for incremental replay.
func (d *DB) GetTail(hash, id string) (tailJSON string, tailSeq int64, ok bool, err error) {
	var tj sql.NullString
	var ts sql.NullInt64
	if err := d.sql.QueryRow(`SELECT tail_json, tail_seq FROM sessions WHERE hash=? AND id=?`, hash, id).Scan(&tj, &ts); err != nil {
		if err == sql.ErrNoRows {
			return "", 0, false, nil
		}
		return "", 0, false, err
	}
	if !tj.Valid || tj.String == "" {
		return "", 0, false, nil
	}
	return tj.String, ts.Int64, true, nil
}

// SetTail persists the folded-state snapshot.
func (d *DB) SetTail(hash, id, tailJSON string, tailSeq int64) error {
	_, err := d.sql.Exec(`UPDATE sessions SET tail_json=?, tail_seq=? WHERE hash=? AND id=?`, tailJSON, tailSeq, hash, id)
	return err
}

// AppendEvent appends one event and returns its seq (monotonic per session).
func (d *DB) AppendEvent(hash, sessionID, kind, payload string) (int64, error) {
	var seq int64
	if err := d.sql.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM events WHERE session_id=?`, sessionID).Scan(&seq); err != nil {
		return 0, err
	}
	seq++
	if _, err := d.sql.Exec(
		`INSERT INTO events(session_id,seq,kind,ts,payload,refs) VALUES(?,?,?,?,?,0)`,
		sessionID, seq, kind, time.Now().UnixMilli(), payload); err != nil {
		return 0, err
	}
	_ = hash
	return seq, nil
}

// GetEvents returns all events for a session in seq order.
func (d *DB) GetEvents(sessionID string) ([]EventRow, error) {
	return d.eventsWhere(`session_id=?`, sessionID)
}

// EventsAfter returns events with seq strictly greater than the given seq.
func (d *DB) EventsAfter(sessionID string, seq int64) ([]EventRow, error) {
	return d.eventsWhere(`session_id=? AND seq>?`, sessionID, seq)
}

func (d *DB) eventsWhere(where string, args ...interface{}) ([]EventRow, error) {
	rows, err := d.sql.Query(`SELECT session_id,seq,kind,ts,payload,refs FROM events WHERE `+where+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.SessionID, &e.Seq, &e.Kind, &e.TS, &e.Payload, &e.Refs); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// DeleteEventsAfter removes events (and their message/fts projections) with seq >
// the given seq. Used by rollback to truncate state without losing the audit trail.
func (d *DB) DeleteEventsAfter(sessionID string, seq int64) error {
	if _, err := d.sql.Exec(`DELETE FROM messages_fts WHERE session_id=? AND seq>?`, sessionID, seq); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`DELETE FROM vectors WHERE session_id=? AND seq>?`, sessionID, seq); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`DELETE FROM messages WHERE session_id=? AND seq>?`, sessionID, seq); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`DELETE FROM events WHERE session_id=? AND seq>?`, sessionID, seq); err != nil {
		return err
	}
	return nil
}

// UpsertMessage writes a message and indexes it for full-text search.
func (d *DB) UpsertMessage(sessionID string, seq int64, role, content, segID string, ts int64) error {
	tok := len([]rune(content)) / 4
	if _, err := d.sql.Exec(
		`INSERT INTO messages(session_id,seq,role,content,seg_id,ts,tok) VALUES(?,?,?,?,?,?,?) ON CONFLICT(session_id,seq) DO UPDATE SET role=excluded.role, content=excluded.content, seg_id=excluded.seg_id`,
		sessionID, seq, role, content, segID, ts, tok); err != nil {
		return err
	}
	// FTS5 virtual tables do not support UPSERT; delete-then-insert is idempotent.
	if _, err := d.sql.Exec(`DELETE FROM messages_fts WHERE session_id=? AND seq=?`, sessionID, seq); err != nil {
		return err
	}
	if _, err := d.sql.Exec(
		`INSERT INTO messages_fts(session_id,seq,content) VALUES(?,?,?)`,
		sessionID, seq, tokenize(content)); err != nil {
		return err
	}
	return nil
}

// ── segments / requirements / redlines ────────────────────────────────────────

// SetSegment opens or updates a task segment.
func (d *DB) SetSegment(sessionID, id string, idx int, title, status string, startSeq, endSeq, tok int64, summary string) error {
	now := time.Now().UnixMilli()
	_, err := d.sql.Exec(
		`INSERT INTO segments(id,session_id,idx,title,status,start_seq,end_seq,summary,tok,created_at,closed_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(session_id,id) DO UPDATE SET title=excluded.title, status=excluded.status, end_seq=excluded.end_seq, summary=excluded.summary, tok=excluded.tok, closed_at=excluded.closed_at`,
		id, sessionID, idx, title, status, startSeq, endSeq, summary, tok, now, nullIfZero(endSeq))
	return err
}

// Requirement is one captured requirement point.
type Requirement struct {
	ID        string
	Text      string
	Status    string
	SourceSeq int64
}

// AddRequirement captures a requirement point (idempotent on id).
func (d *DB) AddRequirement(sessionID, id, text, status string, sourceSeq int64) error {
	_, err := d.sql.Exec(
		`INSERT INTO requirements(id,session_id,text,status,source_seq,created_at) VALUES(?,?,?,?,?,?)
		 ON CONFLICT(session_id,id) DO UPDATE SET text=excluded.text, status=excluded.status`,
		id, sessionID, text, status, sourceSeq, time.Now().UnixMilli())
	return err
}

// Requirements returns all requirement points for a session.
func (d *DB) Requirements(sessionID string) ([]Requirement, error) {
	rows, err := d.sql.Query(`SELECT id,text,status,source_seq FROM requirements WHERE session_id=? ORDER BY source_seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Requirement
	for rows.Next() {
		var r Requirement
		if err := rows.Scan(&r.ID, &r.Text, &r.Status, &r.SourceSeq); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Redline is a hard/soft constraint extracted from the user.
type Redline struct {
	ID        string
	Text      string
	Severity  string // "hard" | "soft"
	SourceSeq int64
}

// AddRedline captures a red line. The (session_id,text) UNIQUE constraint keeps
// re-statements from duplicating; severity is upgraded if a hard one arrives.
func (d *DB) AddRedline(sessionID, id, text, severity string, sourceSeq int64) error {
	now := time.Now().UnixMilli()
	_, err := d.sql.Exec(
		`INSERT INTO redlines(id,session_id,text,severity,source_seq,created_at) VALUES(?,?,?,?,?,?)
		 ON CONFLICT(session_id,text) DO UPDATE SET severity=CASE WHEN excluded.severity='hard' THEN 'hard' ELSE redlines.severity END, source_seq=excluded.source_seq`,
		id, sessionID, text, severity, sourceSeq, now)
	return err
}

// Redlines returns all red lines for a session (small set, always loaded in full so
// the prompt assembler can never accidentally compress them away).
func (d *DB) Redlines(sessionID string) ([]Redline, error) {
	rows, err := d.sql.Query(`SELECT id,text,severity,source_seq FROM redlines WHERE session_id=? ORDER BY severity DESC, source_seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Redline
	for rows.Next() {
		var r Redline
		if err := rows.Scan(&r.ID, &r.Text, &r.Severity, &r.SourceSeq); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func nullIfZero(v int64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

// ── task association links ─────────────────────────────────────────────────────

// LinkKind enumerates how a current task relates to a historical one.
const (
	LinkContinues  = "continues"  // builds on / continues an earlier task
	LinkConflicts  = "conflicts"  // contradicts an earlier decision (most dangerous)
	LinkReferences = "references" // cites / reuses earlier work
	LinkSupersedes = "supersedes" // replaces an earlier approach
)

// AddLink records a directed association between two segments.
func (d *DB) AddLink(fromSeg, toSeg, kind string, score float32, reason string) error {
	if fromSeg == "" || toSeg == "" {
		return fmt.Errorf("memdb: link requires both segments")
	}
	_, err := d.sql.Exec(
		`INSERT INTO links(from_seg,to_seg,kind,score,reason) VALUES(?,?,?,?,?)
		 ON CONFLICT(from_seg,to_seg,kind) DO UPDATE SET score=excluded.score, reason=excluded.reason`,
		fromSeg, toSeg, kind, score, reason)
	return err
}

// LinksTo returns associations pointing at the given segment (what this task
// relates to in history).
func (d *DB) LinksTo(seg string) ([]Link, error) {
	return d.linksWhere(`to_seg=?`, seg)
}

// LinksFrom returns associations the given segment makes into history.
func (d *DB) LinksFrom(seg string) ([]Link, error) {
	return d.linksWhere(`from_seg=?`, seg)
}

// Link is one task-association edge.
type Link struct {
	FromSeg string
	ToSeg   string
	Kind    string
	Score   float32
	Reason  string
}

func (d *DB) linksWhere(where string, args ...interface{}) ([]Link, error) {
	rows, err := d.sql.Query(`SELECT from_seg,to_seg,kind,score,reason FROM links WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.FromSeg, &l.ToSeg, &l.Kind, &l.Score, &l.Reason); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}
