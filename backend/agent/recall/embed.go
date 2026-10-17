// Package recall implements semantic recall over a session's history — the P2
// layer of the intelligent-memory roadmap.
//
// The goal: when the agent needs old context, it must be able to "翻回去快速找到"
// the relevant slice instead of carrying the whole history in the prompt. That
// needs real vector similarity, not substring matching.
//
// Two embedders are provided, and they are interchangeable:
//
//  1. HashingEmbedder — a LOCAL, zero-dependency, deterministic embedder. It
//     builds a sub-linear-TF × IDF weighted bag of (CJK character bigrams +
//     latin words + latin prefixes), hashed into a fixed-dimensional sparse
//     vector. It is not a neural model, but with corpus IDF it behaves like a
//     solid sparse semantic retriever ("会话/对话/context" land near each other
//     because they share weighted n-grams), and it works offline, instantly and
//     with no API key — which is what makes recall usable by default.
//
//  2. RemoteEmbedder — a real neural embedding model behind any OpenAI-compatible
//     /v1/embeddings endpoint. When configured (base URL + model + key) it is
//     used instead, and the local embedder stays as the automatic fallback so a
//     network outage degrades recall quality instead of breaking it.
//
// Retrieval itself is HYBRID: vector cosine + BM25 keyword score, fused and then
// de-duplicated with MMR. Pure vector search misses exact identifiers (a symbol
// name, an error string); pure keyword search misses paraphrases. The blend is
// what makes recall actually useful on a mixed Chinese/English codebase corpus.
package recall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// DefaultDim is the hashing space of the local embedder. Wide enough that
// collisions are tolerable for corpora of a few tens of thousands of slices.
const DefaultDim = 4096

// Vec is a sparse vector: only non-zero buckets are stored. For a typical chat
// message that is a few dozen entries instead of Dim floats, which keeps both
// memory and the on-disk index small.
type Vec struct {
	Idx []int32   `json:"i"`
	Val []float32 `json:"v"`
}

// Norm2 returns the L2 norm of the vector.
func (v Vec) Norm2() float64 {
	var s float64
	for _, x := range v.Val {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

// Cosine returns the cosine similarity of two sparse vectors.
func Cosine(a, b Vec) float64 {
	na, nb := a.Norm2(), b.Norm2()
	if na == 0 || nb == 0 {
		return 0
	}
	i, j := 0, 0
	var dot float64
	for i < len(a.Idx) && j < len(b.Idx) {
		switch {
		case a.Idx[i] == b.Idx[j]:
			dot += float64(a.Val[i]) * float64(b.Val[j])
			i++
			j++
		case a.Idx[i] < b.Idx[j]:
			i++
		default:
			j++
		}
	}
	return dot / (na * nb)
}

// Embedder turns text into vectors.
type Embedder interface {
	// Dim is the hashing/dimensionality space (used for stats and sanity checks).
	Dim() int
	// Embed vectorises a batch of texts. The returned slice is parallel to input.
	Embed(texts []string) ([]Vec, error)
}

// Trainer is an optional interface for embedders that improve with a corpus
// (the local embedder learns IDF weights from the documents it sees).
type Trainer interface {
	Observe(docs []string)
}

// --- tokenisation -----------------------------------------------------------

type token struct {
	text string
	w    float32
}

// tokenize splits mixed CJK/latin text into weighted n-gram tokens.
//
//   - CJK: character unigram (low weight) + character bigram (high weight). The
//     bigram is what carries meaning in Chinese ("会话" is a token, "会" alone is
//     noise), and the unigram keeps recall alive for short queries.
//   - Latin: whole word + a length-6 prefix, so "configure"/"configuration" and
//     "ContextWindow"/"context" still collide usefully.
func tokenize(s string) []token {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return nil
	}
	var toks []token
	var word strings.Builder
	flush := func() {
		if word.Len() == 0 {
			return
		}
		w := word.String()
		word.Reset()
		if len(w) < 2 && !isDigit(rune(w[0])) {
			return
		}
		toks = append(toks, token{text: "w:" + w, w: 1.0})
		toks = append(toks, conceptTokens(w)...)
		if len(w) > 6 {
			toks = append(toks, token{text: "p:" + w[:6], w: 0.5})
		}
	}
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case isCJK(r):
			flush()
			toks = append(toks, token{text: "u:" + string(r), w: 0.35})
			if i+1 < len(runes) && isCJK(runes[i+1]) {
				bigram := string(r) + string(runes[i+1])
				toks = append(toks, token{text: "b:" + bigram, w: 1.0})
				// 概念归一：让"持久化/保存/落盘"落到同一个概念上，
				// 否则同义改写之间余弦相似度直接为 0，语义召回名存实亡。
				toks = append(toks, conceptTokens(bigram)...)
			}
		case isWordRune(r):
			word.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return toks
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func hashBucket(s string, dim int) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int32(h.Sum32() % uint32(dim))
}

// --- local hashing embedder -------------------------------------------------

// HashingEmbedder is the offline, zero-dependency embedder described in the
// package doc. It learns IDF from observed documents (Observe) and applies
// sub-linear TF scaling, then L2-normalises, so Cosine is a proper TF-IDF cosine.
type HashingEmbedder struct {
	dim  int
	mu   sync.RWMutex
	df   map[string]int
	nDoc int
}

// NewHashingEmbedder returns a local embedder with DefaultDim buckets.
func NewHashingEmbedder() *HashingEmbedder {
	return &HashingEmbedder{dim: DefaultDim, df: map[string]int{}}
}

// NewHashingEmbedderDim returns a local embedder with a custom bucket count.
func NewHashingEmbedderDim(dim int) *HashingEmbedder {
	if dim <= 0 {
		dim = DefaultDim
	}
	return &HashingEmbedder{dim: dim, df: map[string]int{}}
}

func (e *HashingEmbedder) Dim() int { return e.dim }

// Observe records documents so IDF can be computed later. Cheap: it only counts
// distinct tokens per document.
func (e *HashingEmbedder) Observe(docs []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, d := range docs {
		if strings.TrimSpace(d) == "" {
			continue
		}
		e.nDoc++
		seen := map[string]bool{}
		for _, t := range tokenize(d) {
			if !seen[t.text] {
				seen[t.text] = true
				e.df[t.text]++
			}
		}
	}
}

func (e *HashingEmbedder) idf(tok string) float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	df := e.df[tok]
	n := e.nDoc
	if n == 0 || df == 0 {
		return 1.0
	}
	// Smoothed inverse document frequency.
	return math.Log(1+float64(n)/(1+float64(df))) + 0.15
}

// Embed vectorises the texts.
func (e *HashingEmbedder) Embed(texts []string) ([]Vec, error) {
	out := make([]Vec, len(texts))
	for i, t := range texts {
		out[i] = e.embedOne(t)
	}
	return out, nil
}

func (e *HashingEmbedder) embedOne(text string) Vec {
	toks := tokenize(text)
	if len(toks) == 0 {
		return Vec{}
	}
	// Accumulate TF per token, then weight, then fold into hashed buckets.
	tf := make(map[string]float64, len(toks))
	for _, t := range toks {
		tf[t.text] += float64(t.w)
	}
	acc := make(map[int32]float64, len(tf))
	for tok, count := range tf {
		w := (1 + math.Log(count)) * e.idf(tok)
		b := hashBucket(tok, e.dim)
		acc[b] += w
	}
	idx := make([]int32, 0, len(acc))
	for b := range acc {
		idx = append(idx, b)
	}
	sort.Slice(idx, func(i, j int) bool { return idx[i] < idx[j] })
	vec := Vec{Idx: idx, Val: make([]float32, len(idx))}
	for k, b := range idx {
		vec.Val[k] = float32(acc[b])
	}
	return vec
}

// --- remote (OpenAI-compatible) embedder ------------------------------------

// RemoteConfig configures a neural embedding endpoint.
type RemoteConfig struct {
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	APIKey  string `json:"apiKey"`
	Dim     int    `json:"dim,omitempty"`
	Timeout int    `json:"timeoutMs,omitempty"` // 0 → 8000
}

// Valid reports whether enough configuration is present to try a remote call.
func (c RemoteConfig) Valid() bool {
	return strings.TrimSpace(c.BaseURL) != "" && strings.TrimSpace(c.Model) != ""
}

// RemoteEmbedder calls an OpenAI-compatible /v1/embeddings endpoint.
type RemoteEmbedder struct {
	cfg    RemoteConfig
	client *http.Client
	dim    int
}

// NewRemoteEmbedder builds a remote embedder. It does not validate connectivity;
// callers should treat the first Embed error as "unavailable" and fall back.
func NewRemoteEmbedder(cfg RemoteConfig) *RemoteEmbedder {
	to := cfg.Timeout
	if to <= 0 {
		to = 8000
	}
	dim := cfg.Dim
	if dim <= 0 {
		dim = DefaultDim
	}
	return &RemoteEmbedder{cfg: cfg, dim: dim, client: &http.Client{Timeout: time.Duration(to) * time.Millisecond}}
}

func (e *RemoteEmbedder) Dim() int { return e.dim }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Embed vectorises a batch. Errors are returned verbatim so the caller can log
// and fall back.
func (e *RemoteEmbedder) Embed(texts []string) ([]Vec, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: e.cfg.Model, Input: texts})
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(e.cfg.BaseURL, "/") + "/embeddings"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("recall: embeddings http %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out embedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("recall: decode embeddings: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, fmt.Errorf("recall: embeddings error: %s", out.Error.Message)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("recall: empty embeddings response")
	}
	vecs := make([]Vec, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			continue
		}
		vecs[d.Index] = denseToVec(d.Embedding)
	}
	if len(out.Data) > 0 && len(out.Data[0].Embedding) > 0 {
		e.dim = len(out.Data[0].Embedding)
	}
	return vecs, nil
}

// denseToVec drops near-zero components. Neural embeddings are dense, so the
// sparse representation saves nothing — but keeping one vector type keeps the
// index, the maths and the on-disk format uniform.
func denseToVec(d []float64) Vec {
	idx := make([]int32, 0, len(d))
	val := make([]float32, 0, len(d))
	for i, x := range d {
		if math.Abs(x) > 1e-8 {
			idx = append(idx, int32(i))
			val = append(val, float32(x))
		}
	}
	return Vec{Idx: idx, Val: val}
}

// --- composite embedder with fallback ---------------------------------------

// FallbackEmbedder prefers a primary (remote, neural) embedder and silently falls
// back to the local hashing embedder when the primary is unavailable. Recall then
// degrades in quality rather than failing — important, because the primary may be
// an endpoint that is offline or unconfigured on a fresh install.
type FallbackEmbedder struct {
	primary  Embedder
	fallback Embedder
	mu       sync.Mutex
	broken   bool
	lastErr  string
}

// NewFallbackEmbedder wraps primary with fallback.
func NewFallbackEmbedder(primary, fallback Embedder) *FallbackEmbedder {
	if fallback == nil {
		fallback = NewHashingEmbedder()
	}
	return &FallbackEmbedder{primary: primary, fallback: fallback}
}

func (e *FallbackEmbedder) Dim() int {
	if e.primary != nil {
		return e.primary.Dim()
	}
	return e.fallback.Dim()
}

// Embed tries the primary first; on error it marks it broken (so later calls skip
// the doomed request) and uses the fallback.
func (e *FallbackEmbedder) Embed(texts []string) ([]Vec, error) {
	e.mu.Lock()
	broken := e.broken
	primary := e.primary
	e.mu.Unlock()
	if primary != nil && !broken {
		vecs, err := primary.Embed(texts)
		if err == nil {
			return vecs, nil
		}
		e.mu.Lock()
		e.broken = true
		e.lastErr = err.Error()
		e.mu.Unlock()
	}
	return e.fallback.Embed(texts)
}

// Degraded reports whether the fallback is currently in use.
func (e *FallbackEmbedder) Degraded() (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.broken, e.lastErr
}

// Observe trains the underlying trainer(s) so IDF stays fresh.
func (e *FallbackEmbedder) Observe(docs []string) {
	if t, ok := e.fallback.(Trainer); ok {
		t.Observe(docs)
	}
}

// --- BM25 keyword scoring ---------------------------------------------------

// bm25Scores ranks docs against query terms. It is the lexical half of the hybrid
// retriever: exact identifier / error-string matches that a vector can blur away.
func bm25Scores(query string, docs []string) []float64 {
	scores := make([]float64, len(docs))
	terms := queryTerms(query)
	if len(terms) == 0 {
		return scores
	}
	k1, b := 1.5, 0.75
	avgdl := 0.0
	tf := make([]map[string]int, len(docs))
	for i, d := range docs {
		tf[i] = map[string]int{}
		for _, t := range tokenize(d) {
			tf[i][t.text]++
		}
		avgdl += float64(len(tokenize(d)))
	}
	if len(docs) > 0 {
		avgdl /= float64(len(docs))
	}
	if avgdl == 0 {
		avgdl = 1
	}
	df := map[string]int{}
	for i := range docs {
		for t := range tf[i] {
			if _, seen := df[t]; !seen {
				df[t]++
			}
		}
	}
	for _, term := range terms {
		idf := math.Log(1 + (float64(len(docs))-float64(df[term])+0.5)/(float64(df[term])+0.5+1e-9))
		for i := range docs {
			f := float64(tf[i][term])
			if f == 0 {
				continue
			}
			dl := float64(len(tokenize(docs[i])))
			scores[i] += idf * (f * (k1 + 1)) / (f + k1*(1-b+b*dl/avgdl))
		}
	}
	// 压缩到 [0,1) 以便与余弦融合。
	//
	// 注意：这里刻意不用"除以本次最大值"的归一 —— 那种做法会让任何查询都必然
	// 产生一个 100% 命中项，即使语料里根本没有相关内容（实测：问"会话消息怎么
	// 保存到磁盘"，在一个只谈 README 的会话里也会给出 42% 的假命中）。用饱和
	// 函数做绝对压缩，弱匹配就保持弱，检索才敢说"没有相关历史"。
	for i, s := range scores {
		if s > 0 {
			scores[i] = s / (s + 1.0)
		}
	}
	return scores
}

func queryTerms(query string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tokenize(query) {
		if t.w >= 1.0 && !seen[t.text] { // keep strong tokens only (bigrams/words)
			seen[t.text] = true
			out = append(out, t.text)
		}
	}
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// shuffled is used by MMR tie-breaking so equal-scoring items do not always come
// back in insertion order.
func shuffled(n int, seed int64) []int {
	r := rand.New(rand.NewSource(seed))
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	r.Shuffle(n, func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}
