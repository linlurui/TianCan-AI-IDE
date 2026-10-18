package memdb

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Hit is one retrieval result.
type Hit struct {
	Seq     int64
	Score   float32
	Content string
}

// ftsQuery builds an FTS5 MATCH expression from free text. Each bigram/latin-word
// token becomes a phrase; tokens are OR-ed so a Chinese sub-phrase still recalls
// the message (AND would require every token present and over-restrict).
func ftsQuery(text string) string {
	toks := strings.Fields(tokenize(text))
	if len(toks) == 0 {
		return ""
	}
	for i, t := range toks {
		toks[i] = `"` + t + `"`
	}
	return strings.Join(toks, " OR ")
}

// Search performs two-phase retrieval over one session:
//  1. FTS5 inverted index recalls the top `candidates` messages by lexical match
//     (sub-linear, uses the fts index).
//  2. If qvec is non-empty, only those candidates' vectors are loaded and
//     reranked by cosine similarity. This avoids a full O(n) vector scan over
//     months of history.
//
// k is the number of final hits returned.
func (d *DB) Search(sessionID, query string, k, candidates int, qvec []float32) ([]Hit, error) {
	mq := ftsQuery(query)
	if mq == "" {
		return nil, nil
	}
	rows, err := d.sql.Query(
		`SELECT seq FROM messages_fts WHERE session_id=? AND messages_fts MATCH ? ORDER BY rank LIMIT ?`,
		sessionID, mq, candidates)
	if err != nil {
		return nil, fmt.Errorf("memdb: fts search: %w", err)
	}
	var seqs []int64
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			_ = rows.Close()
			return nil, err
		}
		seqs = append(seqs, seq)
	}
	_ = rows.Close()
	if len(seqs) == 0 {
		return nil, nil
	}

	// Fetch content + (optional) vectors for the candidates only.
	placeholders := strings.Repeat("?,", len(seqs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]interface{}, 0, len(seqs)+1)
	args = append(args, sessionID)
	for _, s := range seqs {
		args = append(args, s)
	}
	q := fmt.Sprintf(`SELECT seq, content, COALESCE((SELECT vec FROM vectors v WHERE v.session_id=m.session_id AND v.seq=m.seq), '') FROM messages m WHERE m.session_id=? AND m.seq IN (%s)`, placeholders)
	crows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("memdb: fetch candidates: %w", err)
	}
	type cand struct {
		seq     int64
		content string
		vec     []float32
	}
	var cands []cand
	for crows.Next() {
		var c cand
		var vecBlob []byte
		if err := crows.Scan(&c.seq, &c.content, &vecBlob); err != nil {
			_ = crows.Close()
			return nil, err
		}
		if len(vecBlob) > 0 {
			c.vec = decodeVec(vecBlob)
		}
		cands = append(cands, c)
	}
	_ = crows.Close()

	useVec := len(qvec) > 0
	hits := make([]Hit, 0, len(cands))
	for _, c := range cands {
		var score float32
		if useVec && len(c.vec) == len(qvec) {
			score = cosine(qvec, c.vec)
		} else if useVec {
			score = 0 // dimension mismatch -> drop from vector rerank
		} else {
			score = 1 // lexical-only: equal weight, caller sorts by seq
		}
		hits = append(hits, Hit{Seq: c.seq, Score: score, Content: c.content})
	}
	if useVec {
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	}
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// StoreVector persists a message's embedding for later reranking.
func (d *DB) StoreVector(sessionID string, seq int64, vec []float32) error {
	if len(vec) == 0 {
		return nil
	}
	_, err := d.sql.Exec(
		`INSERT INTO vectors(session_id,seq,dim,vec) VALUES(?,?,?,?) ON CONFLICT(session_id,seq) DO UPDATE SET dim=excluded.dim, vec=excluded.vec`,
		sessionID, seq, len(vec), encodeVec(vec))
	return err
}

// cosine returns the cosine similarity of two equal-length vectors.
func cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (float32(math.Sqrt(float64(na))) * float32(math.Sqrt(float64(nb))))
}
