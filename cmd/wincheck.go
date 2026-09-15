package cmd

import (
	"fmt"
	"regexp"
)

// winVerRe 只认"数字.数字"这种版本号形状。
// 为什么不去匹配英文单词 "Version"：`ver` 的输出会被本地化，
// 中文 Windows 上是 "Microsoft Windows [版本 10.0.19045.3803]"，
// 匹配单词会直接失效（这个检查就静默不干活了）。只认数字最稳。
var winVerRe = regexp.MustCompile(`(\d+)\.\d+`)

// parseWindowsMajor 从 `ver` 命令的输出里取出主版本号，取不到返回 0：
//
//	"Microsoft Windows [Version 10.0.19045.3803]" → 10
//	"Microsoft Windows [版本 10.0.19045.3803]"    → 10
//	"看不懂的输出"                                  → 0（调用方据此放行，不误拦）
func parseWindowsMajor(out string) int {
	m := winVerRe.FindStringSubmatch(out)
	if len(m) < 2 {
		return 0
	}
	n := 0
	for _, c := range m[1] {
		n = n*10 + int(c-'0')
	}
	return n
}

// unsupportedWindowsMsg 旧系统上的说明文案。
// 单独一个函数是为了能在非 Windows 平台上测（checkWindowsVersion 本身带编译标签，跑不到）
func unsupportedWindowsMsg(major int) string {
	return fmt.Sprintf(
		"错误: 当前系统 Windows NT %d 不受支持\n"+
			"flare 依赖的 cloudflared 和 Go 运行时都要求 Windows 10 或更高版本。\n"+
			"Windows 7/8/8.1 上请改用 Linux/macOS，或在 WSL 里运行 flare。\n", major)
}
