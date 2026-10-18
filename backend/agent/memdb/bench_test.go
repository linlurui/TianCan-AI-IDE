package memdb

import (
	"fmt"
	"testing"
)

// hashVec produces a deterministic 32-dim embedding from text so the benchmark has
// vectors without a real model. It biases the first dims by a phrase hash so that
// messages sharing vocabulary score higher — enough to exercise reranking.
func hashVec(text string) []float32 {
	const dim = 32
	v := make([]float32, dim)
	for i := 0; i < len(text); i++ {
		c := text[i]
		v[int(c)%dim] += 1
		v[(int(c)+7)%dim] += 0.5
	}
	return v
}

func seedLarge(b *testing.B, db *DB, n int) []float32 {
	b.Helper()
	phrases := []string{"推送代码到远程", "持久化会话到磁盘", "语义召回功能", "前端组件状态", "后端接口 gin", "单元测试覆盖", "不要修改登录逻辑", "压缩历史上下文"}
	for i := 0; i < n; i++ {
		p := phrases[i%len(phrases)]
		content := fmt.Sprintf("任务%d：用户要求%s，需要评估影响范围", i, p)
		seq, err := db.AppendEvent("ph", "bk", "msg", "{}")
		if err != nil {
			b.Fatal(err)
		}
		if err := db.UpsertMessage("bk", seq, "user", content, "seg", 0); err != nil {
			b.Fatal(err)
		}
		if err := db.StoreVector("bk", seq, hashVec(content)); err != nil {
			b.Fatal(err)
		}
	}
	// Query vector ~ "推送代码到远程" message family.
	return hashVec("用户要求推送代码到远程，需要评估影响范围")
}

func BenchmarkTwoPhaseSearch(b *testing.B) {
	db := openTest(b)
	const N = 20000
	q := seedLarge(b, db, N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := db.Search("bk", "推送代码", 10, 200, q); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBruteForceSearch is the baseline: load every vector and scan linearly.
// It shows the cost the two-phase index avoids on a months-long project.
func BenchmarkBruteForceSearch(b *testing.B) {
	db := openTest(b)
	const N = 20000
	q := seedLarge(b, db, N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := db.sql.Query(`SELECT seq, vec FROM vectors WHERE session_id=?`, "bk")
		if err != nil {
			b.Fatal(err)
		}
		best := make([]struct {
			seq   int64
			score float32
		}, 0)
		for rows.Next() {
			var seq int64
			var blob []byte
			if err := rows.Scan(&seq, &blob); err != nil {
				b.Fatal(err)
			}
			s := cosine(q, decodeVec(blob))
			if len(best) < 10 || s > best[len(best)-1].score {
				best = append(best, struct {
					seq   int64
					score float32
				}{seq, s})
			}
			if len(best) > 10 {
				best = best[:10]
			}
		}
		rows.Close()
	}
}
