// Package ctxsvc wires the three memory layers together and exposes them to the
// frontend as one Wails service:
//
//	conversation (P0 持久化)  +  recall (P2 语义召回)  +  prompt (提示词装配)
//
// It owns the per-session wiring that the leaf packages deliberately do not:
//   - every appended message is also indexed for semantic recall;
//   - every USER message is scanned for red lines / requirement points (P1);
//   - rollback truncates both the projection and the vector index;
//   - a saved index is reloaded on reopen, so recall survives a restart.
package ctxsvc

import (
	"sync"
	"time"

	"github.com/linlurui/tiancan-ai-ide/backend/agent/conversation"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/prompt"
	"github.com/linlurui/tiancan-ai-ide/backend/agent/recall"
)

// saveEvery is how often the vector index is flushed to disk (per appends).
const saveEvery = 8

// Service is the Wails-bound context service.
type Service struct {
	mu        sync.Mutex
	store     *conversation.Store
	indices   map[string]*recall.Index
	embed     recall.Embedder
	cfg       recall.RemoteConfig
	saveCount map[string]int
}

// NewService builds the service with the local (offline) embedder. Call
// SetEmbedding later to upgrade to a neural embedding endpoint.
func NewService() *Service {
	return &Service{
		store:     conversation.DefaultStore(),
		indices:   map[string]*recall.Index{},
		embed:     recall.NewHashingEmbedder(),
		saveCount: map[string]int{},
	}
}

// SetEmbedding upgrades the embedder to a neural endpoint. An invalid config
// (no base URL / model) is ignored: the service keeps working offline.
func (s *Service) SetEmbedding(cfg recall.RemoteConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	if !cfg.Valid() {
		s.embed = recall.NewHashingEmbedder()
		return
	}
	// 远程为主、本地兜底：断网/未配置时召回降级但不断供。
	s.embed = recall.NewFallbackEmbedder(recall.NewRemoteEmbedder(cfg), recall.NewHashingEmbedder())
}

// EmbeddingConfig returns the current embedding configuration (key redacted).
func (s *Service) EmbeddingConfig() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := ""
	if s.cfg.APIKey != "" {
		key = "***"
	}
	degraded := false
	if f, ok := s.embed.(*recall.FallbackEmbedder); ok {
		if d, _ := f.Degraded(); d {
			degraded = true
		}
	}
	return map[string]interface{}{
		"baseUrl":  s.cfg.BaseURL,
		"model":    s.cfg.Model,
		"apiKey":   key,
		"remote":   s.cfg.Valid(),
		"degraded": degraded,
	}
}

// --- session + index pairing ------------------------------------------------

func (s *Service) pair(projectRoot, sessionID string) (*conversation.Session, *recall.Index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID == "" {
		sessionID = conversation.MainSessionID
	}
	key := conversation.ProjectHash(projectRoot) + "/" + sessionID
	idx, ok := s.indices[key]
	if ok {
		sess, err := s.store.OpenID(projectRoot, sessionID, "")
		if err != nil {
			return nil, nil, err
		}
		return sess, idx, nil
	}
	sess, err := s.store.OpenID(projectRoot, sessionID, "")
	if err != nil {
		return nil, nil, err
	}
	idx = recall.NewIndex(s.embed, sess.Dir()+"/vectors.json")
	if err := idx.Load(); err != nil {
		// 索引损坏不应让会话打不开：重建即可（向量可以从日志重算）。
		idx = recall.NewIndex(s.embed, sess.Dir()+"/vectors.json")
	}
	// 索引为空但日志有消息（例如首次升级、或索引丢失）：从状态回填。
	if idx.Len() == 0 {
		idx.AddBatch(indexItems(sess.State()))
		_ = idx.Save()
	}
	s.indices[key] = idx
	s.wire(sess, idx, key)
	return sess, idx, nil
}

func (s *Service) wire(sess *conversation.Session, idx *recall.Index, key string) {
	sess.OnAppend(func(seq int64, role conversation.Role, content string) {
		if role == conversation.RoleUser {
			// 红线/需求点自动抽取：用户说了约束，立刻入册，不需要额外操作。
			_, _ = sess.AutoIngest(content, 0)
		}
		_ = idx.Add(recall.Item{
			Seq:     seq,
			Kind:    "msg",
			Role:    string(role),
			Text:    content,
			Preview: preview(content, 120),
		})
		s.mu.Lock()
		s.saveCount[key]++
		n := s.saveCount[key]
		s.mu.Unlock()
		if n%saveEvery == 0 {
			_ = idx.Save()
		}
	})
}

func indexItems(st *conversation.SessionState) []recall.Item {
	out := make([]recall.Item, 0, len(st.Messages)+len(st.Redlines)+len(st.Requirement))
	for _, m := range st.Messages {
		out = append(out, recall.Item{Seq: m.Seq, Kind: "msg", Role: string(m.Role), Text: m.Content})
	}
	for _, r := range st.Redlines {
		out = append(out, recall.Item{Seq: r.Seq, Kind: "redline", Text: r.Text})
	}
	for _, r := range st.Requirement {
		out = append(out, recall.Item{Seq: r.Seq, Kind: "req", Text: r.Text})
	}
	for _, g := range st.Segments {
		if g.Summary != "" {
			out = append(out, recall.Item{Seq: g.OpenSeq, Kind: "segment", Text: g.Title + "：" + g.Summary})
		}
	}
	return out
}

func preview(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// --- frontend-facing API ----------------------------------------------------

// MsgView is a message shaped for the UI.
type MsgView struct {
	Seq       int64  `json:"seq"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Time      int64  `json:"time"`
	SegmentID string `json:"segmentId,omitempty"`
	Tokens    int    `json:"tokens"`
}

// SessionOpen opens (or creates) a session and returns its info.
func (s *Service) SessionOpen(projectRoot, sessionID string) (conversation.SessionInfo, error) {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return conversation.SessionInfo{}, err
	}
	return sess.Info(), nil
}

// SessionList lists the sessions of a project.
func (s *Service) SessionList(projectRoot string) ([]conversation.SessionInfo, error) {
	return s.store.ListSessions(projectRoot)
}

// SessionMessages returns the tail of a session's messages (limit<=0 → all).
func (s *Service) SessionMessages(projectRoot, sessionID string, limit int) ([]MsgView, error) {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return nil, err
	}
	msgs := sess.State().Recent(limit)
	out := make([]MsgView, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, MsgView{Seq: m.Seq, Role: string(m.Role), Content: m.Content, Time: m.Time, SegmentID: m.SegmentID, Tokens: m.Tokens})
	}
	return out, nil
}

// SessionAppend records a message. User messages are also scanned for red lines.
// Returns the journal seq of the new message.
func (s *Service) SessionAppend(projectRoot, sessionID, role, content string) (int64, error) {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return 0, err
	}
	return sess.AppendMessage(conversation.Role(role), content)
}

// SessionSegments lists the work segments.
func (s *Service) SessionSegments(projectRoot, sessionID string) ([]conversation.Segment, error) {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return nil, err
	}
	return sess.State().Segments, nil
}

// SessionOpenSegment starts a new segment.
func (s *Service) SessionOpenSegment(projectRoot, sessionID, title string) (string, error) {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return "", err
	}
	return sess.OpenSegment(title)
}

// SessionCloseSegment closes the active segment with a summary.
func (s *Service) SessionCloseSegment(projectRoot, sessionID, summary string) error {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return err
	}
	return sess.CloseSegment(summary)
}

// SessionRequirements lists requirement points.
func (s *Service) SessionRequirements(projectRoot, sessionID string) ([]conversation.Requirement, error) {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return nil, err
	}
	return sess.State().Requirement, nil
}

// SessionAddRequirement adds a requirement point.
func (s *Service) SessionAddRequirement(projectRoot, sessionID, text string) (int64, error) {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return 0, err
	}
	return sess.AddRequirement(text, "user")
}

// SessionDoneRequirement marks a requirement done.
func (s *Service) SessionDoneRequirement(projectRoot, sessionID, id string) error {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return err
	}
	return sess.DoneRequirement(id)
}

// SessionRedlines lists the red lines of a project session.
func (s *Service) SessionRedlines(projectRoot, sessionID string) ([]conversation.Redline, error) {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return nil, err
	}
	return sess.State().Redlines, nil
}

// SessionAddRedline records a red line (hard constraint) manually.
func (s *Service) SessionAddRedline(projectRoot, sessionID, text string) (int64, error) {
	sess, idx, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return 0, err
	}
	seq, err := sess.AddRedline(text, "user", "hard")
	if err != nil {
		return 0, err
	}
	_ = idx.Add(recall.Item{Seq: seq, Kind: "redline", Text: text})
	_ = idx.Save()
	return seq, nil
}

// SessionRecall runs semantic recall over the session history.
func (s *Service) SessionRecall(projectRoot, sessionID, query string, k int) ([]recall.Hit, error) {
	sess, idx, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return nil, err
	}
	if k <= 0 {
		k = 5
	}
	hits, err := idx.Search(query, k, func(it recall.Item) bool { return it.Kind != "redline" })
	if err != nil {
		return nil, err
	}
	// 召回写回日志：以后审计时能解释"为什么这段旧内容进了提示词"。
	seqs := make([]int64, 0, len(hits))
	scores := make([]float64, 0, len(hits))
	for _, h := range hits {
		seqs = append(seqs, h.Seq)
		scores = append(scores, h.Score)
	}
	_ = sess.RecordRecall(query, seqs, scores)
	return hits, nil
}

// SessionAudit returns the audit trail.
func (s *Service) SessionAudit(projectRoot, sessionID string, fromSeq int64, limit int) ([]conversation.AuditEntry, error) {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return nil, err
	}
	return sess.Audit(fromSeq, limit), nil
}

// SessionAnchors lists anchors.
func (s *Service) SessionAnchors(projectRoot, sessionID string) ([]conversation.AnchorInfo, error) {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return nil, err
	}
	return sess.Anchors(), nil
}

// SessionDropAnchor snapshots the current state.
func (s *Service) SessionDropAnchor(projectRoot, sessionID string) (conversation.AnchorInfo, error) {
	sess, _, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return conversation.AnchorInfo{}, err
	}
	return sess.DropAnchor()
}

// SessionRollback rolls the session back to target seq. The audit trail is kept.
func (s *Service) SessionRollback(projectRoot, sessionID string, target int64, note string) error {
	sess, idx, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return err
	}
	if err := sess.Rollback(target, note); err != nil {
		return err
	}
	// 向量索引必须同步截断，否则会召回已经被撤销的内容。
	idx.TruncateAfter(target)
	return idx.Save()
}

// SessionStats reports diagnostics (index size, embedding mode).
func (s *Service) SessionStats(projectRoot, sessionID string) (map[string]interface{}, error) {
	sess, idx, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return nil, err
	}
	stat := idx.Stats()
	info := sess.Info()
	return map[string]interface{}{
		"session":  info,
		"indexed":  stat.Items,
		"dim":      stat.Dim,
		"degraded": stat.Degraded,
		"lastErr":  stat.LastErr,
	}, nil
}

// PromptPreview assembles (but does not send) the prompt for a query. This is the
// "看看你到底给模型喂了什么" window — prompt engineering is only trustworthy if
// you can read the assembled result.
func (s *Service) PromptPreview(projectRoot, sessionID, query string) (prompt.Packed, error) {
	sess, idx, err := s.pair(projectRoot, sessionID)
	if err != nil {
		return prompt.Packed{}, err
	}
	hits, err := idx.Search(query, 5, func(it recall.Item) bool { return it.Kind != "redline" })
	if err != nil {
		hits = nil
	}
	return prompt.Pack(prompt.Input{
		State:  sess.State(),
		Query:  query,
		Recall: hits,
		Budget: prompt.DefaultBudget(),
	}), nil
}

// --- lifecycle --------------------------------------------------------------

// Shutdown flushes indices and closes journals.
func (s *Service) Shutdown() error {
	s.mu.Lock()
	indices := make([]*recall.Index, 0, len(s.indices))
	for _, idx := range s.indices {
		indices = append(indices, idx)
	}
	s.mu.Unlock()
	for _, idx := range indices {
		_ = idx.Save()
	}
	return s.store.Close()
}

// Now is a tiny helper so the UI can render timestamps without a second binding.
func (s *Service) Now() int64 { return time.Now().UnixMilli() }
