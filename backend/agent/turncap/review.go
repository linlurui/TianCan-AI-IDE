package turncap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/memdb"
)

// FileVersion is one changed file of one turn, as the review panel lists it.
type FileVersion struct {
	Path      string `json:"path"`
	Status    string `json:"status"` // A 新增 | M 修改 | D 删除
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
	UpdatedAt int64  `json:"updatedAt"`
}

// HunkView is one changed region plus whether it can still be applied to the
// current working copy.
type HunkView struct {
	Hunk
	Superseded bool   `json:"superseded"` // 被后续修改占用 / 定位失败 → 不允许应用
	Reason     string `json:"reason"`     // 为什么不能应用（展示给用户）
	Applied    bool   `json:"applied"`    // 已经应用过
}

// FileDetail is the full review payload of one file in one turn.
type FileDetail struct {
	Path          string     `json:"path"`
	Status        string     `json:"status"`
	Added         int        `json:"added"`
	Removed       int        `json:"removed"`
	BeforeContent string     `json:"beforeContent"`
	AfterContent  string     `json:"afterContent"`
	Hunks         []HunkView `json:"hunks"`
}

// span is a half-open line range [Start, End).
type span struct {
	Start int
	End   int
}

func (s span) len() int { return s.End - s.Start }

// Files lists the files one turn changed, capturing the turn first (it may
// still be open — reviewing mid-turn shows the current state).
func (r *Recorder) Files(seq int64) ([]FileVersion, error) {
	if err := r.Capture(seq); err != nil {
		return nil, err
	}
	rows, err := r.db.ListAnchorFiles(r.hash, r.id, seq)
	if err != nil {
		return nil, err
	}
	out := make([]FileVersion, 0, len(rows))
	for _, f := range rows {
		out = append(out, FileVersion{
			Path: f.Path, Status: f.Status,
			Added: f.Added, Removed: f.Removed, UpdatedAt: f.UpdatedAt,
		})
	}
	return out, nil
}

// Detail returns one file's full review payload: both versions, every changed
// region, and for each region whether it can still be applied.
func (r *Recorder) Detail(seq int64, path string) (*FileDetail, error) {
	// 先补抓再持锁：capture 自己也要拿锁，嵌套会死锁。
	if err := r.Capture(seq); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.detail(seq, path)
}

func (r *Recorder) detail(seq int64, path string) (*FileDetail, error) {
	chain, err := r.db.AnchorFilesForPath(r.hash, r.id, path)
	if err != nil {
		return nil, err
	}
	i := -1
	for k, c := range chain {
		if c.Seq == seq {
			i = k
			break
		}
	}
	if i < 0 {
		return nil, errors.New("该锚点没有这个文件的改动记录")
	}
	row := chain[i]

	before := r.blobText(row.BeforeSHA)
	after := r.blobText(row.AfterSHA)

	applied := map[int]bool{}
	if logs, err := r.db.ListAnchorApplies(r.hash, r.id, seq); err == nil {
		for _, l := range logs {
			if l.Path == path && l.OK {
				applied[l.Hunk] = true
			}
		}
	}

	var curLines []string
	if s := r.readWorktree(path); s != nil {
		curLines = strings.Split(*s, "\n")
	}
	hunks := Hunks(before, after)
	views := make([]HunkView, 0, len(hunks))
	for _, h := range hunks {
		_, ok, reason := r.resolve(chain, i, h, curLines, applied)
		if !ok && applied[h.Index] {
			// 还原过的片段，工作区里已经是改前内容，坐标核对必然不过——
			// 真实原因说清楚，别让用户以为是文件被谁改坏了。
			reason = "此片段已应用过（工作区已是改前内容）"
		}
		views = append(views, HunkView{
			Hunk:       h,
			Superseded: !ok,
			Reason:     reason,
			Applied:    applied[h.Index],
		})
	}
	return &FileDetail{
		Path:          path,
		Status:        row.Status,
		Added:         row.Added,
		Removed:       row.Removed,
		BeforeContent: before,
		AfterContent:  after,
		Hunks:         views,
	}, nil
}

// ApplyHunk restores one changed region of one turn back into the working copy
// (a partial revert: only the selected lines go back to their old content).
//
// The region is mapped forward through every later turn that touched the same
// file: if a later turn edited the same lines the apply is refused — reverting
// would silently destroy newer work.
func (r *Recorder) ApplyHunk(seq int64, path string, hunkIndex int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	chain, err := r.db.AnchorFilesForPath(r.hash, r.id, path)
	if err != nil {
		return err
	}
	i := -1
	for k, c := range chain {
		if c.Seq == seq {
			i = k
			break
		}
	}
	if i < 0 {
		return errors.New("该锚点没有这个文件的改动记录")
	}
	row := chain[i]
	before := r.blobText(row.BeforeSHA)
	after := r.blobText(row.AfterSHA)

	applied := map[int]bool{}
	if logs, err := r.db.ListAnchorApplies(r.hash, r.id, seq); err == nil {
		for _, l := range logs {
			if l.Path == path && l.OK {
				applied[l.Hunk] = true
			}
		}
	}
	if applied[hunkIndex] {
		return errors.New("此片段已应用过（工作区已是改前内容）")
	}

	var target *Hunk
	hunks := Hunks(before, after)
	if hunkIndex >= 0 && hunkIndex < len(hunks) {
		target = &hunks[hunkIndex]
	}
	if target == nil {
		return errors.New("片段不存在")
	}

	ws := r.readWorktree(path)
	if ws == nil {
		return errors.New("文件不存在于工作区")
	}
	curLines := strings.Split(*ws, "\n")

	region, ok, reason := r.resolve(chain, i, *target, curLines, applied)
	if !ok {
		_ = r.db.RecordAnchorApply(r.hash, r.id, seq, path, hunkIndex, false, reason)
		return errors.New(reason)
	}

	next := make([]string, 0, len(curLines)-region.len()+len(target.Before))
	next = append(next, curLines[:region.Start]...)
	next = append(next, target.Before...)
	next = append(next, curLines[region.End:]...)

	abs := filepath.Join(r.root, filepath.FromSlash(path))
	if err := os.WriteFile(abs, []byte(strings.Join(next, "\n")), 0o644); err != nil {
		_ = r.db.RecordAnchorApply(r.hash, r.id, seq, path, hunkIndex, false, err.Error())
		return err
	}
	_ = r.db.RecordAnchorApply(r.hash, r.id, seq, path, hunkIndex, true, "")
	return nil
}

// TruncateAfter drops the version records of turns after seq (conversation
// rollback). Blob references are released so the blob store does not grow
// without bound; the turn bases go too, otherwise a refresh capture would
// resurrect the rolled-back turns.
func (r *Recorder) TruncateAfter(seq int64) {
	if rows, err := r.db.AnchorFileRowsAfter(r.hash, r.id, seq); err == nil {
		for _, o := range rows {
			_ = r.db.DropBlobRef(o.BeforeSHA)
			_ = r.db.DropBlobRef(o.AfterSHA)
		}
	}
	if bases, err := r.db.TurnBasesAfter(r.hash, r.id, seq); err == nil {
		for _, b := range bases {
			if b.SnapSHA == "" {
				continue
			}
			// manifest blob 本身 + 它引用的每个内容 blob 都只被这个基点持有。
			if data, err := r.db.GetBlob(b.SnapSHA); err == nil {
				for _, sha := range parseManifest(data) {
					_ = r.db.DropBlobRef(sha)
				}
			}
			_ = r.db.DropBlobRef(b.SnapSHA)
		}
	}
	_ = r.db.DeleteAnchorFilesAfter(r.hash, r.id, seq)
	_ = r.db.DeleteAnchorAppliesAfter(r.hash, r.id, seq)
	// 基点也必须删：否则 Files() 里的补抓会把刚撤掉的轮次再抓回来。
	_ = r.db.DeleteTurnBasesAfter(r.hash, r.id, seq)
}

// resolve maps a hunk of turn chain[i] forward through every later turn that
// touched the same file and reports whether it can still be applied.
//
// Coordinate spaces: the hunk's region lives in "this turn's after" space.
// Every later turn's before-content equals the previous chain entry's
// after-content (they are consecutive versions of the same file), so the
// region can be shifted turn by turn until it lands in current-file space.
//
// Two kinds of edits shift the region: later turns of the chain, and hunks of
// THIS turn that were already restored by the user (an applied hunk changes
// the line count of the working copy exactly like a later edit would).
func (r *Recorder) resolve(chain []memdb.AnchorFileRow, i int, h Hunk, cur []string, applied map[int]bool) (span, bool, string) {
	region := span{Start: h.AfterStart, End: h.AfterEnd}

	// 同一轮里已经还原过的片段：它们已经把工作区的行数改掉了，
	// 目标片段在它们之后的部分必须跟着平移，否则会定位到错误的行。
	hunks := Hunks(r.blobText(chain[i].BeforeSHA), r.blobText(chain[i].AfterSHA))
	for _, hb := range hunks {
		if hb.Index == h.Index || !applied[hb.Index] {
			continue
		}
		switch {
		case hb.AfterEnd <= region.Start:
			d := -hb.Delta() // 还原 = 行数变化取反
			region.Start += d
			region.End += d
		case hb.AfterStart >= region.End:
			// 完全在目标片段之后：不动。
		default:
			return span{}, false, "同一轮内片段相互重叠，无法定位"
		}
	}

	prevAfter := chain[i].AfterSHA
	for k := i + 1; k < len(chain); k++ {
		b := chain[k]
		if b.BeforeSHA != prevAfter {
			// 两轮之间文件被别的途径改过（用户手动编辑、未记录的工具），
			// 版本链断了：任何老片段的坐标都不可信，一律不许应用。
			return span{}, false, "版本链断裂：轮次之间文件被其他修改"
		}
		bb := r.blobText(b.BeforeSHA)
		ba := r.blobText(b.AfterSHA)
		for _, hb := range Hunks(bb, ba) {
			switch {
			case hb.BeforeEnd <= region.Start:
				// 完全在目标片段之前：整体下移。
				d := hb.Delta()
				region.Start += d
				region.End += d
			case hb.BeforeStart >= region.End:
				// 完全在目标片段之后：不动。
			default:
				return span{}, false, "后续修改已占用同一片段"
			}
		}
		prevAfter = b.AfterSHA
	}
	if region.Start < 0 || region.End > len(cur) || region.Start > region.End {
		return span{}, false, "文件已被其他修改，片段定位失败"
	}
	// 与工作区现状核对：这一段必须还是这一轮改完的样子。
	want := h.After
	if region.len() != len(want) {
		return span{}, false, "文件已被其他修改，片段定位失败"
	}
	for k := range want {
		if cur[region.Start+k] != want[k] {
			return span{}, false, "文件已被其他修改，片段定位失败"
		}
	}
	return region, true, ""
}

// readWorktree reads the current content of a project file (nil when absent).
func (r *Recorder) readWorktree(path string) *string {
	b, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(path)))
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

func (r *Recorder) blobText(sha string) string {
	if sha == "" {
		return ""
	}
	b, err := r.db.GetBlob(sha)
	if err != nil {
		return ""
	}
	return string(b)
}
