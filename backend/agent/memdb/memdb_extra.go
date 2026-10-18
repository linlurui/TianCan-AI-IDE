package memdb

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DefaultPath returns the canonical unified memory database path. The conversation
// store and the file-rollback manager both open this single file so a project's
// memory and its snapshots live together (and survive an app restart). It mirrors
// conversation.DefaultStore's layout: ~/.tiancan-ai-ide/sessions/store.sqlite.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".tiancan-ai-ide", "sessions", "store.sqlite")
}

// SessionRow is the on-disk summary of a session, readable without replaying its log.
type SessionRow struct {
	ID        string
	Title     string
	TailJSON  string
	TailSeq   int64
	LastAt    int64
	CreatedAt int64
}

// ListProjects returns the distinct project hashes that have sessions.
func (d *DB) ListProjects() ([]string, error) {
	rows, err := d.sql.Query(`SELECT hash FROM projects ORDER BY hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// ListSessions returns the sessions of a project (newest first), summarised from
// the folded tail snapshot so an unopened session is listed without replay.
func (d *DB) ListSessions(hash string) ([]SessionRow, error) {
	rows, err := d.sql.Query(
		`SELECT id,title,tail_json,tail_seq,last_at,created_at FROM sessions WHERE hash=? ORDER BY last_at DESC`,
		hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		var tj sql.NullString
		var la, ca sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Title, &tj, &r.TailSeq, &la, &ca); err != nil {
			return nil, err
		}
		r.TailJSON = tj.String
		r.LastAt = la.Int64
		r.CreatedAt = ca.Int64
		out = append(out, r)
	}
	return out, nil
}

// AnchorRow is one persisted fold snapshot, i.e. a point the UI can jump to and
// roll back to. See the anchors table in initSchema and docs/anchor-design.md.
type AnchorRow struct {
	Seq        int64
	CoverFrom  int64
	Time       int64
	Title      string
	Messages   int
	Tokens     int
	Kind       string // turn | fold | manual
	Summary    string // one-line digest (small-model refined when available)
	Status     string // active | archived
	MsgSeq     int64  // first message seq of the work unit (turn anchors)
	TurnEndSeq int64  // last message seq of the work unit (turn anchors)
}

// RecordAnchor records an explicitly dropped snapshot.
//
// It is the unguarded upsert: the caller asked for a checkpoint here, so it
// replaces whatever sat on this seq. Even then the fields the caller has no
// opinion about are kept, so writing a checkpoint on top of a user turn does not
// blank the turn's summary or its message range — only its kind changes.
func (d *DB) RecordAnchor(hash, id string, a AnchorRow) error {
	_, err := d.sql.Exec(
		`INSERT INTO anchors(hash, id, seq, cover_from, time, title, messages, tokens,
		    kind, summary, status, msg_seq, turn_end_seq)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(hash, id, seq) DO UPDATE SET
		   cover_from=excluded.cover_from, time=excluded.time,
		   title=COALESCE(NULLIF(excluded.title,''), title),
		   messages=excluded.messages, tokens=excluded.tokens,
		   kind=excluded.kind,
		   summary=COALESCE(NULLIF(excluded.summary,''), summary),
		   status=excluded.status,
		   msg_seq=COALESCE(NULLIF(excluded.msg_seq,0), msg_seq),
		   turn_end_seq=COALESCE(NULLIF(excluded.turn_end_seq,0), turn_end_seq)`,
		hash, id, a.Seq, a.CoverFrom, a.Time, a.Title, a.Messages, a.Tokens,
		a.Kind, a.Summary, a.Status, a.MsgSeq, a.TurnEndSeq)
	return err
}

// CountAnchors returns how many work units a session has.
// Only turns count: automatic fold rows are internal bookkeeping and would make
// the number disagree with the ticks the user actually sees on the rail.
func (d *DB) CountAnchors(hash, id string) int {
	var n int
	if err := d.sql.QueryRow(
		`SELECT COUNT(*) FROM anchors WHERE hash=? AND id=? AND COALESCE(kind,'fold')='turn'`,
		hash, id).Scan(&n); err != nil {
		return 0
	}
	return n
}

// RecordFoldAnchor persists an automatic fold snapshot.
//
// The fold boundary and a user turn can land on the very same event (e.g. the
// app saves the tail right after the newest user message), and rows are keyed by
// (hash, id, seq). A plain upsert would then have the anonymous fold overwrite
// the turn and take its summary and its tick on the timeline with it. The
// guarded DO UPDATE keeps the turn row untouched; the fold simply has nowhere to
// go, which is fine — the point it marks is already recorded.
func (d *DB) RecordFoldAnchor(hash, id string, a AnchorRow) error {
	_, err := d.sql.Exec(
		`INSERT INTO anchors(hash, id, seq, cover_from, time, title, messages, tokens,
		    kind, summary, status, msg_seq, turn_end_seq)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(hash, id, seq) DO UPDATE SET
		   cover_from=excluded.cover_from, time=excluded.time, title=excluded.title,
		   messages=excluded.messages, tokens=excluded.tokens,
		   kind=excluded.kind, summary=excluded.summary, status=excluded.status,
		   msg_seq=excluded.msg_seq, turn_end_seq=excluded.turn_end_seq
		 WHERE kind IS NULL OR kind='' OR kind='fold'`,
		hash, id, a.Seq, a.CoverFrom, a.Time, a.Title, a.Messages, a.Tokens,
		a.Kind, a.Summary, a.Status, a.MsgSeq, a.TurnEndSeq)
	return err
}

const anchorCols = `seq, cover_from, time, title, messages, tokens,
		COALESCE(kind,'fold'), COALESCE(summary,''), COALESCE(status,'active'),
		COALESCE(msg_seq,0), COALESCE(turn_end_seq,0)`

func scanAnchor(rows *sql.Rows) (AnchorRow, error) {
	var a AnchorRow
	err := rows.Scan(&a.Seq, &a.CoverFrom, &a.Time, &a.Title, &a.Messages, &a.Tokens,
		&a.Kind, &a.Summary, &a.Status, &a.MsgSeq, &a.TurnEndSeq)
	return a, err
}

// ListAnchors returns the persisted fold snapshots of a session, oldest first.
func (d *DB) ListAnchors(hash, id string) ([]AnchorRow, error) {
	rows, err := d.sql.Query(
		`SELECT `+anchorCols+` FROM anchors
		 WHERE hash=? AND id=? ORDER BY seq ASC`, hash, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AnchorRow
	for rows.Next() {
		a, err := scanAnchor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Exec runs a raw statement against the store. Exposed for sibling packages that
// need a targeted update (e.g. closing open turn anchors) without a new method each.
func (d *DB) Exec(query string, args ...interface{}) error {
	_, err := d.sql.Exec(query, args...)
	return err
}

// RefineAnchorSummary replaces the heuristic one-liner with the small-model one.
func (d *DB) RefineAnchorSummary(hash, id string, seq int64, summary string) error {
	_, err := d.sql.Exec(`UPDATE anchors SET summary=?, title=? WHERE hash=? AND id=? AND seq=?`,
		summary, summary, hash, id, seq)
	return err
}

// SetAnchorStatus flips an anchor between active and archived. Archived work
// units are excluded from prompt full-text (their summary stays).
func (d *DB) SetAnchorStatus(hash, id string, seq int64, status string) error {
	_, err := d.sql.Exec(`UPDATE anchors SET status=? WHERE hash=? AND id=? AND seq=?`,
		status, hash, id, seq)
	return err
}

// AnchorHit is an anchor found by search, with the matched snippet.
type AnchorHit struct {
	Anchor  AnchorRow
	HitSeq  int64
	Score   float32
	Snippet string
}

// SearchAnchors maps message-level retrieval onto work units: FTS-recall the
// matching messages, then bucket each hit into the anchor whose
// [msg_seq, turn_end_seq] range covers it. This is how "do it like we did it
// back then" finds the right anchor.
func (d *DB) SearchAnchors(hash, id, query string, limit int) ([]AnchorHit, error) {
	hits, err := d.Search(hash+"/"+id, query, limit*4, limit*8, nil)
	if err != nil || len(hits) == 0 {
		return nil, err
	}
	anchors, err := d.ListAnchors(hash, id)
	if err != nil {
		return nil, err
	}
	// Only turn anchors own message ranges; pick the anchor with the largest
	// msg_seq that still covers the hit.
	bySeq := make([]AnchorRow, 0, len(anchors))
	for _, a := range anchors {
		if a.Kind == "turn" && a.MsgSeq > 0 {
			bySeq = append(bySeq, a)
		}
	}
	best := make(map[int64]AnchorHit) // anchor seq -> best hit
	for _, h := range hits {
		var owner *AnchorRow
		for i := range bySeq {
			a := &bySeq[i]
			// turn_end_seq=0 表示这一轮还没合上（最新一轮总是如此）：它是开区间，
			// 一直管到会话末尾。否则"照上次那个做法做"永远检索不到最近一轮 ——
			// 那恰恰是最常被引用的一轮。
			end := a.TurnEndSeq
			if end <= 0 {
				end = 1 << 62
			}
			if a.MsgSeq <= h.Seq && h.Seq <= end {
				if owner == nil || a.MsgSeq > owner.MsgSeq {
					owner = a
				}
			}
		}
		if owner == nil {
			continue
		}
		prev, ok := best[owner.Seq]
		if !ok || h.Score > prev.Score {
			best[owner.Seq] = AnchorHit{Anchor: *owner, HitSeq: h.Seq, Score: h.Score, Snippet: h.Content}
		}
	}
	out := make([]AnchorHit, 0, len(best))
	for _, ah := range best {
		out = append(out, ah)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}


// DeleteAnchorsAfter drops every anchor newer than seq. Rolling back means those
// points describe a future that no longer exists.
func (d *DB) DeleteAnchorsAfter(hash, id string, seq int64) error {
	_, err := d.sql.Exec(`DELETE FROM anchors WHERE hash=? AND id=? AND seq>?`, hash, id, seq)
	return err
}

// DeleteSession removes every trace of a session (events, messages, projections).
// The session is identified by (hash,id); its event-log rows are keyed by the
// combined "hash/id" session id used by the conversation store.
func (d *DB) DeleteSession(hash, id string) error {
	sid := hash + "/" + id
	if _, err := d.sql.Exec(`DELETE FROM messages_fts WHERE session_id=?`, sid); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`DELETE FROM vectors WHERE session_id=?`, sid); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`DELETE FROM messages WHERE session_id=?`, sid); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`DELETE FROM segments WHERE session_id=?`, sid); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`DELETE FROM requirements WHERE session_id=?`, sid); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`DELETE FROM redlines WHERE session_id=?`, sid); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`DELETE FROM events WHERE session_id=?`, sid); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`DELETE FROM anchors WHERE hash=? AND id=?`, hash, id); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`DELETE FROM sessions WHERE hash=? AND id=?`, hash, id); err != nil {
		return err
	}
	return nil
}

// SnapshotsForPath lists the snapshots of a path in ascending seq order (oldest first).
func (d *DB) SnapshotsForPath(projectHash, path string) ([]SnapshotMeta, error) {
	return d.snapshotsWhere(`project_hash=? AND path=? ORDER BY seq ASC`, projectHash, path)
}

// DistinctSnapshotPaths returns every file path that has at least one snapshot.
func (d *DB) DistinctSnapshotPaths(projectHash string) ([]string, error) {
	rows, err := d.sql.Query(`SELECT DISTINCT path FROM snapshots WHERE project_hash=?`, projectHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// SnapshotContent returns the stored content of a snapshot and whether the file
// existed at snapshot time. A non-existent file yields (content="", existed=false).
func (d *DB) SnapshotContent(m SnapshotMeta) (content string, existed bool, err error) {
	var sha sql.NullString
	if err := d.sql.QueryRow(`SELECT blob_sha FROM snapshots WHERE id=?`, m.Seq).Scan(&sha); err != nil {
		return "", false, err
	}
	if !sha.Valid || sha.String == "" {
		return "", false, nil
	}
	data, e := d.getBlob(sha.String)
	if e != nil {
		return "", false, e
	}
	return string(data), true, nil
}

// DeleteAllSnapshots removes every snapshot of a project and frees its blobs.
func (d *DB) DeleteAllSnapshots(projectHash string) error {
	rows, err := d.sql.Query(`SELECT id, blob_sha FROM snapshots WHERE project_hash=?`, projectHash)
	if err != nil {
		return err
	}
	type row struct {
		id  int64
		sha string
	}
	var ids []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.sha); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, r)
	}
	_ = rows.Close()
	for _, r := range ids {
		if r.sha != "" {
			_ = d.dropBlobRef(r.sha)
		}
	}
	if _, err := d.sql.Exec(`DELETE FROM snapshots WHERE project_hash=?`, projectHash); err != nil {
		return err
	}
	return nil
}

// DeleteAllCheckpoints removes every checkpoint row of a project.
func (d *DB) DeleteAllCheckpoints(projectHash string) error {
	if _, err := d.sql.Exec(`DELETE FROM checkpoints WHERE project_hash=?`, projectHash); err != nil {
		return err
	}
	return nil
}

// DeleteCheckpoint removes a single checkpoint row (its snapshots are kept).
func (d *DB) DeleteCheckpoint(projectHash, id string) error {
	if _, err := d.sql.Exec(`DELETE FROM checkpoints WHERE id=? AND project_hash=?`, id, projectHash); err != nil {
		return err
	}
	return nil
}

// SetCurrentCheckpoint records which checkpoint is currently active for a project.
func (d *DB) SetCurrentCheckpoint(projectHash, id string) error {
	key := "curcp:" + projectHash
	if _, err := d.sql.Exec(
		`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`,
		key, id); err != nil {
		return err
	}
	return nil
}

// GetCurrentCheckpoint returns the active checkpoint id for a project ("" if none).
func (d *DB) GetCurrentCheckpoint(projectHash string) (string, error) {
	var v sql.NullString
	if err := d.sql.QueryRow(`SELECT v FROM meta WHERE k=?`, "curcp:"+projectHash).Scan(&v); err != nil {
		return "", err
	}
	if !v.Valid {
		return "", nil
	}
	return v.String, nil
}

// ── 每轮文件改动（审查/版本管理）────────────────────────────────────────────
// 一个 turn 锚点 = 一轮工作。AI 在这一轮里改过的文件、改前/改后内容都记在这里，
// 内容本身在 blobs 里按 sha 寻址，本表只留引用和行数统计。

// AnchorFileRow is one file changed during one turn (anchor seq).
type AnchorFileRow struct {
	Seq       int64
	Path      string
	Status    string // A | M | D
	BeforeSHA string // "" for new files
	AfterSHA  string // "" for deleted files
	Added     int
	Removed   int
	UpdatedAt int64
}

// UpsertAnchorFile records (or refreshes) one changed file of one turn.
func (d *DB) UpsertAnchorFile(hash, id string, f AnchorFileRow) error {
	_, err := d.sql.Exec(
		`INSERT INTO anchor_files(hash, id, seq, path, status, before_sha, after_sha, added, removed, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(hash, id, seq, path) DO UPDATE SET
		   status=excluded.status, before_sha=excluded.before_sha, after_sha=excluded.after_sha,
		   added=excluded.added, removed=excluded.removed, updated_at=excluded.updated_at`,
		hash, id, f.Seq, f.Path, f.Status, f.BeforeSHA, f.AfterSHA, f.Added, f.Removed, f.UpdatedAt)
	return err
}

// ListAnchorFiles returns the files changed in one turn, oldest path first.
func (d *DB) ListAnchorFiles(hash, id string, seq int64) ([]AnchorFileRow, error) {
	rows, err := d.sql.Query(
		`SELECT seq,path,status,before_sha,after_sha,added,removed,updated_at
		 FROM anchor_files WHERE hash=? AND id=? AND seq=? ORDER BY path ASC`, hash, id, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AnchorFileRow
	for rows.Next() {
		var f AnchorFileRow
		if err := rows.Scan(&f.Seq, &f.Path, &f.Status, &f.BeforeSHA, &f.AfterSHA, &f.Added, &f.Removed, &f.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AnchorFilesForPath returns every turn that touched a path, oldest turn first.
// This is the version chain the UI walks to decide whether an old hunk can still
// be applied (a later turn editing the same lines blocks it).
func (d *DB) AnchorFilesForPath(hash, id, path string) ([]AnchorFileRow, error) {
	rows, err := d.sql.Query(
		`SELECT seq,path,status,before_sha,after_sha,added,removed,updated_at
		 FROM anchor_files WHERE hash=? AND id=? AND path=? ORDER BY seq ASC`, hash, id, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AnchorFileRow
	for rows.Next() {
		var f AnchorFileRow
		if err := rows.Scan(&f.Seq, &f.Path, &f.Status, &f.BeforeSHA, &f.AfterSHA, &f.Added, &f.Removed, &f.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteAnchorFilesAfter drops the file records of turns after seq (rollback).
func (d *DB) DeleteAnchorFilesAfter(hash, id string, seq int64) error {
	_, err := d.sql.Exec(`DELETE FROM anchor_files WHERE hash=? AND id=? AND seq>?`, hash, id, seq)
	return err
}

// SetTurnBase stores the git tree a turn started from (git stash create output)
// plus the sha of the begin-of-turn file manifest blob (see capture.go).
func (d *DB) SetTurnBase(hash, id string, seq int64, tree string, isGit bool, snapSha string) error {
	g := 0
	if isGit {
		g = 1
	}
	_, err := d.sql.Exec(
		`INSERT INTO anchor_turn_bases(hash, id, seq, tree, git, ts, snap_sha) VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(hash, id, seq) DO UPDATE SET tree=excluded.tree, git=excluded.git, ts=excluded.ts, snap_sha=excluded.snap_sha`,
		hash, id, seq, tree, g, time.Now().UnixMilli(), snapSha)
	return err
}

// GetTurnBase returns the recorded base tree of a turn ("" when unknown),
// whether that base was a git tree, and the begin-of-turn manifest blob sha.
func (d *DB) GetTurnBase(hash, id string, seq int64) (tree string, isGit bool, snapSha string, err error) {
	var t, snap sql.NullString
	var git int
	err = d.sql.QueryRow(
		`SELECT tree, git, COALESCE(snap_sha,'') FROM anchor_turn_bases WHERE hash=? AND id=? AND seq=?`, hash, id, seq).
		Scan(&t, &git, &snap)
	if err != nil {
		return "", false, "", err
	}
	return t.String, git == 1, snap.String, nil
}

// OpenTurnSeqs returns the seqs of turn anchors that were never closed
// (turn_end_seq = 0) — these still owe a file-capture pass.
func (d *DB) OpenTurnSeqs(hash, id string) ([]int64, error) {
	rows, err := d.sql.Query(
		`SELECT seq FROM anchors WHERE hash=? AND id=? AND COALESCE(kind,'fold')='turn' AND turn_end_seq=0 ORDER BY seq ASC`,
		hash, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RecordAnchorApply logs one "apply this hunk" action (audit trail).
func (d *DB) RecordAnchorApply(hash, id string, seq int64, path string, hunk int, ok bool, note string) error {
	v := 0
	if ok {
		v = 1
	}
	_, err := d.sql.Exec(
		`INSERT INTO anchor_applies(hash, id, seq, path, hunk, ok, note, applied_at) VALUES(?,?,?,?,?,?,?,?)`,
		hash, id, seq, path, hunk, v, note, time.Now().UnixMilli())
	return err
}

// ListAnchorApplies returns the apply log of one turn, oldest first.
func (d *DB) ListAnchorApplies(hash, id string, seq int64) ([]AnchorApplyRow, error) {
	rows, err := d.sql.Query(
		`SELECT seq,path,hunk,ok,note,applied_at FROM anchor_applies WHERE hash=? AND id=? AND seq=? ORDER BY applied_at ASC`,
		hash, id, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AnchorApplyRow
	for rows.Next() {
		var r AnchorApplyRow
		if err := rows.Scan(&r.Seq, &r.Path, &r.Hunk, &r.OK, &r.Note, &r.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AnchorApplyRow is one logged "apply this hunk" action.
type AnchorApplyRow struct {
	Seq       int64
	Path      string
	Hunk      int
	OK        bool
	Note      string
	AppliedAt int64
}

// PutBlob stores content in the content-addressed blob store and returns its
// sha. Exported for the turn-capture recorder, which keeps file versions there.
func (d *DB) PutBlob(data []byte) (string, error) { return d.putBlob(data) }

// GetBlob reads blob content by sha.
func (d *DB) GetBlob(sha string) ([]byte, error) { return d.getBlob(sha) }

// DropBlobRef releases one reference to a blob (removes it at zero refs).
func (d *DB) DropBlobRef(sha string) error { return d.dropBlobRef(sha) }

// DeleteAnchorFiles drops every file row of one turn (a turn is re-captured
// from scratch each time, while it is open or on refresh).
func (d *DB) DeleteAnchorFiles(hash, id string, seq int64) error {
	_, err := d.sql.Exec(`DELETE FROM anchor_files WHERE hash=? AND id=? AND seq=?`, hash, id, seq)
	return err
}

// DeleteAnchorAppliesAfter drops the apply log of turns after seq (rollback).
func (d *DB) DeleteAnchorAppliesAfter(hash, id string, seq int64) error {
	_, err := d.sql.Exec(`DELETE FROM anchor_applies WHERE hash=? AND id=? AND seq>?`, hash, id, seq)
	return err
}

// DeleteTurnBasesAfter drops the begin-of-turn base records after seq, so a
// rolled-back turn can never be captured again.
func (d *DB) DeleteTurnBasesAfter(hash, id string, seq int64) error {
	_, err := d.sql.Exec(`DELETE FROM anchor_turn_bases WHERE hash=? AND id=? AND seq>?`, hash, id, seq)
	return err
}
