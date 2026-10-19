package ai

// zcode 把每个 endpoint 的 provider 状态放在 ~/.zcode/v2/runtime 下，写文件时用
// mkdir 出来的「锁目录」保护（xxx.json.lock）。正常流程是写完立刻删掉，但**只要
// 进程被 kill——正是我们自己的首包/停滞超时在做的事——锁目录就会留在那里**。
//
// 后果很要命：残留之后每一次 zcode 启动都在 "ZCode Built-in 刷新" 阶段撞上
// EEXIST builtin provider 刷新不出来 → Model creation failed → exit status 1，
// 于是这一档彻底报废，只剩下 OpenRelay / LM Studio 这些本就没开的，最终报
// 「所有 LLM 均不可用」。症状是**每次超时 self-inflicted 之后必然复发**。
//
// 这里两道保险：
//  1. 调 CLI 前先扫一遍孤儿锁（只清明显陈旧的，绝不碰可能还在用的）；
//  2. 若仍然以锁冲突失败，按报错里给的具体路径精确清掉，只重试一次。

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// zcodeLockStaleAge 是判断「孤儿锁」的时间阈值。锁只在一次写文件的瞬间存在，
// 正常生命周期以毫秒计；能活过这个时长的，一定是被 kill 或崩溃留下的。
const zcodeLockStaleAge = 10 * time.Minute

// zcodeRuntimeRootOverride 便于测试把 runtime 目录指到临时目录；生产为空。
var zcodeRuntimeRootOverride string

func zcodeRuntimeRoot() string {
	if zcodeRuntimeRootOverride != "" {
		return zcodeRuntimeRootOverride
	}
	return filepath.Join(homeDir(), ".zcode", "v2", "runtime")
}

// sweepZCodeLocks 清掉 zcode 的陈旧锁（目录或文件），返回清理数量。
//
// 只动满足两个条件的条目：名字以 .lock 结尾，且最后修改时间早于
// zcodeLockStaleAge —— 避免误删另一个正在跑的 zcode 手上的锁。
func sweepZCodeLocks() int {
	root := zcodeRuntimeRoot()
	removed := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil // 目录不存在 / 不可读：没有 zcode 就没存货可清
		}
		if !strings.HasSuffix(d.Name(), ".lock") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if time.Since(info.ModTime()) < zcodeLockStaleAge {
			return nil
		}
		if d.IsDir() {
			if rmErr := os.RemoveAll(path); rmErr == nil {
				removed++
				log.Printf("ai: 清除 zcode 孤儿锁目录 %s（陈旧 %s）", path, time.Since(info.ModTime()).Round(time.Second))
			}
			return fs.SkipDir
		}
		if rmErr := os.Remove(path); rmErr == nil {
			removed++
			log.Printf("ai: 清除 zcode 孤儿锁文件 %s（陈旧 %s）", path, time.Since(info.ModTime()).Round(time.Second))
		}
		return nil
	})
	return removed
}

// zcodeLockPathPattern 从 zcode 的报错里抠出锁路径：
// "EEXIST: file already exists, mkdir '<...>.lock'"
var zcodeLockPathPattern = regexp.MustCompile(`'([^']+\.lock)'`)

// isZCodeModelCreationFailure 判断是否是我们重排 providerOrder 造成的失败：
// 模型往往只绑在原来的账号型档上，被 API-key 档顶下去之后 zcode 找不到可用
// provider，就会报 "Model creation failed"。
func isZCodeModelCreationFailure(msg string) bool {
	return strings.Contains(msg, "Model creation failed")
}

// extractZCodeLockPaths 抠出报错里提到的锁路径，并做范围校验。
// 只在 zcode 自己明确说「拿不到这把锁」时才动手，比按时间猜测更不容易误伤。
func extractZCodeLockPaths(msg string) []string {
	if msg == "" {
		return nil
	}
	matches := zcodeLockPathPattern.FindAllStringSubmatch(msg, -1)
	if len(matches) == 0 {
		return nil
	}
	var paths []string
	seen := map[string]bool{}
	for _, m := range matches {
		path := filepath.Clean(m[1])
		// 只允许清理 zcode runtime 目录下的锁，避免拿报错里的任意路径开刀。
		rel, err := filepath.Rel(zcodeRuntimeRoot(), path)
		if err != nil || strings.HasPrefix(rel, "..") || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

// clearZCodeLocksInError 清掉报错里点名的锁，返回真正被清掉的路径。
func clearZCodeLocksInError(msg string) []string {
	var cleared []string
	for _, path := range extractZCodeLockPaths(msg) {
		if rmErr := os.RemoveAll(path); rmErr == nil {
			cleared = append(cleared, path)
			log.Printf("ai: 按报错清除 zcode 锁 %s", path)
		}
	}
	return cleared
}
