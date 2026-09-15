package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// 版本信息。这里的默认值给"直接 go build 源码"的场景兜底；
// 正式发版时用 -ldflags 在编译期注入，源码里不必手改：
//
//	go build -ldflags "-X flare/cmd.version=1.2.3 -X flare/cmd.commit=$(git rev-parse --short HEAD)"
//
// 为什么要这么绕：版本号若写死在源码里，每次发版都得改代码、还容易忘；
// 编译期注入让"打出来的二进制"和"源码"能对上账
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "查看版本信息",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("flare %s (commit %s, built %s)\n", version, commit, date)
		fmt.Printf("平台: %s/%s  运行时: %s\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
		// 报版本时顺手带上数据目录：问版本的人十有八九在排查环境问题
		fmt.Println(dataDirNote())
		return nil
	},
}

func init() {
	// 赋了 Version，cobra 就自动支持 "flare --version"
	systemCmd.Version = version
	systemCmd.AddCommand(versionCmd)
}
