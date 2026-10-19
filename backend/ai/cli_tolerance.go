package ai

import "strings"

// 本地 CLI 的已知噪声：会打印出来、甚至让进程以非零码退出，但答案其实已经完整写在
// stdout 里了。zcode 尤其典型——它每次跑都会带着锁冲突告警退出或直接 exit 1，
// 而我们原本的做法是「退出码非零即判这一档死」，于是紧接着把剩下几档（全都没在跑）
// 也试一遍失败，最后报「所有 LLM 均不可用」。一次可用档位就这么被自己的噪声埋掉。
//
// 这里只在「报错里只有已知噪声 + stdout 看着确实像答案」时才收下 stdout，
// 其余情况照旧当失败处理，避免掩盖真正的故障。
var cliKnownNoise = []string{
	"ZCode Built-in 刷新失败",
	"EEXIST: file already exists, mkdir",
}

// minSalvageAnswerLen 是能被视为「答案」的 stdout 最小长度（按字符数）。
// 短于此的基本是截断的输出或报错回显，不该当答案收下。
const minSalvageAnswerLen = 32

// salvageCLIOutput 判断本次 CLI 失败是否可以救回 stdout 里的答案。
func salvageCLIOutput(detail, stdout string) (string, bool) {
	text := strings.TrimSpace(stdout)
	if len([]rune(text)) < minSalvageAnswerLen {
		return "", false
	}
	if detail == "" {
		return "", false
	}
	known := false
	for _, noise := range cliKnownNoise {
		if strings.Contains(detail, noise) {
			known = true
			break
		}
	}
	if !known {
		return "", false
	}
	return text, true
}
