package ai

import "testing"

// TestSalvageCLIOutput: zcode 每次都会带着锁冲突告警打印/退出，答案却在 stdout 里。
// 这种情况必须能救回答案，否则这一档被判死，整条备用链一起报「所有 LLM 均不可用」。
func TestSalvageCLIOutput(t *testing.T) {
	const answer = "这是一段足够长、看起来确实像答案的回复内容，用来验证「退出码非零但答案已完整」时能不能把答案救回来。"
	const lockNoise = "ZCode Built-in 刷新失败: EEXIST: file already exists, mkdir '/Users/rocky/.zcode/x.json.lock'"

	if got, ok := salvageCLIOutput(lockNoise, answer); !ok || got != answer {
		t.Fatalf("已知锁噪声 + 完整答案应当被救回，got ok=%v %q", ok, got)
	}
	// 答案要兜 strip 空白再返回。
	if got, ok := salvageCLIOutput(lockNoise, "\n  "+answer+"  \n"); !ok || got != answer {
		t.Fatalf("应返回 trim 后的答案，got %q", got)
	}

	// 短输出不当答案：那更像是报错回显而不是回复。
	if _, ok := salvageCLIOutput(lockNoise, "no"); ok {
		t.Fatal("短输出不应被当成答案")
	}
	// 没有已知噪声时不救（否则会掩盖真正的故障）。
	if _, ok := salvageCLIOutput("fatal: api key invalid", answer); ok {
		t.Fatal("非已知噪声不应被救回")
	}
	// 没有 stderr 佐证也不救。
	if _, ok := salvageCLIOutput("", answer); ok {
		t.Fatal("无诊断信息时不应猜测")
	}
	// 空 stdout 更不用说了。
	if _, ok := salvageCLIOutput(lockNoise, ""); ok {
		t.Fatal("空输出不应被当成答案")
	}
}
