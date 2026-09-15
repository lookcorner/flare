package cmd

import (
	"flare/internal/selfupdate"
	"fmt"

	"github.com/spf13/cobra"
)

// updateCheckOnly 对应 --check：只报告有没有新版本，不改动本机任何文件
var updateCheckOnly bool

// updateCmd：把自己换成新版本。
// 分两步走（先查、再下），而不是直接下载 ：查一次只有几 KB，
// 已经是最新版本时就不该让用户等一次几十 MB 的下载
var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "更新 flare 到最新版本",
	Long: "从 GitHub Releases 下载与当前平台匹配的发布包，替换 flare 自己。\n" +
		"--check 只检查版本，不下载、不替换。\n" +
		"下载地址取自编译期注入的发布仓库（见发版脚本的 -ldflags）。",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("正在检查更新...")
		latest, err := selfupdate.LatestVersion()
		if err != nil {
			return fmt.Errorf("检查更新失败: %w", err)
		}
		if !selfupdate.IsNewer(version, latest) {
			fmt.Printf("已是最新版本: %s\n", version)
			return nil
		}
		fmt.Printf("发现新版本: %s → %s\n", version, latest)
		if updateCheckOnly {
			fmt.Println("运行 flare update 进行更新")
			return nil
		}

		fmt.Println("正在下载并替换当前程序...")
		if err := selfupdate.Update(latest); err != nil {
			return fmt.Errorf("更新失败: %w", err)
		}
		// 正在跑的这个进程还是旧版本（内存里的映像换不掉），
		// 说清楚"下次运行才生效"，免得用户以为更新没成功
		fmt.Printf("已更新到 %s（下次运行 flare 即生效）\n", latest)
		fmt.Println("提示: 用 flare version 确认版本")
		return nil
	},
}

func init() {
	updateCmd.Flags().BoolVar(&updateCheckOnly, "check", false, "只检查是否有新版本")
	systemCmd.AddCommand(updateCmd)
}
