package ai

// zcode 内部可以同时配置多个凭证（provider）。CLI 并没有 --provider 可选参数
// （实测 `zcode -p ... --provider x` 会报 Unknown option），所以无法在命令行上挑凭证：
// 用哪个凭证完全由 ~/.zcode/v2/provider_config.json 的 providerOrder 决定。
//
// 后果是：一旦排在前面的凭证挂了（例如某个自建中转 124.222.126.15 平均 243s 才响应、
// 甚至直接连不上），zcode 会在它上面反复重试且期间不往 stdout 吐任何字，
// 天蚕这边的「首包超时」就必然触发，最后整档被判死 —— 即使用户明明还有别的可用凭证。
//
// 这里的做法：**并发探针**。同时对每个可用凭证发一个极轻量的 /models 请求，
// 谁先成功、谁最快就用谁，并把胜出的凭证顶到 providerOrder 第一位再调 CLI。
// 相比逐个串行试（每个都要耗掉整个首包超时，5 个凭证就是几分钟），
// 并发探针在几百毫秒到几秒内就能定下可用档。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// probeTimeout 单个凭证探针的上限。探针只求「通不通、快不快」，宁可放弃慢的。
const probeTimeout = 8 * time.Second

// probeTTL 探针结果的缓存时长，避免每次对话都重新探一轮。
const probeTTL = 10 * time.Minute

// zcodeCredential 是 zcode 里的一个可用凭证。
type zcodeCredential struct {
	ID      string
	Name    string
	BaseURL string
	APIKey  string
	APIType string // openai-responses / anthropic-messages / ...
	Models  []string
}

// zcodeProviderPathOverride 便于测试把配置指到临时文件；生产为空。
var zcodeProviderPathOverride string

func zcodeProviderPath() string {
	if zcodeProviderPathOverride != "" {
		return zcodeProviderPathOverride
	}
	return filepath.Join(homeDir(), ".zcode", "v2", "provider_config.json")
}

// loadZCodeCredentials 读出全部「可探测」的凭证：必须是 enabled 且带 api-key 的
// 自定义 provider（账号型 provider 走 OAuth，没有可直接探测的 key，跳过）。
func loadZCodeCredentials() ([]zcodeCredential, error) {
	data, err := os.ReadFile(zcodeProviderPath())
	if err != nil {
		return nil, err
	}
	// 真实结构里 providerRules 嵌在 providerConfigRules 之下。
	var raw struct {
		Config struct {
			ProviderOrder []string `json:"providerOrder"`
			ProviderConfigRules struct {
				ProviderRules []struct {
					ProviderID   string `json:"providerId"`
					ProviderName string `json:"providerName"`
					Enabled      *bool  `json:"enabled"`
					Config       struct {
						Access struct {
							Type   string `json:"type"`
							APIKey string `json:"apiKey"`
						} `json:"access"`
					API struct {
						Type    string `json:"type"`
						BaseURL string `json:"baseUrl"`
					} `json:"api"`
						PersonalModelIDs []string `json:"personalModelIds"`
					} `json:"config"`
				} `json:"providerRules"`
			} `json:"providerConfigRules"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	var out []zcodeCredential
	for _, r := range raw.Config.ProviderConfigRules.ProviderRules {
		if r.Enabled != nil && !*r.Enabled {
			continue // 已禁用
		}
		if r.Config.Access.APIKey == "" || r.Config.API.BaseURL == "" {
			continue // 账号型（OAuth）或配置不全，无法用 key 直探
		}
		out = append(out, zcodeCredential{
			ID:      r.ProviderID,
			Name:    r.ProviderName,
			BaseURL: strings.TrimRight(r.Config.API.BaseURL, "/"),
			APIKey:  r.Config.Access.APIKey,
			APIType: r.Config.API.Type,
			Models:  r.Config.PersonalModelIDs,
		})
	}
	return out, nil
}

// probeOne 探测单个凭证是否可用，返回耗时。
//
// 关键：不能用 GET /models 来判快慢。实测某个已挂的中转站 GET /models 只要 151ms（比健康的
// 那家还快），但真正生成时平均要 243s —— 列表接口快完全不代表生成快，照它挑会精准踩坑。
// 所以这里发一次**真实的最小生成请求**（max_tokens=1）来测端到端延迟。
func probeOne(ctx context.Context, c zcodeCredential) (time.Duration, error) {
	model := ""
	if len(c.Models) > 0 {
		model = c.Models[0]
	}
	if model == "" {
		model = "glm-5.3-flash"
	}
	// 依据 api.type 决定先试哪个端点，失败再退到另一个。
	endpoints := []string{"/chat/completions", "/responses"}
	if strings.Contains(c.APIType, "responses") {
		endpoints = []string{"/responses", "/chat/completions"}
	}
	var lastErr error
	for _, ep := range endpoints {
		lat, err := probeGenerate(ctx, c, model, ep)
		if err == nil {
			return lat, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

// probeGenerate 对指定端点发一次最小生成请求，返回端到端耗时。2xx 视为可用。
func probeGenerate(ctx context.Context, c zcodeCredential, model, endpoint string) (time.Duration, error) {
	var body []byte
	if endpoint == "/responses" {
		body = []byte(fmt.Sprintf(`{"model":%q,"input":"hi","max_output_tokens":1}`, model))
	} else {
		body = []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":1,"stream":false}`, model))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+endpoint, strings.NewReader(string(body)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: probeTimeout}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // 读净连接，避免影响计时与复用
	latency := time.Since(start)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return latency, fmt.Errorf("%s HTTP %d", endpoint, resp.StatusCode)
	}
	return latency, nil
}

type probeResult struct {
	cred    zcodeCredential
	latency time.Duration
	err     error
}

type probeOutcome struct {
	best   *zcodeCredential
	failed map[string]bool // 明确被判为不可用的 providerId
}

// probeZCodeCredentials 并发探测全部凭证，返回最快成功的那个 + 明确失败集合。
func probeZCodeCredentials(creds []zcodeCredential) *probeOutcome {
	if len(creds) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout+2*time.Second)
	defer cancel()

	results := make(chan probeResult, len(creds))
	var wg sync.WaitGroup
	for _, c := range creds {
		wg.Add(1)
		go func(c zcodeCredential) {
			defer wg.Done()
			lat, err := probeOne(ctx, c)
			results <- probeResult{cred: c, latency: lat, err: err}
		}(c)
	}
	wg.Wait()
	close(results)

	var ok []probeResult
	failed := make(map[string]bool)
	for r := range results {
		if r.err == nil {
			ok = append(ok, r)
		} else {
			failed[r.cred.ID] = true
			log.Printf("ai: zcode 凭证探针不可用 %s(%s) %s: %v", r.cred.Name, r.cred.ID, r.cred.BaseURL, r.err)
		}
	}
	if len(ok) == 0 {
		return &probeOutcome{failed: failed}
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i].latency < ok[j].latency })
	best := ok[0]
	log.Printf("ai: zcode 凭证探针选中 %s(%s) %s，耗时 %v（候选 %d/%d 可用）",
		best.cred.Name, best.cred.ID, best.cred.BaseURL, best.latency.Round(time.Millisecond), len(ok), len(creds))
	return &probeOutcome{best: &best.cred, failed: failed}
}

var (
	zcodeProbeMu     sync.Mutex
	zcodeProbeBest   *zcodeCredential
	zcodeProbeAt     time.Time
	zcodeProbeFailed map[string]bool
)

// bestZCodeCredential 返回当前应使用的凭证（带缓存）。探不到任何可用凭证时返回 nil，
// 此时调用方保持原样，不改动 zcode 配置。
func bestZCodeCredential() *zcodeCredential {
	zcodeProbeMu.Lock()
	defer zcodeProbeMu.Unlock()
	if zcodeProbeBest != nil && time.Since(zcodeProbeAt) < probeTTL {
		return zcodeProbeBest
	}
	creds, err := loadZCodeCredentials()
	if err != nil {
		log.Printf("ai: 读取 zcode 凭证配置失败: %v", err)
		return zcodeProbeBest // 保留上一次的结论，不因为读失败就清空
	}
	if best := probeZCodeCredentials(creds); best != nil {
		if best.best != nil {
			zcodeProbeBest = best.best
		}
		zcodeProbeFailed = best.failed
		zcodeProbeAt = time.Now()
	}
	return zcodeProbeBest
}

// applyZCodeProviderOrder 在必要时把指定凭证顶到 providerOrder 首位。
//
// 关键：**不能无条件重排**。providerOrder 里可能包含**账号型** provider
// （如 account:bigmodel-individual-coding-plan），它们走 OAuth、没有可直接探测的 key，
// 但往往正是 setting 指定的主用档，并且**只有它们绑定了某些模型**
// （实测 GLM-5.3 仅绑在 bigmodel 个人编码套餐上，API-key 档并不提供）。
// 若每次都把探出来的 API-key 档顶到首位，zcode 为选用模型找不到可用 provider，
// 会直接报 "Model creation failed"，把本来能用的链路打断。
//
// 因此这里只在「首位被明确探判定为不可用」时才介入，其余情况保持 zcode 自身顺序不动。
func applyZCodeProviderOrder(id string, failed map[string]bool) error {
	path := zcodeProviderPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	cfg, _ := doc["config"].(map[string]interface{})
	if cfg == nil {
		return fmt.Errorf("zcode 配置缺少 config 节点")
	}
	if raw, ok := cfg["providerOrder"].([]interface{}); ok && len(raw) > 0 {
		// 首位是 zcode 当前的主用档。只有当它被明确探挂了才把它挪走；
		// 探不了（账号型/OAuth）或探通的一律保持原顺序。
		if first, ok := raw[0].(string); ok && first != id && !failed[first] {
			log.Printf("ai: zcode providerOrder 首位 %s 仍可用，保持不动（不因探针重排）", first)
			return nil
		}
		var original []string
		for _, v := range raw {
			if s, ok := v.(string); ok {
				original = append(original, s)
			}
		}
		order := []string{id}
		for _, s := range original {
			if s != id {
				order = append(order, s)
			}
		}
		// 先备份「动它之前」的顺序（完整原样，不能丢条目）：重排是我们在替 zcode
		// 做决定，一旦换上来这一档报 Model creation failed（模型往往只绑在原来的
		// 账号型档上），还要能立刻退回去。见 restoreZCodeProviderOrder。
		backupZCodeProviderOrder(original)
		cfg["providerOrder"] = order
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// zcodeOrderBackupPathOverride 便于测试把备份指到临时目录；生产为空。
var zcodeOrderBackupPathOverride string

// zcodeOrderBackupPath 存放「我们重排之前」的 providerOrder。
func zcodeOrderBackupPath() string {
	if zcodeOrderBackupPathOverride != "" {
		return zcodeOrderBackupPathOverride
	}
	return filepath.Join(homeDir(), ".tiancan-ai-ide", "zcode-provider-order-backup.json")
}

// backupZCodeProviderOrder 记下重排前的顺序，供失败回滚使用。
// 已有备份时不覆盖——那份才是用户原本的顺序。
func backupZCodeProviderOrder(order []string) {
	if len(order) == 0 {
		return
	}
	path := zcodeOrderBackupPath()
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(order, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// restoreZCodeProviderOrder 把 providerOrder 还原成我们动它之前的样子，
// 删除备份。没有备份（即我们没重排过）时返回 false。
func restoreZCodeProviderOrder() bool {
	path := zcodeOrderBackupPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var order []string
	if err := json.Unmarshal(data, &order); err != nil || len(order) == 0 {
		_ = os.Remove(path)
		return false
	}
	cfgPath := zcodeProviderPath()
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return false
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false
	}
	cfg, _ := doc["config"].(map[string]interface{})
	if cfg == nil {
		return false
	}
	cfg["providerOrder"] = order
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false
	}
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return false
	}
	if err := os.Rename(tmp, cfgPath); err != nil {
		return false
	}
	_ = os.Remove(path)
	log.Printf("ai: 已还原 zcode providerOrder 为 %v", order)
	// 冷静期：回滚说明这次重排没帮上忙，短期内不要再试，否则来回抖动。
	zcodeProbeMu.Lock()
	zcodeReorderBlockUntil = time.Now().Add(30 * time.Minute)
	zcodeProbeMu.Unlock()
	return true
}

// zcodeReorderBlockUntil 在此时间之前不做 providerOrder 重排（回滚后的冷静期）。
var zcodeReorderBlockUntil time.Time

// preferZCodeCredential 在调用 zcode CLI 之前把最快可用的凭证顶上去。
// 失败不影响主流程（最多是沿用 zcode 自己的顺序）。
func preferZCodeCredential() {
	zcodeProbeMu.Lock()
	blocked := time.Now().Before(zcodeReorderBlockUntil)
	zcodeProbeMu.Unlock()
	if blocked {
		// 上次重排被回滚过，冷静期内不再替 zcode 做决定。
		return
	}
	// 先确保探过一轮：既拿到最快可用档，也拿到「明确失败」集合，
	// 后者决定是否真的需要动 providerOrder。
	bestZCodeCredential()

	zcodeProbeMu.Lock()
	best := zcodeProbeBest
	failed := zcodeProbeFailed
	zcodeProbeMu.Unlock()
	if best == nil {
		return
	}
	if err := applyZCodeProviderOrder(best.ID, failed); err != nil {
		log.Printf("ai: 切换 zcode 凭证到 %s 失败: %v", best.ID, err)
	}
}
