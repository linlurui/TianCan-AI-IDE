package tools

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/linlurui/tiancan-ai-ide/backend/envconfig"
)

// rlimit.go — G4 进程级沙箱：经 shell ulimit 前奏给 bash 工具注入资源限制。
//
// 溯源：G4 计划实测 setrlimit=0 命中（仅 Setpgid+Kill）。Go 的 SysProcAttr
// 不暴露 rlimit 设置，最稳的可移植做法是在既有 `bash -c` 前加 ulimit 前奏——
// 只前插、不改写用户命令本体，无引号转义风险。
//
// 环境变量（经 envconfig 统一读取）：
//   TIANCAN_RLIMIT_DISABLE=1   整体关闭
//   TIANCAN_RLIMIT_CPU         CPU 秒数（默认 600，0=不限制）
//   TIANCAN_RLIMIT_AS_KB       地址空间 KB（默认 4GB，0=不限制）
//   TIANCAN_RLIMIT_FSIZE_KB    单文件大小 KB（默认 1GB，0=不限制）
//   TIANCAN_RLIMIT_NPROC       进程数（默认 512，0=不限制）

// ResourceLimits 描述 bash 工具的资源限制。
type ResourceLimits struct {
	CPUSeconds   uint64 // ulimit -t
	MemoryKB     uint64 // ulimit -v
	FileSizeKB   uint64 // ulimit -f
	MaxProcesses uint64 // ulimit -u
}

// 默认上限（可被 env 覆盖）。
const (
	rlimitDefaultCPUSeconds   = 600
	rlimitDefaultMemoryKB     = 4 * 1024 * 1024 // 4GB
	rlimitDefaultFileSizeKB   = 1024 * 1024     // 1GB
	rlimitDefaultMaxProcesses = 512
)

// DefaultResourceLimits 返回当前生效的资源限制（env 可调；0 表示该项不限制）。
func DefaultResourceLimits() ResourceLimits {
	return ResourceLimits{
		CPUSeconds:   uint64(envconfig.EnvInt("TIANCAN_RLIMIT_CPU", rlimitDefaultCPUSeconds)),
		MemoryKB:     uint64(envconfig.EnvInt("TIANCAN_RLIMIT_AS_KB", rlimitDefaultMemoryKB)),
		FileSizeKB:   uint64(envconfig.EnvInt("TIANCAN_RLIMIT_FSIZE_KB", rlimitDefaultFileSizeKB)),
		MaxProcesses: uint64(envconfig.EnvInt("TIANCAN_RLIMIT_NPROC", rlimitDefaultMaxProcesses)),
	}
}

// resourceLimitsEnabled 报告 ulimit 前奏是否启用。
// Windows 无 bash/ulimit 语义，直接关闭。
func resourceLimitsEnabled() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	return !envconfig.EnvBool("TIANCAN_RLIMIT_DISABLE")
}

// wrapWithResourceLimits 给命令前插 ulimit 前奏；未启用或全部项为 0 时原样返回。
//
// 前奏单项失败（如 macOS 不支持 ulimit -v）只吞错继续，不影响命令本体执行——
// 这是"尽力而为"的护栏，不是强制隔离；强制隔离靠 G4 的 deny 规则与 G13 的沙箱提供方。
func wrapWithResourceLimits(command string, lim ResourceLimits) string {
	if !resourceLimitsEnabled() {
		return command
	}
	var prelude strings.Builder
	if lim.CPUSeconds > 0 {
		fmt.Fprintf(&prelude, "ulimit -t %d 2>/dev/null; ", lim.CPUSeconds)
	}
	if lim.MemoryKB > 0 {
		fmt.Fprintf(&prelude, "ulimit -v %d 2>/dev/null; ", lim.MemoryKB)
	}
	if lim.FileSizeKB > 0 {
		fmt.Fprintf(&prelude, "ulimit -f %d 2>/dev/null; ", lim.FileSizeKB)
	}
	if lim.MaxProcesses > 0 {
		fmt.Fprintf(&prelude, "ulimit -u %d 2>/dev/null; ", lim.MaxProcesses)
	}
	if prelude.Len() == 0 {
		return command
	}
	return prelude.String() + command
}
