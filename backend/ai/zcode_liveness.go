package ai

// zcodeActivity 跟踪 zcode 自身日志的写入活跃度，作为「进程是否还在干活」的外部信号。
//
// 为什么需要它：zcode 在 -p 模式下跑 agent loop 时，中间的工具调用阶段
// （读文件 / 改文件 / 跑命令）会**连续多轮完全不往 stdout 写东西**，
// 但它同时在不间断地往自己的 ~/.zcode/cli/log/zcode-YYYY-MM-DD.jsonl 追加事件
// （turn.phase、session.event.persistence、model.request.* 等，粒度很细）。
//
// 天蚕原先只看 stdout 的「首包超时」，于是把这段完全正常的静默期判成卡死并杀进程，
// 用户看到的就永远是「zcode CLI 首包超时（1m30s 内没有任何输出）」——
// 哪怕从 zcode 自己的日志看，模型请求一直在成功、任务其实在推进。
//
// 有了活跃信号后：只要日志还在长，就说明它在干活，不该被超时杀掉；
// 只有当 stdout 静默**且**日志也停了，才判定真的卡死。

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// zcodeLogOverride 便于测试把日志目录指到临时目录；生产为空。
var zcodeLogOverride string

func zcodeLogDir() string {
	if zcodeLogOverride != "" {
		return zcodeLogOverride
	}
	return filepath.Join(homeDir(), ".zcode", "cli", "log")
}

// zcodeActivityMonitor 通过对比 zcode 日志文件大小的增长来判断它是否活跃。
type zcodeActivityMonitor struct {
	mu    sync.Mutex
	dir   string
	sizes map[string]int64 // 文件名 -> 上次观察到的大小
	last  time.Time        // 最近一次观察到增长的时刻（零值表示尚未观察到）
}

// newZCodeActivityMonitor 在启动 CLI **之前**调用，以便建立正确的基线。
func newZCodeActivityMonitor() *zcodeActivityMonitor {
	return &zcodeActivityMonitor{
		dir:   zcodeLogDir(),
		sizes: make(map[string]int64),
	}
}

// poll 检查日志是否有新写入；有则刷新活跃时刻。
func (m *zcodeActivityMonitor) poll() {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return // 目录不存在或不可读：保持沉默，退化为纯 stdout 判定
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "zcode-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		size := info.Size()
		prev, seen := m.sizes[name]
		switch {
		case !seen:
			// 首次见到只登记基线，不当作活跃：历史文件本身的存在不代表当前在干活。
			// 真正的活跃由下一次轮询观察到的增长来体现（轮询间隔 2s，足够灵敏）。
			m.sizes[name] = size
		case size > prev:
			m.sizes[name] = size
			m.last = now
		}
	}
}

// LastActive 返回最近一次观察到日志有写入的时刻；零值表示从未观察到。
// 供 chatGenericLocalCLI 的超时判定取用。
func (m *zcodeActivityMonitor) LastActive() time.Time {
	m.poll()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}
