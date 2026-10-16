package envconfig

import (
	"os"
	"path/filepath"
)

// paths.go — G2 路径注入：~/.tiancan* 三类目录的唯一构造入口。
//
// 历史遗留三个目录名并存，本文件保持语义不变、只做收敛与覆盖口：
//   - ConfigHome：~/.tiancan        （配置：MCP、自定义 provider、扩展、token、沙箱 bin）
//   - DataHome：  ~/.tiancan-ide    （数据/运行时：playwright、debug 工具链、lm-studio）
//   - MemoryHome：~/.tiancan-ai-ide （项目记忆）
//   - ProjectDataDir：<root>/.tiancan（项目级数据，跟随项目根）
//
// 每个目录都有环境变量覆盖口，便于测试隔离与 PC 端工作区绑定。

const (
	// DotDir 是项目级与用户级配置目录名（历史沿用，勿改）。
	// 原定义在 backend/agent/config，G2 收敛时上移到本叶子包；config.DotDir 保留别名兼容。
	DotDir = ".tiancan"
	// dataDotDir 是 DataHome 的默认目录名（历史沿用，勿改——改了用户存量数据会丢）。
	dataDotDir = ".tiancan-ide"
	// memoryDotDir 是 MemoryHome 的默认目录名（历史沿用，勿改）。
	memoryDotDir = ".tiancan-ai-ide"
)

func userHome() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return os.Getenv("HOME") // UserHomeDir 失败的兜底（保持与原调用点一致的行为）
}

// ConfigHome 返回用户级配置目录：$TIANCAN_CONFIG_HOME 或 ~/.tiancan。
func ConfigHome() string {
	if override := EnvString("TIANCAN_CONFIG_HOME"); override != "" {
		return override
	}
	return filepath.Join(userHome(), DotDir)
}

// DataHome 返回用户级数据/运行时目录：$TIANCAN_DATA_HOME 或 ~/.tiancan-ide。
func DataHome() string {
	if override := EnvString("TIANCAN_DATA_HOME"); override != "" {
		return override
	}
	return filepath.Join(userHome(), dataDotDir)
}

// MemoryHome 返回记忆目录：$TIANCAN_MEMORY_DIR 或 ~/.tiancan-ai-ide。
// （覆盖变量沿用历史名字 TIANCAN_MEMORY_DIR，不改名以免破坏既有部署。）
func MemoryHome() string {
	if override := EnvString("TIANCAN_MEMORY_DIR"); override != "" {
		return override
	}
	return filepath.Join(userHome(), memoryDotDir)
}

// ProjectDataDir 返回项目级数据目录：<rootPath>/.tiancan。
func ProjectDataDir(rootPath string) string {
	return filepath.Join(rootPath, DotDir)
}
