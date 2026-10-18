// Package memdb is the unified SQLite-backed store for the agent's memory system.
//
// It consolidates what used to be three disconnected stores (the markdown
// auto-memory tree, the project-root memory_store.json, and the per-session
// journal.jsonl) plus the file-snapshot rollback manager into ONE database so a
// multi-month project can be searched, compressed, and reasoned about with
// indexed queries instead of replaying/scanning everything.
//
// Design notes:
//   - `events` is the single source of truth (append-only). Rollback appends a
//     "rollback" event; the audit trail is never rewritten.
//   - `messages`, `segments`, `requirements`, `redlines` are MATERIALIZED
//     projections kept in sync on write, so reads are O(log n) via indexes.
//   - FTS5 with CJK bigram preprocessing gives sub-linear full-text retrieval
//     (the default unicode61 tokenizer treats a run of Chinese as one token and
//     cannot match a sub-phrase).
//   - Two-phase retrieval (FTS5 recall -> vector rerank over the candidates only)
//     avoids a full O(n) vector scan over months of history.
//   - File snapshot *content* is NOT stored in SQLite (BLOB bloat would evict the
//     page cache); it is content-addressed (sha256) under blobsDir and referenced
//     by `snapshots.blob_sha`.
package memdb

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

// blobDirName is the subdirectory (under the DB's parent) holding snapshot content.
const blobDirName = "blobs"

// schemaVersion is bumped whenever the DDL changes in an incompatible way.
const schemaVersion = 1

// DB is a handle to the unified memory database.
type DB struct {
	sql  *sql.DB
	mu   sync.Mutex // guards per-statement retries / schema touches
	path string
}

// Open opens (creating if needed) the unified store at dbPath, enabling WAL and a
// busy timeout so multiple instances (the watchdog on this machine relaunches the
// app) can share the file instead of hitting SQLITE_BUSY.
func Open(dbPath string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("memdb: open %s: %w", dbPath, err)
	}
	// WAL + generous busy timeout: the watchdog can relaunch the app while an
	// old instance still holds a connection to the same file.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := sqlDB.Exec(pragma); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("memdb: %s: %w", pragma, err)
		}
	}
	d := &DB{sql: sqlDB, path: dbPath}
	if err := d.initSchema(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// path returns the underlying database file path.
func (d *DB) Path() string { return d.path }

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }

func (d *DB) initSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT)`,
		fmt.Sprintf(`INSERT OR IGNORE INTO meta(k,v) VALUES ('schema_version', '%d')`, schemaVersion),

		`CREATE TABLE IF NOT EXISTS projects(hash TEXT PRIMARY KEY, root TEXT, created_at INTEGER, last_at INTEGER)`,

		`CREATE TABLE IF NOT EXISTS sessions(hash TEXT, id TEXT, title TEXT, created_at INTEGER, last_at INTEGER, last_seq INTEGER DEFAULT 0, tail_json TEXT, tail_seq INTEGER DEFAULT 0, PRIMARY KEY(hash,id))`,

		`CREATE TABLE IF NOT EXISTS events(session_id TEXT, seq INTEGER, kind TEXT, ts INTEGER, payload TEXT, refs INTEGER DEFAULT 0, PRIMARY KEY(session_id, seq))`,
		`CREATE INDEX IF NOT EXISTS idx_events_sk ON events(session_id, kind, seq)`,

		// 折叠快照的历史点（= UI 上的「锚点」）。sessions.tail_json 只保存最新一份且
		// 会被反复覆盖，无法支撑「回退到历史上某个锚点」，所以这里单独留一份履历。
		`CREATE TABLE IF NOT EXISTS anchors(hash TEXT, id TEXT, seq INTEGER, cover_from INTEGER DEFAULT 0, time INTEGER, title TEXT, messages INTEGER DEFAULT 0, tokens INTEGER DEFAULT 0, PRIMARY KEY(hash, id, seq))`,
		`CREATE INDEX IF NOT EXISTS idx_anchors_sess ON anchors(hash, id, seq)`,
		// 锚点扩展列（逐列迁移，兼容已存在的库）：见 docs/anchor-design.md。
		`ALTER TABLE anchors ADD COLUMN kind TEXT DEFAULT 'fold'`,
		`ALTER TABLE anchors ADD COLUMN summary TEXT DEFAULT ''`,
		`ALTER TABLE anchors ADD COLUMN status TEXT DEFAULT 'active'`,
		`ALTER TABLE anchors ADD COLUMN msg_seq INTEGER DEFAULT 0`,
		`ALTER TABLE anchors ADD COLUMN turn_end_seq INTEGER DEFAULT 0`,

		`CREATE TABLE IF NOT EXISTS messages(session_id TEXT, seq INTEGER, role TEXT, content TEXT, seg_id TEXT, ts INTEGER, tok INTEGER, PRIMARY KEY(session_id, seq))`,
		`CREATE INDEX IF NOT EXISTS idx_messages_seg ON messages(session_id, seg_id, seq)`,

		`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(session_id UNINDEXED, seq UNINDEXED, content, tokenize='unicode61')`,

		`CREATE TABLE IF NOT EXISTS segments(id TEXT, session_id TEXT, idx INTEGER, title TEXT, status TEXT, start_seq INTEGER, end_seq INTEGER, summary TEXT, tok INTEGER, created_at INTEGER, closed_at INTEGER, PRIMARY KEY(session_id, id))`,

		`CREATE TABLE IF NOT EXISTS requirements(id TEXT, session_id TEXT, text TEXT, status TEXT, source_seq INTEGER, created_at INTEGER, PRIMARY KEY(session_id, id))`,

		`CREATE TABLE IF NOT EXISTS redlines(id TEXT, session_id TEXT, text TEXT, severity TEXT, source_seq INTEGER, created_at INTEGER, PRIMARY KEY(session_id, id), UNIQUE(session_id, text))`,

		`CREATE TABLE IF NOT EXISTS snapshots(id INTEGER PRIMARY KEY AUTOINCREMENT, project_hash TEXT, path TEXT, seq INTEGER, checkpoint_id TEXT, blob_sha TEXT, size INTEGER, existed INTEGER, ts INTEGER)`,
		`CREATE INDEX IF NOT EXISTS idx_snap_path ON snapshots(project_hash, path, seq)`,
		`CREATE INDEX IF NOT EXISTS idx_snap_cp ON snapshots(project_hash, checkpoint_id)`,

		`CREATE TABLE IF NOT EXISTS checkpoints(id TEXT PRIMARY KEY, project_hash TEXT, name TEXT, desc TEXT, ts INTEGER)`,

		`CREATE TABLE IF NOT EXISTS blobs(sha TEXT PRIMARY KEY, size INTEGER, refs INTEGER DEFAULT 0, ts INTEGER)`,

		`CREATE TABLE IF NOT EXISTS vectors(session_id TEXT, seq INTEGER, dim INTEGER, vec BLOB, PRIMARY KEY(session_id, seq))`,

		`CREATE TABLE IF NOT EXISTS links(from_seg TEXT, to_seg TEXT, kind TEXT, score REAL, reason TEXT, PRIMARY KEY(from_seg, to_seg, kind))`,
		`CREATE INDEX IF NOT EXISTS idx_links_to ON links(to_seg)`,

		// 每轮（turn 锚点）改动过的文件与改前/改后版本。内容走 blobs（内容寻址），
		// 这里只存 sha 引用与行数统计。见 docs/anchor-design.md「审查」。
		`CREATE TABLE IF NOT EXISTS anchor_files(hash TEXT, id TEXT, seq INTEGER, path TEXT, status TEXT, before_sha TEXT DEFAULT '', after_sha TEXT DEFAULT '', added INTEGER DEFAULT 0, removed INTEGER DEFAULT 0, updated_at INTEGER, PRIMARY KEY(hash, id, seq, path))`,
		`CREATE INDEX IF NOT EXISTS idx_anchor_files_path ON anchor_files(hash, id, path, seq)`,

		// 每轮开工时的 git 基点树（git stash create，不碰工作区）。轮末拿它做树差分，
		// 得到这一轮精确的改前/改后。跨重启后补抓未合的轮次也要靠它。
		`CREATE TABLE IF NOT EXISTS anchor_turn_bases(hash TEXT, id TEXT, seq INTEGER, tree TEXT, git INTEGER DEFAULT 0, ts INTEGER, PRIMARY KEY(hash, id, seq))`,
		// snap_sha：轮次开工时的文件清单（哈希+路径）blob。git 项目里是未跟踪文件清单
		// （配合同目录硬链接快照补抓新文件），非 git 项目里是全部文本文件清单。
		`ALTER TABLE anchor_turn_bases ADD COLUMN snap_sha TEXT DEFAULT ''`,

		// 「应用此段」流水：用户把某个锚点的某一段改动落回工作区的记录。
		`CREATE TABLE IF NOT EXISTS anchor_applies(hash TEXT, id TEXT, seq INTEGER, path TEXT, hunk INTEGER, ok INTEGER, note TEXT, applied_at INTEGER)`,
	}
	for _, s := range stmts {
		_, err := d.sql.Exec(s)
		if err == nil {
			continue
		}
		// 列迁移用的 ALTER TABLE 在列已存在时会报错，这是正常路径（老库升级），
		// 忽略之；其余错误照常失败。
		if strings.HasPrefix(s, "ALTER TABLE") && strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			continue
		}
		return fmt.Errorf("memdb: init schema (%s): %w", s[:min(48, len(s))], err)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── tokenize: CJK bigram + latin word, for FTS5 ──────────────────────────────

// tokenize prepares text for FTS5 indexing/querying. Latin runs become lowercase
// words (unicode61 tokenizes letter runs); Han runs become overlapping bigrams so
// a 2-character Chinese query can match. This is what makes Chinese sub-phrase
// search work where the default tokenizer fails.
func tokenize(s string) string {
	var b strings.Builder
	rs := []rune(s)
	var latin []rune
	var han []rune
	flushH := func() {
		if len(han) == 1 {
			b.WriteString(string(han))
			b.WriteByte(' ')
		}
		for i := 0; i+1 < len(han); i++ {
			b.WriteString(string(han[i]))
			b.WriteString(string(han[i+1]))
			b.WriteByte(' ')
		}
		han = nil
	}
	flushL := func() {
		if len(latin) > 0 {
			b.WriteString(strings.ToLower(string(latin)))
			b.WriteByte(' ')
			latin = nil
		}
	}
	for _, r := range rs {
		switch {
		case unicode.Is(unicode.Han, r):
			flushL()
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			flushH()
			latin = append(latin, r)
		default:
			flushL()
			flushH()
		}
	}
	flushL()
	flushH()
	return strings.TrimSpace(b.String())
}

// ── content-addressed blobs (file snapshot content lives here, not in SQLite) ─

func (d *DB) blobsDir() string { return filepath.Join(filepath.Dir(d.path), blobDirName) }

// putBlob stores data under sha256 and returns the hex digest. Refs are bumped on
// insert so GC never deletes a referenced blob.
func (d *DB) putBlob(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	blobPath := filepath.Join(d.blobsDir(), sha[:2], sha)
	if _, err := os.Stat(blobPath); err != nil {
		full := filepath.Dir(blobPath)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(blobPath, data, 0o644); err != nil {
			return "", err
		}
	}
	if _, err := d.sql.Exec(
		`INSERT INTO blobs(sha,size,refs,ts) VALUES(?,?,1,?) ON CONFLICT(sha) DO UPDATE SET refs=refs+1`,
		sha, len(data), time.Now().UnixMilli()); err != nil {
		return "", err
	}
	return sha, nil
}

// getBlob reads content-backing data by sha.
func (d *DB) getBlob(sha string) ([]byte, error) {
	return os.ReadFile(filepath.Join(d.blobsDir(), sha[:2], sha))
}

// dropBlobRef decrements a blob's reference count; when it hits zero the file is
// removed. Caller must have removed the referencing snapshot row first.
func (d *DB) dropBlobRef(sha string) error {
	if sha == "" {
		return nil
	}
	res, err := d.sql.Exec(`UPDATE blobs SET refs=refs-1 WHERE sha=?`, sha)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	var refs int
	if err := d.sql.QueryRow(`SELECT refs FROM blobs WHERE sha=?`, sha).Scan(&refs); err != nil {
		return err
	}
	if refs <= 0 {
		if _, err := d.sql.Exec(`DELETE FROM blobs WHERE sha=?`, sha); err != nil {
			return err
		}
		_ = os.Remove(filepath.Join(d.blobsDir(), sha[:2], sha))
	}
	return nil
}

// ── vector (de)serialization ─────────────────────────────────────────────────

// encodeVec packs a float32 slice into little-endian bytes for the vectors table.
func encodeVec(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(x))
	}
	return buf
}

// decodeVec unpacks little-endian float32 bytes back into a slice.
func decodeVec(buf []byte) []float32 {
	n := len(buf) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return v
}
