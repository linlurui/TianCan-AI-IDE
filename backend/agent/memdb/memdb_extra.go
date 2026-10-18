package memdb

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
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
