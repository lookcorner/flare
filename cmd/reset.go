package cmd

import (
	"flare/internal/config"
	"flare/internal/daemon"
	"flare/internal/log"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// resetForce 对应 --force：跳过确认。
// reset 同时删云端资源和本地数据，默认必须人工确认
var resetForce bool

// resetCmd：回到"从没装过 flare"的状态。
//
// 与 destroy 的分工：destroy 只删隧道（保留 API 令牌，方便马上重建一条）；
// reset 是"全部推倒重来"——云端隧道、DNS 记录、本地配置、下载的二进制、
// 日志全部清掉，包括令牌。用户要的就是"彻底重来一次"这个语义
var resetCmd = &cobra.Command{
	Use:   "reset",
	Short: "重置全部（删除隧道 + 清除本地配置）",
	Long: "删除云端隧道及其 DNS 记录，并清掉本地全部数据（配置、令牌、日志、下载的程序）。\n" +
		"回到「从没装过 flare」的状态，之后需要重新 flare wizard（或 flare init）。",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !resetForce {
			fmt.Println("将删除云端隧道及其全部路由，并清除本地配置（含 API 令牌、路由、中继设置），此操作不可恢复！")
			fmt.Print("确认重置？(y/N): ")
			if !confirmYes(readLine()) {
				fmt.Println("已取消")
				return nil
			}
		}

		// 配置读不出来也要继续：配置坏掉、版本过高，正是需要 reset 的场合
		cfg, err := config.Load()
		if err != nil {
			log.Warnf("读取配置失败（继续重置）: %v", err)
			cfg = &config.Config{}
		}

		// 1) 先停后台进程。隧道马上要被删，留着一个还在重连的 cloudflared
		// 只会往日志里刷错误，也会让"重置完了"看起来还没完
		if daemon.Running() {
			if err := daemon.Stop(); err != nil {
				log.Warnf("停止 cloudflared 失败: %v", err)
			}
		}
		if daemon.RelayRunning() {
			if err := daemon.StopRelay(); err != nil {
				log.Warnf("停止 frpc 失败: %v", err)
			}
		}

		// 2) 云端：删 DNS 记录和隧道。复用 destroy 的实现——它按"先 DNS 后隧道"的顺序来，
		// 中途失败重跑即可（幂等），这份顺序逻辑不该在 reset 里再写一遍
		if cfg.Tunnel.ID == "" {
			fmt.Println("本地没有隧道记录，跳过云端清理")
		} else {
			destroyBool = true // 上面已经确认过了，别再问一次
			if err := destroyCmd.RunE(cmd, nil); err != nil {
				// 云端删不掉不拦着本地重置：本地数据留着才更危险（令牌、指向已删隧道的配置）
				fmt.Printf("警告: 删除云端隧道失败: %v\n", err)
				fmt.Println("云端可能还留着这条隧道，可稍后重跑 flare destroy，或到 Cloudflare 控制台手动删除")
			}
		}

		// 3) 本地：先清空内存里的配置字段，再删数据文件。
		// 顺序有讲究：万一文件删不掉（被占用、权限不足），至少不会留下一份
		// "指向已删隧道 + 明文令牌"的配置
		dir := config.Dir()
		wipeLocalConfig(cfg)
		if err := clearLocalData(dir); err != nil {
			log.Warnf("清理本地数据失败: %v", err)
			if saveErr := config.Save(cfg); saveErr != nil {
				log.Warnf("清空配置也失败: %v", saveErr)
			}
			return fmt.Errorf("本地数据未能全部清除: %w", err)
		}

		fmt.Printf("本地数据已清除: %s\n", dir)
		fmt.Println("重新开始: flare wizard（交互引导，一条命令配完）")
		log.Auditf("reset: 已重置全部（数据目录 %s）", dir)
		return nil
	},
}

// confirmYes：只有明确的 y/yes 才算同意，回车和任何其它输入都当作"不"
func confirmYes(input string) bool {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "y", "yes":
		return true
	}
	return false
}

// wipeLocalConfig 把配置里的内容全部抹掉（Auth 也不例外）。
//
// 与 destroy 的差别就在 Auth：destroy 特意保留令牌，好让用户马上 create 新隧道；
// reset 的语义是"回到从没装过"，令牌、中继设置一并清掉，也就是从头 flare init
func wipeLocalConfig(cfg *config.Config) {
	cfg.Auth = config.AuthConfig{}
	cfg.Tunnel = config.TunnelConfig{}
	cfg.Routes = nil
	cfg.Relay = config.RelayConfig{}
}

// localResetEntries 列出 reset 要清掉的数据目录条目（相对路径）。
//
// 为什么逐个点名，而不是把数据目录整个删掉：数据目录可能是用户用 FLARE_DIR
// 指定的、也可能是便携模式下 exe 所在目录 —— 里面很可能还住着用户自己的东西。
// 只有叫得出名字的才是 flare 建的
func localResetEntries() []string {
	return []string{
		"config.yml",       // 认证、隧道、路由、中继：全部配置
		"bin",              // 下载来的 cloudflared / frpc
		"logs",             // flare / cloudflared / frpc 三个日志
		"cloudflared.pid",  // 后台进程的记账文件
		"frpc.pid",         // 同上，frpc 的
		"fast-config.yaml", // flare fast 的临时配置
		"frpc.toml",        // 中继规则生成给 frpc 的配置文件
	}
}

// localResetTargets 把条目拼成完整路径。
// 单独抽出来是为了让"到底删哪些路径"这件事能被单测钉住
func localResetTargets(dir string) []string {
	entries := localResetEntries()
	out := make([]string, 0, len(entries))
	for _, name := range entries {
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// clearLocalData 删除数据目录下的 flare 数据，返回第一个错误（其余条目仍会尽力删完）
func clearLocalData(dir string) error {
	var firstErr error
	for _, path := range localResetTargets(dir) {
		if err := os.RemoveAll(path); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("清理 %s 失败: %w", path, err)
		}
	}
	// 默认模式下数据目录（~/.flare）是 flare 自己建的，清空后顺手把空目录也删掉，
	// 才真的像"从没装过"。两种情况下不能碰：
	//   - 便携模式：那是 exe 所在目录，portTable 标记还必须留着
	//   - 用户用 FLARE_DIR 指定的目录：那是用户的地盘
	// os.Remove 对非空目录会失败，正好当作"里面还有别的东西，留着"的信号
	if !config.PortTable() && os.Getenv("FLARE_DIR") == "" {
		os.Remove(dir)
	}
	return firstErr
}

func init() {
	resetCmd.Flags().BoolVar(&resetForce, "force", false, "跳过确认")
	systemCmd.AddCommand(resetCmd)
}
