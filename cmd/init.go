package cmd

import (
	"bufio"
	"flare/internal/config"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var initToken, initAccountId string
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "配置 Cloudflare API 认证信息",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiToken := strings.TrimSpace(initToken) // 去掉字符串 开头 和 结尾 的所有“空白字符” 普通空格（' '，U+0020）制表符（\t）	换行符（\n）回车符（\r）垂直制表符（\v）换页符（\f）以及部分特殊 Unicode 空白符（如 \u00A0 不间断空格、\u1680 等）
		accountId := strings.TrimSpace(initAccountId)
		// flag 没给  提示用户从终端粘贴
		if apiToken == "" {
			fmt.Println("粘贴 API 令牌: ")
			apiToken = strings.TrimSpace(readLine())
		}
		if accountId == "" {
			fmt.Println("粘贴账户 ID: ")
			accountId = strings.TrimSpace(readLine())
		}
		if apiToken == "" || accountId == "" {
			return fmt.Errorf("API 令牌和账户 ID 不能为空")
		}
		// 命名层的万能模板
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg.Auth = config.AuthConfig{ApiToken: apiToken, AccountID: accountId}
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("认证信息已保存到 %s\n", config.Path())
		// 顺带告诉用户数据目录在哪、哪种模式：换了机器/复制了 exe 找不到配置时最需要这一行
		fmt.Println(dataDirNote())
		return nil
	},
}

// readLine 读一行终端输入（去掉换行）
func readLine() string {
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n") //兼容windows的\r\n
}

func init() {
	initCmd.Flags().StringVar(&initToken, "token", "", "API 令牌")
	initCmd.Flags().StringVar(&initAccountId, "account", "", "账户 ID")
	systemCmd.AddCommand(initCmd)
}
