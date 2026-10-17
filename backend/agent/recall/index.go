package recall

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Item is one indexed slice of session history. Seq ties an item back to the
// journal event that produced it, so a hit can always be traced (and rolled back).
type Item struct {
	Seq     int64  `json:"seq"`
	Kind    string `json:"kind"`    // "msg" | "redline" | "req" | "segment"
	Role    string `json:"role"`    // for msg: user / assistant / tool
	Text    string `json:"text"`    // full text kept for exact expansion
	Preview string `json:"preview"` // short form for UI / prompt
	Vec     Vec    `json:"vec"`
}

// Hit is one retrieval result.
type Hit struct {
	Item     `json:"item"`
	Score    float64 `json:"score"`    // fused score
	VecScore float64 `json:"vecScore"` // cosine part
	KWScore  float64 `json:"kwScore"`  // BM25 part
}

// MinVecScore is the semantic floor for recall: if even the closest item scores
// below this cosine, the history simply does not contain the topic and nothing is
// returned. See Search for why this matters more than it looks.
const MinVecScore = 0.15

// Index is the vector + lexical index over one session's history.
//
// It is intentionally small and dependency-free: documents here are chat messages,
// red lines and segment summaries, so a plain in-memory index with periodic
// snapshotting is faster and simpler than pulling in a vector database.
type Index struct {
	mu     sync.RWMutex
	embed  Embedder
	items  []Item
	bySeq  map[int64]int
	path   string
	dirty  bool
	vecW   float64 // weight of the vector half
	kwW    float64 // weight of the keyword half
	mmrLam float64 // MMR diversity factor
	minLen int     // texts shorter than this are not indexed alone
}

// Options tunes the hybrid retriever.
type Options struct {
	VecWeight float64 // default 0.65
	KWWeight  float64 // default 0.35
	MMRLambda float64 // default 0.7 (relevance vs diversity)
	MinLen    int     // default 4 runes
}

// DefaultOptions returns the tuned defaults.
func DefaultOptions() Options {
	return Options{VecWeight: 0.65, KWWeight: 0.35, MMRLambda: 0.7, MinLen: 4}
}

// NewIndex builds an index. When path != "" the index snapshots itself there on
// Save and can be reloaded with Load.
func NewIndex(embed Embedder, path string) *Index {
	if embed == nil {
		embed = NewHashingEmbedder()
	}
	o := DefaultOptions()
	return &Index{
		embed:  embed,
		bySeq:  map[int64]int{},
		path:   path,
		vecW:   o.VecWeight,
		kwW:    o.KWWeight,
		mmrLam: o.MMRLambda,
		minLen: o.MinLen,
	}
}

// SetOptions overrides retrieval weights.
func (ix *Index) SetOptions(o Options) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if o.VecWeight > 0 {
		ix.vecW = o.VecWeight
	}
	if o.KWWeight > 0 {
		ix.kwW = o.KWWeight
	}
	if o.MMRLambda > 0 {
		ix.mmrLam = o.MMRLambda
	}
	if o.MinLen > 0 {
		ix.minLen = o.MinLen
	}
}

// Len returns the number of indexed items.
func (ix *Index) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.items)
}

// Add indexes one item, replacing any previous item with the same seq.
func (ix *Index) Add(it Item) error {
	it.Text = strings.TrimSpace(it.Text)
	if len([]rune(it.Text)) < ix.minLen {
		return nil
	}
	if it.Preview == "" {
		it.Preview = truncate(it.Text, 120)
	}
	if it.Kind == "" {
		it.Kind = "msg"
	}
	vecs, err := ix.embed.Embed([]string{it.Text})
	if err != nil {
		return fmt.Errorf("recall: embed: %w", err)
	}
	if len(vecs) == 1 {
		it.Vec = vecs[0]
	}
	if t, ok := ix.embed.(Trainer); ok {
		t.Observe([]string{it.Text})
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	if pos, ok := ix.bySeq[it.Seq]; ok {
		ix.items[pos] = it
		ix.dirty = true
		return nil
	}
	ix.bySeq[it.Seq] = len(ix.items)
	ix.items = append(ix.items, it)
	ix.dirty = true
	return nil
}

// AddBatch indexes many items with a single embedding call.
func (ix *Index) AddBatch(items []Item) error {
	if len(items) == 0 {
		return nil
	}
	keep := make([]Item, 0, len(items))
	texts := make([]string, 0, len(items))
	for _, it := range items {
		it.Text = strings.TrimSpace(it.Text)
		if len([]rune(it.Text)) < ix.minLen {
			continue
		}
		if it.Preview == "" {
			it.Preview = truncate(it.Text, 120)
		}
		if it.Kind == "" {
			it.Kind = "msg"
		}
		keep = append(keep, it)
		texts = append(texts, it.Text)
	}
	if len(keep) == 0 {
		return nil
	}
	// Batch in chunks: remote endpoints cap input size.
	const chunk = 32
	for start := 0; start < len(keep); start += chunk {
		end := start + chunk
		if end > len(keep) {
			end = len(keep)
		}
		vecs, err := ix.embed.Embed(texts[start:end])
		if err != nil {
			return fmt.Errorf("recall: embed batch: %w", err)
		}
		if t, ok := ix.embed.(Trainer); ok {
			t.Observe(texts[start:end])
		}
		ix.mu.Lock()
		for i, it := range keep[start:end] {
			if i < len(vecs) {
				it.Vec = vecs[i]
			}
			if pos, ok := ix.bySeq[it.Seq]; ok {
				ix.items[pos] = it
				continue
			}
			ix.bySeq[it.Seq] = len(ix.items)
			ix.items = append(ix.items, it)
		}
		ix.dirty = true
		ix.mu.Unlock()
	}
	return nil
}

// Remove drops an item by seq (used when a rollback truncates history).
func (ix *Index) Remove(seq int64) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	pos, ok := ix.bySeq[seq]
	if !ok {
		return
	}
	ix.items = append(ix.items[:pos], ix.items[pos+1:]...)
	delete(ix.bySeq, seq)
	for i := pos; i < len(ix.items); i++ {
		ix.bySeq[ix.items[i].Seq] = i
	}
	ix.dirty = true
}

// TruncateAfter drops every item with seq > max (rollback support).
func (ix *Index) TruncateAfter(max int64) int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	kept := ix.items[:0]
	drop := 0
	for _, it := range ix.items {
		if it.Seq > max {
			drop++
			delete(ix.bySeq, it.Seq)
			continue
		}
		kept = append(kept, it)
	}
	ix.items = kept
	ix.bySeq = map[int64]int{}
	for i, it := range ix.items {
		ix.bySeq[it.Seq] = i
	}
	if drop > 0 {
		ix.dirty = true
	}
	return drop
}

// Search runs hybrid retrieval: cosine (vector) + BM25 (lexical), fused, then
// diversified with MMR so the top-k are not five paraphrases of the same message.
//
// filter, when non-nil, can exclude items by kind (e.g. skip red lines, which are
// always injected anyway and would otherwise eat the whole budget).
func (ix *Index) Search(query string, k int, filter func(Item) bool) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if query == "" || k <= 0 {
		return nil, nil
	}
	ix.mu.RLock()
	items := make([]Item, len(ix.items))
	copy(items, ix.items)
	vecW, kwW, lam := ix.vecW, ix.kwW, ix.mmrLam
	ix.mu.RUnlock()

	if filter != nil {
		kept := items[:0]
		for _, it := range items {
			if filter(it) {
				kept = append(kept, it)
			}
		}
		items = kept
	}
	if len(items) == 0 {
		return nil, nil
	}

	qvecs, err := ix.embed.Embed([]string{query})
	if err != nil {
		return nil, fmt.Errorf("recall: embed query: %w", err)
	}
	var qvec Vec
	if len(qvecs) == 1 {
		qvec = qvecs[0]
	}

	docs := make([]string, len(items))
	for i, it := range items {
		docs[i] = it.Text
	}
	kw := bm25Scores(query, docs)

	type cand struct {
		hit Hit
		vec Vec
	}
	cands := make([]cand, 0, len(items))
	maxVec := 0.0
	for i, it := range items {
		vs := Cosine(qvec, it.Vec)
		score := vecW*vs + kwW*kw[i]
		if score <= 0.001 {
			continue
		}
		if vs > maxVec {
			maxVec = vs
		}
		cands = append(cands, cand{hit: Hit{Item: it, Score: score, VecScore: vs, KWScore: kw[i]}, vec: it.Vec})
	}
	if len(cands) == 0 {
		return nil, nil
	}
	// 语义门槛：这是"语义召回"，不是关键词搜索。若连最像的一条在向量空间上都
	// 几乎不相关（余弦 < MinVecScore），说明历史里确实没有这块内容 —— 此时返回空，
	// 由调用方决定不注入召回块。宁可空，也不要把"只因为共用了'什么''做'这类
	// 虚词而字面重合"的旧消息塞进提示词（实测那样会给出 30%~45% 的假命中）。
	if maxVec < MinVecScore {
		return nil, nil
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].hit.Score > cands[j].hit.Score })

	// MMR: greedily pick the best marginal (relevance − λ·max similarity to
	// already-picked) so the result set covers distinct aspects of the query.
	var picked []Hit
	remaining := make([]cand, len(cands))
	copy(remaining, cands)
	for len(picked) < k && len(remaining) > 0 {
		best := 0
		bestVal := -math.MaxFloat64
		for i := range remaining {
			val := lam * remaining[i].hit.Score
			for _, p := range picked {
				sim := Cosine(remaining[i].vec, p.Vec)
				if sim > 0 {
					val -= (1 - lam) * sim
				}
			}
			if val > bestVal {
				bestVal = val
				best = i
			}
		}
		picked = append(picked, remaining[best].hit)
		remaining = append(remaining[:best], remaining[best+1:]...)
	}
	return picked, nil
}

// --- persistence ------------------------------------------------------------

// Save snapshots the index to its path (no-op when no path was configured or
// nothing changed since the last save).
func (ix *Index) Save() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.path == "" || !ix.dirty {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(ix.path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(ix.items)
	if err != nil {
		return err
	}
	tmp := ix.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, ix.path); err != nil {
		return err
	}
	ix.dirty = false
	return nil
}

// Load restores an index snapshot from disk, replacing the in-memory items.
// Vectors are kept verbatim, so a snapshot written with the remote embedder stays
// valid (and stays comparable) after a restart.
func (ix *Index) Load() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.path == "" {
		return nil
	}
	data, err := os.ReadFile(ix.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var items []Item
	if err := json.Unmarshal(data, &items); err != nil {
		return fmt.Errorf("recall: decode index: %w", err)
	}
	ix.items = items
	ix.bySeq = make(map[int64]int, len(items))
	for i, it := range items {
		ix.bySeq[it.Seq] = i
	}
	if t, ok := ix.embed.(Trainer); ok {
		texts := make([]string, len(items))
		for i, it := range items {
			texts[i] = it.Text
		}
		t.Observe(texts)
	}
	ix.dirty = false
	return nil
}

// Items returns a copy of every indexed item (used by the UI and by tests).
func (ix *Index) Items() []Item {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]Item, len(ix.items))
	copy(out, ix.items)
	return out
}

// Stats describes the index for diagnostics.
type Stats struct {
	Items    int    `json:"items"`
	Dim      int    `json:"dim"`
	Path     string `json:"path,omitempty"`
	Degraded bool   `json:"degraded,omitempty"`
	LastErr  string `json:"lastErr,omitempty"`
}

// Stats reports index diagnostics.
func (ix *Index) Stats() Stats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	s := Stats{Items: len(ix.items), Dim: ix.embed.Dim(), Path: ix.path}
	if f, ok := ix.embed.(*FallbackEmbedder); ok {
		if deg, err := f.Degraded(); deg {
			s.Degraded = true
			s.LastErr = err
		}
	}
	return s
}
