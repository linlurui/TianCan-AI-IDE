// Package envconfig — G2 配置收敛：全仓 TIANCAN_* 环境变量与 ~/.tiancan* 路径的唯一读取入口。
//
// 本包是叶子包（仅依赖标准库），任何业务包都可安全导入，不会引入循环依赖。
package envconfig

import (
	"os"
	"strconv"
	"strings"
)

// env.go — G2 配置收敛：全仓 TIANCAN_* 环境变量的唯一读取入口。
//
// 契约：
//   - 所有 TIANCAN_* 环境变量只允许通过本文件的 Env* 函数读取，
//     业务代码不得再直接 os.Getenv（新增变量须在此登记语义）。
//   - 语义与历史实现兼容：Bool 原来写作 =="1"，现接受 "1/true/yes/on"（超集兼容）；
//     Int 解析失败回退默认值（与原 strconv.Atoi 忽略错误的语义一致）。
//   - envLookup 可在测试中替换（SetEnvLookupForTest），业务路径保持 os.Getenv。

var envLookup = os.Getenv

// SetEnvLookupForTest 替换底层 env 查询函数，返回恢复函数。
// 仅用于测试；生产代码不应调用。
func SetEnvLookupForTest(fn func(string) string) func() {
	old := envLookup
	envLookup = fn
	return func() { envLookup = old }
}

// EnvString 读取字符串型环境变量；未设置返回空串。
func EnvString(key string) string {
	return envLookup(key)
}

// EnvBool 读取布尔型环境变量。
// 兼容历史语义（=="1"），并接受 true/yes/on（大小写不敏感）；其余值一律 false。
func EnvBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(envLookup(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// EnvInt 读取整型环境变量；未设置或解析失败返回 def。
func EnvInt(key string, def int) int {
	v := strings.TrimSpace(envLookup(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// EnvFloat 读取浮点型环境变量；未设置或解析失败返回 def。
func EnvFloat(key string, def float64) float64 {
	v := strings.TrimSpace(envLookup(key))
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

// EnvList 读取逗号分隔列表；未设置返回 nil。
// 元素去空白，空元素被丢弃（与原 strings.Split 后逐元素 TrimSpace 的调用点语义一致）。
func EnvList(key string) []string {
	v := envLookup(key)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	var out []string // 保持 nil 语义：全空元素时与"未设置"一致（沿用原调用点行为）
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// 已登记的 TIANCAN_* 变量目录（新增变量请在此补充一行注释，便于审计）：
//
//	TIANCAN_DISABLE_RALPH          Bool   禁用 Ralph 自纠错循环
//	TIANCAN_RALPH_MAX_ITER         Int    Ralph 最大迭代数
//	TIANCAN_DISABLE_ROLLBACK       Bool   禁用回滚管理
//	TIANCAN_DISABLE_AUTO_MEMORY    Bool   禁用自动记忆提取
//	TIANCAN_MEMORY_MIN_TOKENS_INIT    Int 记忆提取阈值（首次）
//	TIANCAN_MEMORY_MIN_TOKENS_UPDATE  Int 记忆提取阈值（更新）
//	TIANCAN_MEMORY_TOOL_CALLS_UPDATE  Int 记忆提取阈值（工具调用数）
//	TIANCAN_MEMORY_DIR             String 记忆目录覆盖（见 paths.go MemoryHome）
//	TIANCAN_AUTO_MODE              Bool   自动批准模式
//	TIANCAN_BYPASS_PERMISSIONS     Bool   跳过权限检查
//	TIANCAN_DENY_TOOLS             List   追加 deny 工具
//	TIANCAN_ASK_TOOLS              List   追加 ask 工具
//	TIANCAN_READONLY_TOOLS         List   追加只读工具
//	TIANCAN_PROTECTED_PATHS        List   追加受保护路径
//	TIANCAN_CLASSIFIER_MODE        String YOLO 分类器模式
//	TIANCAN_CLASSIFIER_MODEL       String YOLO 分类器模型
//	TIANCAN_COORDINATOR_MODE       Bool   层级协调者模式
//	TIANCAN_TAG_SET                String 标签集选择
//	TIANCAN_MODEL                  String 默认模型覆盖
//	TIANCAN_TEXT_EXTENSIONS        List   @include 允许的文本扩展名
//	TIANCAN_MANAGED_CONFIG         String 托管配置路径
//	TIANCAN_BINARY_EXTS            List   二进制文件扩展名
//	TIANCAN_AUTORESEARCH_EVENTS    List   AutoResearch 触发事件
//	TIANCAN_MARKET_URL             String 技能市场地址
//	TIANCAN_SANDBOX_PROVIDER       String 沙箱提供方
//	TIANCAN_SANDBOX_INSTALL_DIR    String 沙箱安装目录
//	TIANCAN_GPU_MEMORY_GB          Float  GPU 显存覆盖
//	TIANCAN_GPU_NAME               String GPU 名称覆盖
//	TIANCAN_ROUTE_KEYWORDS         List   路由关键词覆盖
//	TIANCAN_PROJECT_ROOT           String 项目根目录覆盖
//	TIANCAN_AGENT_DEFS_DIR         String 子代理定义目录
//	TIANCAN_DISABLE_COMPACT        Bool   禁用上下文压缩
//	TIANCAN_CONTEXT_WINDOW         Int    上下文窗口大小覆盖
//	TIANCAN_AUTOCOMPACT_PCT_OVERRIDE Float 自动压缩阈值百分比
//	TIANCAN_SEARCH_SOURCE          String AutoResearch 搜索源
//	TIANCAN_SEARXNG_URL            String SearXNG 实例地址
//	TIANCAN_CONFIG_HOME            String 配置目录覆盖（见 paths.go ConfigHome）
//	TIANCAN_DATA_HOME              String 数据目录覆盖（见 paths.go DataHome）
