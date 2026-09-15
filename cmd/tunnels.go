package cmd

import (
	"flare/internal/cfapi"
	"flare/internal/config"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var tunnelsYes bool

// tunnelsCmd：列云端隧道，并标出本地在用的是哪条。
// 为什么需要它：本地 config.yml 最多只记得一条隧道，云端却可能有更多——
// create 中途失败、或换过隧道没清理，都会留下用不到的"孤儿"。
// 这些孤儿平时看不见，却占着名字、也让人怀疑"我到底在用哪条"。
var tunnelsCmd = &cobra.Command{
	Use:   "tunnels",
	Short: "列出云端的隧道（标出本地在用的那条）",
	Long: "本地只记一条隧道，云端可能不止一条。\n" +
		"用不到的（比如 create 失败留下的）可以用 flare tunnels delete <ID> 清理。",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Auth.ApiToken == "" {
			return fmt.Errorf("请先运行 flare init 配置认证信息")
		}
		client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)
		list, err := client.ListTunnels()
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("云端没有任何隧道（flare create <名称> 可创建一条）")
			return nil
		}

		fmt.Printf("%-38s %s\n", "隧道 ID", "名称")
		foundLocal := false
		for _, t := range list {
			mark := ""
			if t.ID == cfg.Tunnel.ID {
				mark = "  ← 本地使用中"
				foundLocal = true
			}
			fmt.Printf("%-38s %s%s\n", t.ID, t.Name, mark)
		}

		// 本地记着一条、云端却没有：说明云端被删过（或换了账户），得让用户知道，
		// 否则下次 up 会拿着一个不存在的隧道去连，报错还很难懂
		if cfg.Tunnel.ID != "" && !foundLocal {
			fmt.Printf("\n注意: 本地配置里的隧道 %s (%s) 在云端已不存在，请重新 create（或 flare destroy 清掉本地记录）\n",
				cfg.Tunnel.Name, cfg.Tunnel.ID)
		}
		if len(list) > 1 {
			fmt.Printf("\n云端还有 %d 条隧道；用不到的可以用 flare tunnels delete <ID> 清理\n", len(list)-1)
		}
		return nil
	},
}

var tunnelsDeleteCmd = &cobra.Command{
	Use:   "delete [隧道ID]",
	Short: "删除云端的一条隧道",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := strings.TrimSpace(args[0])
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Auth.ApiToken == "" {
			return fmt.Errorf("请先运行 flare init 配置认证信息")
		}
		// 本地正在用的那条不许从这里删：它牵扯 DNS 记录和本地配置，
		// 只有 flare destroy 会一并清理。从这里删掉会让本地指向一条已消失的隧道
		if id == cfg.Tunnel.ID {
			return fmt.Errorf("隧道 %s 是本机正在使用的那条，请用 flare destroy 销毁（它会一并清理 DNS 记录和本地配置）", id)
		}

		client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)
		list, err := client.ListTunnels()
		if err != nil {
			return err
		}
		name := ""
		for _, t := range list {
			if t.ID == id {
				name = t.Name
			}
		}
		if name == "" {
			return fmt.Errorf("云端找不到隧道 %s（flare tunnels 可查看全部）", id)
		}
		// 毁灭性操作：非 -y 要人工确认（和 destroy 一个规矩）
		if !tunnelsYes {
			fmt.Printf("将删除云端隧道 %s (%s)。输入隧道名称确认: ", name, id)
			if strings.TrimSpace(readLine()) != name {
				return fmt.Errorf("已取消")
			}
		}
		if err := client.DeleteTunnel(id); err != nil {
			return err
		}
		fmt.Printf("已删除云端隧道 %s (%s)\n", name, id)
		return nil
	},
}

func init() {
	tunnelsDeleteCmd.Flags().BoolVarP(&tunnelsYes, "yes", "y", false, "跳过确认")
	tunnelsCmd.AddCommand(tunnelsDeleteCmd)
	systemCmd.AddCommand(tunnelsCmd)
}
