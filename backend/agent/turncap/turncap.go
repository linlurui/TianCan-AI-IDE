package turncap

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/memdb"
)

// maxTextFile skips anything larger: versions are for source code, not build
// artifacts or media.
const maxTextFile = 1 << 20

// skipDirs are never walked, hashed or snapshotted (build output, VCS, IDE
// metadata). Mirrors the watcher's ignore list.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, ".next": true, "target": true, "__pycache__": true,
	".venv": true, ".tiancan": true, ".idea": true, ".vscode": true,
}

// Recorder captures the file changes of every turn of one session and keeps
// their versions in memdb. One recorder per (project, session); all methods are
// safe for concurrent use.
type Recorder struct {
	root string
	db   *memdb.DB
	hash string
	id   string

	mu  sync.Mutex
	git bool
}

// NewRecorder builds the recorder for a session. git is detected once: a git
// project captures via tree diffs, everything else via blob snapshots.
func NewRecorder(root string, db *memdb.DB, hash, id string) *Recorder {
	r := &Recorder{root: root, db: db, hash: hash, id: id}
	r.git = r.isGitRepo()
	return r
}

// Begin marks the start of a turn: record the base state the turn will be
// diffed against. For git that is a stash commit (worktree untouched); the
// untracked files are blobbed because trees do not carry them. Without git
// every text file is blobbed.
//
// The before-version must be taken BEFORE the change happens, and it must be a
// copy: a hardlink would be silently rewritten in place by any tool that writes
// without replacing the file (os.WriteFile, sed -i, many editors), turning the
// "old" version into the new one — the one failure a version history cannot
// have. Blobs are content-addressed, so repeated turns cost almost nothing.
func (r *Recorder) Begin(seq int64) {
	if seq <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	var tree string
	var paths []string
	if r.git {
		if t, err := r.stashTree(); err == nil {
			tree = t
		} else {
			// 基点树没取到（仓库锁、git 故障）：退化成全量 blob，
			// 绝不能只记未跟踪文件——那会把 tracked 文件的改动全丢了。
			paths = r.walkTextFiles()
		}
		if tree != "" {
			if u, err := r.untrackedFiles(); err == nil {
				paths = u
			}
		}
	} else {
		paths = r.walkTextFiles()
	}

	m := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(p)))
		if err != nil || !isText(b) {
			continue
		}
		if sha, err := r.db.PutBlob(b); err == nil {
			m[p] = sha
		}
	}
	snapSha := ""
	if len(m) > 0 {
		if sha, err := r.db.PutBlob(marshalManifest(m)); err == nil {
			snapSha = sha
		}
	}
	_ = r.db.SetTurnBase(r.hash, r.id, seq, tree, r.git && tree != "", snapSha)
}

// Capture computes and persists the file changes of a turn. Idempotent: calling
// it again (on archive, on review, on shutdown) just refreshes the rows.
//
// Only an OPEN turn is captured. A closed turn's record was taken the moment it
// closed; refreshing it later would diff its base tree against whatever the
// worktree looks like now — which includes every later turn's edits — and
// silently rewrite history (that is how a review of turn 10 used to show
// turn 20's changes as turn 10's "after" version).
func (r *Recorder) Capture(seq int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.capture(seq)
}

// CaptureOpen flushes every still-open turn of the session (app shutdown).
func (r *Recorder) CaptureOpen() {
	seqs, err := r.db.OpenTurnSeqs(r.hash, r.id)
	if err != nil {
		return
	}
	for _, s := range seqs {
		_ = r.Capture(s)
	}
}

// capture must be called with the mutex held.
func (r *Recorder) capture(seq int64) error {
	if seq <= 0 {
		return nil
	}
	open, err := r.db.IsOpenTurn(r.hash, r.id, seq)
	if err != nil || !open {
		// 轮次已关闭（记录定型）或查不动：不碰历史记录。
		// 关轮时的补抓若失败，这个轮次就显示"没有记录到文件改动"——
		// 好过拿后续轮次的状态冒充它的改后版本。
		return nil
	}
	base, isGit, snapSha, err := r.db.GetTurnBase(r.hash, r.id, seq)
	if err != nil {
		return nil // 没有基点：这个轮次早于功能上线，无从查起
	}
	if isGit && base != "" {
		return r.captureGit(seq, base, snapSha)
	}
	if snapSha != "" {
		return r.captureSnap(seq, snapSha)
	}
	return nil
}

// captureGit diffs the base tree against the current worktree state.
func (r *Recorder) captureGit(seq int64, base, snapSha string) error {
	end, err := r.stashTree()
	if err != nil {
		return err
	}
	changes, err := r.diffNameStatus(base, end)
	if err != nil {
		return err
	}

	// 树里没有未跟踪文件：开工时快照过清单的，逐个比对内容。
	old := map[string]string{}
	if snapSha != "" {
		if data, err := r.db.GetBlob(snapSha); err == nil {
			old = parseManifest(data)
		}
	}
	cur, err := r.untrackedFiles()
	if err != nil {
		// 列不出未跟踪文件就不能推断删除：否则一次 git 故障会把整个清单
		// 误报成 D。树差分本身仍然可信，原样返回它的结果。
		return r.storeChanges(seq, changes, base, end, old)
	}
	curSet := map[string]bool{}
	for _, p := range cur {
		curSet[p] = true
	}
	// 已跟踪文件清单：开工时未跟踪、期间被 git add 的文件仍在工作区，
	// 不能因为"不在未跟踪列表里"就判删除。
	tracked, err := r.trackedFiles()
	if err != nil {
		tracked = nil
	}
	seen := map[string]bool{}
	for _, c := range changes {
		seen[c.path] = true
	}
	for p, oldSha := range old {
		if seen[p] {
			continue
		}
		if !curSet[p] {
			if tracked[p] {
				continue // 已被 git add：它的变更由树差分负责
			}
			changes = append(changes, change{status: "D", path: p})
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(p)))
		if err != nil || !isText(data) {
			continue
		}
		if sha256hex(data) != oldSha {
			changes = append(changes, change{status: "M", path: p})
		}
	}
	for _, p := range cur {
		if _, ok := old[p]; ok || seen[p] {
			continue
		}
		changes = append(changes, change{status: "A", path: p})
	}
	return r.storeChanges(seq, changes, base, end, old)
}

// captureSnap is the no-git path: compare the begin-of-turn manifest against
// the current walk; old content comes from the hardlink snapshot.
func (r *Recorder) captureSnap(seq int64, snapSha string) error {
	data, err := r.db.GetBlob(snapSha)
	if err != nil {
		return err
	}
	old := parseManifest(data)
	cur := map[string]string{}
	for _, p := range r.walkTextFiles() {
		if b, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(p))); err == nil && isText(b) {
			cur[p] = sha256hex(b)
		}
	}
	var changes []change
	for p, sha := range cur {
		if oldSha, ok := old[p]; !ok {
			changes = append(changes, change{status: "A", path: p})
		} else if oldSha != sha {
			changes = append(changes, change{status: "M", path: p})
		}
	}
	for p := range old {
		if _, ok := cur[p]; !ok {
			changes = append(changes, change{status: "D", path: p})
		}
	}
	return r.storeChanges(seq, changes, "", "", old)
}

// storeChanges writes one row per changed file. Old rows of the same turn are
// replaced (a turn can be captured repeatedly while it is still open).
//
// Exception: a file the user already restored a hunk of keeps its recorded
// versions. The working copy no longer matches what the AI left, so a fresh
// capture would overwrite the versions with post-restore content and the
// apply log's hunk indexes would silently drift onto different regions.
func (r *Recorder) storeChanges(seq int64, changes []change, baseTree, endTree string, manifest map[string]string) error {
	frozen := map[string]bool{}
	if m, err := r.db.AnchorAppliedPaths(r.hash, r.id, seq); err == nil {
		frozen = m
	}
	// 先删旧行并释放它们的 blob 引用，否则重复捕获会堆积垃圾。
	if olds, err := r.db.ListAnchorFiles(r.hash, r.id, seq); err == nil {
		for _, o := range olds {
			if frozen[o.Path] {
				continue // 人工干预过的行连同它的 blob 一起保留
			}
			_ = r.db.DropBlobRef(o.BeforeSHA)
			_ = r.db.DropBlobRef(o.AfterSHA)
		}
	}
	if err := r.db.DeleteAnchorFilesUnfrozen(r.hash, r.id, seq, frozen); err != nil {
		return err
	}

	now := nowMs()
	for _, c := range changes {
		if frozen[c.path] {
			continue // 该文件已被人工还原过：保留当时的记录，不刷新
		}
		var before, after []byte
		switch c.status {
		case "A":
			// 新文件：改前为空。
		case "D":
			before = r.versionContent(c.path, baseTree, manifest, true)
		default:
			before = r.versionContent(c.path, baseTree, manifest, true)
			after = r.versionContent(c.path, endTree, manifest, false)
		}
		if c.status != "D" && (len(after) == 0 && c.status != "A") {
			// 改动却读不到改后内容（文件被删/读不了）：按删除记。
			c.status = "D"
			after = nil
		}
		if len(before) > 0 && !isText(before) {
			before = nil
		}
		if len(after) > 0 && !isText(after) {
			after = nil
		}

		beforeSha, afterSha := "", ""
		if len(before) > 0 {
			if sha, err := r.db.PutBlob(before); err == nil {
				beforeSha = sha
			}
		}
		if len(after) > 0 {
			if sha, err := r.db.PutBlob(after); err == nil {
				afterSha = sha
			}
		}
		added, removed := 0, 0
		if beforeSha != "" && afterSha != "" {
			for _, h := range Hunks(string(before), string(after)) {
				added += len(h.After)
				removed += len(h.Before)
			}
		} else if afterSha != "" {
			added = countLines(string(after))
		} else if beforeSha != "" {
			removed = countLines(string(before))
		}
		_ = r.db.UpsertAnchorFile(r.hash, r.id, memdb.AnchorFileRow{
			Seq: seq, Path: c.path, Status: c.status,
			BeforeSHA: beforeSha, AfterSHA: afterSha,
			Added: added, Removed: removed, UpdatedAt: now,
		})
	}
	return nil
}

// versionContent reads one version of a path: from a git tree when given,
// otherwise from the begin-of-turn blob manifest (old) or the worktree (new).
func (r *Recorder) versionContent(path, tree string, manifest map[string]string, old bool) []byte {
	if tree != "" {
		if b, err := r.catBlob(tree, path); err == nil {
			return b
		}
	}
	if old {
		sha := manifest[path]
		if sha == "" {
			return nil
		}
		b, err := r.db.GetBlob(sha)
		if err != nil {
			return nil
		}
		return b
	}
	b, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(path)))
	if err != nil {
		return nil
	}
	return b
}

// ── snapshots ---------------------------------------------------------------

type change struct {
	status string // A | M | D
	path   string // slash-separated, project-relative
}

// marshalManifest/parseManifest persist the begin-of-turn file list as
// "sha␠path" lines inside a blob.
func marshalManifest(m map[string]string) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(m[k])
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func parseManifest(data []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		i := strings.IndexByte(line, ' ')
		if i != 64 {
			continue
		}
		out[line[i+1:]] = line[:i]
	}
	return out
}

// ── project walking ---------------------------------------------------------

// walkTextFiles returns every text file under the project root (slash paths),
// skipping VCS/build/IDE metadata directories and hidden directories.
func (r *Recorder) walkTextFiles() []string {
	var out []string
	_ = filepath.WalkDir(r.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if path == r.root {
			return nil
		}
		if d.IsDir() {
			if skipDirs[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > maxTextFile {
			return nil
		}
		rel, err := filepath.Rel(r.root, path)
		if err != nil {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out
}

// ── git ---------------------------------------------------------------------

func gitOutput(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (r *Recorder) isGitRepo() bool {
	out, err := gitOutput(r.root, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// stashTree commits the current worktree state into an object WITHOUT touching
// the worktree or the index, and returns its tree. With no local changes the
// command prints nothing and EXITS 1 — that is not a failure, the answer is
// simply HEAD's tree. (A turn where the AI changed nothing, or changed and
// changed back, lands here.)
func (r *Recorder) stashTree() (string, error) {
	out, _ := gitOutput(r.root, "stash", "create")
	if h := strings.TrimSpace(out); h != "" {
		return h, nil
	}
	out, err := gitOutput(r.root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r *Recorder) catBlob(tree, path string) ([]byte, error) {
	cmd := exec.Command("git", "cat-file", "blob", tree+":"+path)
	cmd.Dir = r.root
	return cmd.Output()
}

// diffNameStatus lists tracked changes between two trees (A/M/D, renames
// expanded into D+A). -z keeps paths with spaces intact.
func (r *Recorder) diffNameStatus(from, to string) ([]change, error) {
	out, err := gitOutput(r.root, "diff", "--name-status", "-z", from, to)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	var changes []change
	for i := 0; i+1 < len(fields); i += 2 {
		code := strings.TrimSpace(fields[i])
		if code == "" {
			continue
		}
		if strings.HasPrefix(code, "R") || strings.HasPrefix(code, "C") {
			if i+2 < len(fields) {
				changes = append(changes,
					change{status: "D", path: fields[i+1]},
					change{status: "A", path: fields[i+2]})
				i += 2
			}
			continue
		}
		changes = append(changes, change{status: code[:1], path: fields[i+1]})
	}
	return changes, nil
}

// untrackedFiles lists files git does not track (slash paths), with our own
// snapshot directory filtered out.
func (r *Recorder) untrackedFiles() ([]string, error) {
	out, err := gitOutput(r.root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, ".tiancan/") {
			continue
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// trackedFiles returns the set of paths git tracks (slash paths).
func (r *Recorder) trackedFiles() (map[string]bool, error) {
	out, err := gitOutput(r.root, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, p := range strings.Split(out, "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			set[p] = true
		}
	}
	return set, nil
}

// ── helpers -----------------------------------------------------------------

func isText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	n := len(b)
	if n > 512 {
		n = 512
	}
	for _, c := range b[:n] {
		if c == 0 {
			return false
		}
	}
	return true
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}
