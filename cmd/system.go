package cmd

import (
	"flare/internal/config"
	"flare/internal/log"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// rootCmd 是命令树的根。注意：它没有 RunE ——
// cobra 对"无 RunE 的命令"的处理 = 打印用法。
// var rootCmd = &cobra.Command 是包级变量——所有命令文件都引用它（AddCommand 的目标），所以它必须是全局的。
var systemCmd = &cobra.Command{
	Use:   "flare",
	Short: "本地服务内网访问外网",
}

func Execute() {
	// Windows 上的第一件事：系统太旧就直接说清楚原因。
	// 否则后面 cloudflared/Go 运行时报的是"缺 DLL"这类看不懂的错
	checkWindowsVersion()

	// 日志：一份写终端，一份落 <数据目录>/logs/flare.log。
	// 初始化失败不拦路——写不了日志不该让任何命令不可用
	if err := log.Setup("flare"); err == nil {
		defer log.Close()
	}
	// 记一笔"用户执行了什么"。排查问题时，"最后一条命令是什么"往往就是答案。
	// 用 Auditf：只进日志文件，不在终端里重复回显用户刚敲的命令；
	// 参数先打码，详见 redactArgs
	log.Auditf("执行: flare %s", strings.Join(redactArgs(os.Args[1:]), " "))

	if err := systemCmd.Execute(); err != nil {
		log.Auditf("失败: %v", err)
		fmt.Println(err)
		os.Exit(1) //非零退出码：shell 能感知失败
	}
}

// sensitiveFlags：这些旗标后面跟的是凭据
var sensitiveFlags = []string{"--token", "--auth", "--api-token", "--password", "--secret"}

// redactArgs 把命令行里的凭据换成 ****** 再写日志。
// 日志文件会长期留在磁盘上（还可能被贴进 issue 里），把 token、密码原样写进去
// 等于自己把凭据泄出去。两种写法都要照顾：--token xxx 和 --token=xxx
func redactArgs(args []string) []string {
	out := make([]string, 0, len(args))
	maskNext := false
	for _, a := range args {
		if maskNext { // 上一个参数是敏感旗标，这个就是它的值
			out = append(out, "******")
			maskNext = false
			continue
		}
		flag := a
		eq := strings.Index(a, "=")
		if eq >= 0 {
			flag = a[:eq]
		}
		if !isSensitiveFlag(flag) {
			out = append(out, a)
			continue
		}
		if eq >= 0 {
			out = append(out, a[:eq+1]+"******") // --token=xxx
		} else {
			out = append(out, a) // --token xxx
			maskNext = true
		}
	}
	return out
}

func isSensitiveFlag(flag string) bool {
	for _, s := range sensitiveFlags {
		if s == flag {
			return true
		}
	}
	return false
}

// dataDirNote：告诉用户数据放在哪、是哪种模式。
// 便携模式（exe 旁边有 portTable 文件）和默认模式（~/.flare）差别很大，
// 用户换了机器、或复制了 exe 却找不到配置时，这一行能省下大量困惑
func dataDirNote() string {
	mode := "默认"
	if config.PortTable() {
		mode = "便携"
	}
	return fmt.Sprintf("数据目录: %s（%s模式）", config.Dir(), mode)
}
