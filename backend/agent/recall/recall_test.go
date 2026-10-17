package recall

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCosineBasics(t *testing.T) {
	a := Vec{Idx: []int32{1, 2, 3}, Val: []float32{1, 2, 3}}
	b := Vec{Idx: []int32{1, 2, 3}, Val: []float32{1, 2, 3}}
	if got := Cosine(a, b); math.Abs(got-1) > 1e-6 {
		t.Fatalf("identical vectors should be 1, got %f", got)
	}
	c := Vec{Idx: []int32{4, 5}, Val: []float32{1, 1}}
	if got := Cosine(a, c); got != 0 {
		t.Fatalf("disjoint vectors should be 0, got %f", got)
	}
	if got := Cosine(Vec{}, b); got != 0 {
		t.Fatalf("empty vector should be 0, got %f", got)
	}
}

// 语义近似：本地 hashing embedder 必须能把"说法不同但意思接近"的句子排在无关句子前面。
// 这是"效果好不好"的底线测试——如果这条挂了，召回就退化成关键词匹配了。
func TestLocalEmbeddingIsSemanticNotExact(t *testing.T) {
	e := NewHashingEmbedder()
	corpus := []string{
		"把会话消息持久化到后端磁盘上",
		"将聊天历史保存到服务端",
		"对话记录需要落盘存储",
		"今天中午吃什么好呢",
		"编译失败：undefined: foo",
	}
	e.Observe(corpus)
	vecs, err := e.Embed(corpus)
	if err != nil {
		t.Fatal(err)
	}
	q, err := e.Embed([]string{"聊天记录要存到硬盘"})
	if err != nil {
		t.Fatal(err)
	}
	top := 0
	topScore := -1.0
	for i, v := range vecs {
		s := Cosine(q[0], v)
		if s > topScore {
			topScore = s
			top = i
		}
	}
	if top > 2 {
		t.Fatalf("semantic nearest should be one of the storage sentences, got #%d (%q)", top, corpus[top])
	}
	// 反证：无关句子的得分必须明显低于命中项。
	noise := math.Max(Cosine(q[0], vecs[3]), Cosine(q[0], vecs[4]))
	if topScore <= noise {
		t.Fatalf("semantic score %.4f must beat noise %.4f", topScore, noise)
	}
}

func TestLocalEmbedderIsDeterministic(t *testing.T) {
	e1 := NewHashingEmbedder()
	e2 := NewHashingEmbedder()
	e1.Observe([]string{"会话持久化"})
	e2.Observe([]string{"会话持久化"})
	a, _ := e1.Embed([]string{"会话持久化方案"})
	b, _ := e2.Embed([]string{"会话持久化方案"})
	if len(a[0].Idx) != len(b[0].Idx) {
		t.Fatal("non-deterministic tokenisation")
	}
	for i := range a[0].Idx {
		if a[0].Idx[i] != b[0].Idx[i] || a[0].Val[i] != b[0].Val[i] {
			t.Fatal("non-deterministic embedding")
		}
	}
}

// 精确标识符必须靠关键词那半边捞回来：纯向量很容易把一个陌生符号名抹平。
func TestHybridFindsExactIdentifiers(t *testing.T) {
	ix := NewIndex(NewHashingEmbedder(), "")
	docs := []string{
		"我们把 EstimateTokens 函数改成了按字数估算",
		"聊天记录要保存到磁盘上",
		"今天的天气不错",
	}
	for i, d := range docs {
		if err := ix.Add(Item{Seq: int64(i + 1), Text: d}); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := ix.Search("EstimateTokens", 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits at all")
	}
	if hits[0].Seq != 1 {
		t.Fatalf("exact identifier should rank first, got seq=%d", hits[0].Seq)
	}
	if hits[0].KWScore <= 0 {
		t.Fatalf("keyword half contributed nothing: %+v", hits[0])
	}
}

func TestSearchRespectsKindFilter(t *testing.T) {
	ix := NewIndex(NewHashingEmbedder(), "")
	_ = ix.Add(Item{Seq: 1, Kind: "redline", Text: "不要推送代码"})
	_ = ix.Add(Item{Seq: 2, Kind: "msg", Text: "推送代码到远端仓库"})
	hits, err := ix.Search("推送代码", 5, func(it Item) bool { return it.Kind != "redline" })
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Kind == "redline" {
			t.Fatalf("filter ignored: %+v", h)
		}
	}
}

// MMR：召回列表里不应出现几乎重复的两条。
func TestMMRRemovesNearDuplicates(t *testing.T) {
	ix := NewIndex(NewHashingEmbedder(), "")
	_ = ix.Add(Item{Seq: 1, Text: "会话持久化：把消息写入 journal 文件"})
	_ = ix.Add(Item{Seq: 2, Text: "会话持久化：把消息写入 journal 文件（重复一遍）"})
	_ = ix.Add(Item{Seq: 3, Text: "语义召回用 embedding 向量做相似度检索"})
	hits, err := ix.Search("会话持久化与语义召回", 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 {
		t.Fatalf("want at least 2 hits, got %d", len(hits))
	}
	if hits[0].Seq == hits[1].Seq {
		t.Fatal("duplicate seq in results")
	}
}

func TestIndexPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vec.json")
	ix := NewIndex(NewHashingEmbedder(), path)
	for i := 0; i < 3; i++ {
		if err := ix.Add(Item{Seq: int64(i + 1), Text: "文档内容 " + strings.Repeat("字", i+1)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ix.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("index file missing: %v", err)
	}
	ix2 := NewIndex(NewHashingEmbedder(), path)
	if err := ix2.Load(); err != nil {
		t.Fatal(err)
	}
	if ix2.Len() != ix.Len() {
		t.Fatalf("reload lost items: %d vs %d", ix2.Len(), ix.Len())
	}
	hits, err := ix2.Search("文档内容", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("reloaded index cannot search")
	}
}

func TestTruncateAfterSupportsRollback(t *testing.T) {
	ix := NewIndex(NewHashingEmbedder(), "")
	for i := 1; i <= 5; i++ {
		_ = ix.Add(Item{Seq: int64(i), Text: "消息 " + string(rune('0'+i))})
	}
	if drop := ix.TruncateAfter(2); drop != 3 {
		t.Fatalf("want 3 dropped, got %d", drop)
	}
	if ix.Len() != 2 {
		t.Fatalf("want 2 left, got %d", ix.Len())
	}
	// 截断后仍然可以正确检索，bySeq 索引没有错位。
	hits, err := ix.Search("消息", 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Seq > 2 {
			t.Fatalf("truncated item still searchable: seq=%d", h.Seq)
		}
	}
}

func TestRemoveKeepsIndexConsistent(t *testing.T) {
	ix := NewIndex(NewHashingEmbedder(), "")
	for i := 1; i <= 4; i++ {
		_ = ix.Add(Item{Seq: int64(i), Text: "条目 " + string(rune('0'+i))})
	}
	ix.Remove(2)
	if ix.Len() != 3 {
		t.Fatalf("want 3, got %d", ix.Len())
	}
	for _, it := range ix.Items() {
		if it.Seq == 2 {
			t.Fatal("removed item still present")
		}
	}
	// 再插入一个新条目，不能因为索引错位而覆盖已有条目。
	_ = ix.Add(Item{Seq: 5, Text: "全新条目"})
	if ix.Len() != 4 {
		t.Fatalf("want 4 after re-add, got %d", ix.Len())
	}
}

// 远程挂了必须降级而不是失败——新装机器上没有配置 embedding 服务也要能用。
type brokenEmbedder struct{ calls int }

func (b *brokenEmbedder) Dim() int { return 4 }
func (b *brokenEmbedder) Embed(texts []string) ([]Vec, error) {
	b.calls++
	return nil, errBroken
}

var errBroken = &embedError{"boom"}

type embedError struct{ msg string }

func (e *embedError) Error() string { return e.msg }

func TestFallbackDegradesGracefully(t *testing.T) {
	broken := &brokenEmbedder{}
	fb := NewFallbackEmbedder(broken, NewHashingEmbedder())
	if _, err := fb.Embed([]string{"会话持久化"}); err != nil {
		t.Fatalf("fallback should not fail: %v", err)
	}
	// 第二次调用不应再浪费一次注定失败的请求。
	if _, err := fb.Embed([]string{"再来一次"}); err != nil {
		t.Fatal(err)
	}
	if broken.calls != 1 {
		t.Fatalf("broken primary should be called once, got %d", broken.calls)
	}
	if deg, _ := fb.Degraded(); !deg {
		t.Fatal("should report degraded")
	}
}

func TestShortAndEmptyAreNotIndexed(t *testing.T) {
	ix := NewIndex(NewHashingEmbedder(), "")
	if err := ix.Add(Item{Seq: 1, Text: "  "}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Add(Item{Seq: 2, Text: "好"}); err != nil {
		t.Fatal(err)
	}
	if ix.Len() != 0 {
		t.Fatalf("short/empty should be skipped, got %d", ix.Len())
	}
}

func TestBatchAddIsEquivalent(t *testing.T) {
	a := NewIndex(NewHashingEmbedder(), "")
	b := NewIndex(NewHashingEmbedder(), "")
	items := []Item{
		{Seq: 1, Text: "把会话持久化到磁盘"},
		{Seq: 2, Text: "用 embedding 做语义召回"},
		{Seq: 3, Text: "红线必须放在提示词最前面"},
	}
	for _, it := range items {
		_ = a.Add(it)
	}
	if err := b.AddBatch(items); err != nil {
		t.Fatal(err)
	}
	if a.Len() != b.Len() {
		t.Fatalf("batch add lost items: %d vs %d", a.Len(), b.Len())
	}
	hits1, _ := a.Search("语义召回", 1, nil)
	hits2, _ := b.Search("语义召回", 1, nil)
	if len(hits1) == 0 || len(hits2) == 0 {
		t.Fatal("no hits")
	}
	if hits1[0].Seq != hits2[0].Seq {
		t.Fatalf("batch differs from single: %d vs %d", hits1[0].Seq, hits2[0].Seq)
	}
}

func TestBM25RanksExactTermFirst(t *testing.T) {
	docs := []string{
		"journal 文件用于持久化事件日志",
		"今天的天气不错",
	}
	s := bm25Scores("journal", docs)
	if s[0] <= s[1] {
		t.Fatalf("bm25 should rank the matching doc first: %v", s)
	}
	if s[1] != 0 {
		t.Fatalf("non-matching doc should score 0, got %f", s[1])
	}
}
