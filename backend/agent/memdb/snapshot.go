package memdb

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Checkpoint is a named snapshot anchor for file rollback.
type Checkpoint struct {
	ID      string
	Name    string
	Desc    string
	TS      int64
	SnapSeq []int64 // snapshot ids captured at this checkpoint
}

// SnapshotMeta describes one stored file snapshot.
type SnapshotMeta struct {
	Seq          int64
	ProjectHash  string
	Path         string
	CheckpointID string
	Size         int
	Existed      bool
	TS           int64
}

// SnapshotFile records the current content of path before (or after) a mutation.
// content is "" when the file did not exist; existed=false means "was absent" and a
// rollback will os.Remove it. Returns the snapshot seq for later rollback targeting.
//
// Each snapshot is tagged with the checkpoint id that was active when it was taken,
// so a checkpoint isolates exactly its own snapshots — it does NOT mutate or copy
// the snapshot history of other checkpoints (this is the bug the in-memory manager
// had: it rewrote every prior snapshot's checkpoint id).
func (d *DB) SnapshotFile(projectHash, checkpointID, path string, content []byte, existed bool) (int64, error) {
	sha := ""
	size := 0
	if existed {
		s, err := d.putBlob(content)
		if err != nil {
			return 0, err
		}
		sha = s
		size = len(content)
	}
	res, err := d.sql.Exec(
		`INSERT INTO snapshots(project_hash,path,seq,checkpoint_id,blob_sha,size,existed,ts) VALUES(?,?,?,?,?,?,?,?)`,
		projectHash, path, nextSnapSeq(d, projectHash, path), checkpointID, sha, size, boolToInt(existed), time.Now().UnixMilli())
	if err != nil {
		if existed && sha != "" {
			_ = d.dropBlobRef(sha)
		}
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// nextSnapSeq returns the next per-(project,path) sequence for stable rollback order.
func nextSnapSeq(d *DB, projectHash, path string) int64 {
	var seq int64
	_ = d.sql.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM snapshots WHERE project_hash=? AND path=?`, projectHash, path).Scan(&seq)
	return seq + 1
}

// CreateCheckpoint inserts a named checkpoint for the project.
func (d *DB) CreateCheckpoint(projectHash, id, name, desc string) error {
	_, err := d.sql.Exec(
		`INSERT INTO checkpoints(id,project_hash,name,desc,ts) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name, desc=excluded.desc`,
		id, projectHash, name, desc, time.Now().UnixMilli())
	return err
}

// GetCheckpoints lists checkpoints for a project, newest first.
func (d *DB) GetCheckpoints(projectHash string) ([]Checkpoint, error) {
	rows, err := d.sql.Query(`SELECT id,name,desc,ts FROM checkpoints WHERE project_hash=? ORDER BY ts DESC`, projectHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		if err := rows.Scan(&c.ID, &c.Name, &c.Desc, &c.TS); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// SnapshotsForCheckpoint returns the snapshot metadata captured under a checkpoint.
func (d *DB) SnapshotsForCheckpoint(projectHash, checkpointID string) ([]SnapshotMeta, error) {
	return d.snapshotsWhere(`project_hash=? AND checkpoint_id=?`, projectHash, checkpointID)
}

// LatestSnapshotForPath returns the most recent snapshot of path (for rollback).
func (d *DB) LatestSnapshotForPath(projectHash, path string) (SnapshotMeta, bool, error) {
	rows, err := d.sql.Query(
		`SELECT id,project_hash,path,checkpoint_id,size,existed,ts FROM snapshots WHERE project_hash=? AND path=? ORDER BY seq DESC LIMIT 1`,
		projectHash, path)
	if err != nil {
		return SnapshotMeta{}, false, err
	}
	defer rows.Close()
	if rows.Next() {
		var m SnapshotMeta
		if err := rows.Scan(&m.Seq, &m.ProjectHash, &m.Path, &m.CheckpointID, &m.Size, &m.Existed, &m.TS); err != nil {
			return SnapshotMeta{}, false, err
		}
		return m, true, nil
	}
	return SnapshotMeta{}, false, nil
}

func (d *DB) snapshotsWhere(where string, args ...interface{}) ([]SnapshotMeta, error) {
	rows, err := d.sql.Query(`SELECT id,project_hash,path,checkpoint_id,size,existed,ts FROM snapshots WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SnapshotMeta
	for rows.Next() {
		var m SnapshotMeta
		if err := rows.Scan(&m.Seq, &m.ProjectHash, &m.Path, &m.CheckpointID, &m.Size, &m.Existed, &m.TS); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// RestoreSnapshot writes (or removes) the file to match the snapshot's recorded
// content. This is the actual file-level rollback.
func (d *DB) RestoreSnapshot(m SnapshotMeta) error {
	if !m.Existed {
		return os.Remove(m.Path)
	}
	data, err := d.getBlobContent(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.Path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.Path, data, 0o644)
}

func (d *DB) getBlobContent(m SnapshotMeta) ([]byte, error) {
	// Resolve the blob sha for this snapshot.
	var sha string
	if err := d.sql.QueryRow(`SELECT blob_sha FROM snapshots WHERE id=?`, m.Seq).Scan(&sha); err != nil {
		return nil, err
	}
	if sha == "" {
		return nil, fmt.Errorf("memdb: snapshot %d has no blob", m.Seq)
	}
	return d.getBlob(sha)
}

// RollbackFile restores a single file to its latest snapshot.
func (d *DB) RollbackFile(projectHash, path string) error {
	m, ok, err := d.LatestSnapshotForPath(projectHash, path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("memdb: no snapshot for %s", path)
	}
	return d.RestoreSnapshot(m)
}

// RollbackToCheckpoint restores every file captured under the checkpoint, then
// removes snapshots that were taken AFTER it (so a later rollback can't see them).
func (d *DB) RollbackToCheckpoint(projectHash, checkpointID string) error {
	cp, err := d.GetCheckpoints(projectHash)
	if err != nil {
		return err
	}
	var found *Checkpoint
	for i := range cp {
		if cp[i].ID == checkpointID {
			found = &cp[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("memdb: checkpoint not found: %s", checkpointID)
	}
	snaps, err := d.SnapshotsForCheckpoint(projectHash, checkpointID)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		if err := d.RestoreSnapshot(s); err != nil {
			return fmt.Errorf("memdb: restore %s: %w", s.Path, err)
		}
	}
	// Drop snapshots taken after the checkpoint's timestamp.
	if _, err := d.sql.Exec(`DELETE FROM snapshots WHERE project_hash=? AND ts > ?`, projectHash, found.TS); err != nil {
		return err
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
