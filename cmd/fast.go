package cmd

import (
	"flare/internal/daemon"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

// fastAuht 是 --auth 旗标的目标变量。
// 声明为包级变量，因为旗标解析发生在 RunE 执行之前，而 RunE 是闭包，
// 通过包级变量才能把解析结果传递到命令执行逻辑中。
var fastAuht string

// --relay（走自建 frps 中继）与 --proto（中继协议）的目标变量
var fastRelay bool
var fastProto string

// fastCmd 定义 "flare fast" 子命令：快速启动免域名隧道。
// 用法：flare fast <端口>，例如 flare fast 3000 会把本地 3000 端口暴露到公网。
var fastCmd = &cobra.Command{
	// Use 声明命令的用法格式。注意 cobra 会把 Use 的第一个单词当作命令名，
	// 所以 "fast" 和参数之间必须用空格隔开；写成 "fast[端口]" 会让命令名
	// 变成 "fast[端口]"，导致 "flare fast 3000" 匹配不到命令。
	// [端口] 表示该参数为必填的端口号
	Use: "fast [端口]",
	// Short 是在命令列表（help 概览）中显示的简短描述
	Short: "快速启动免域名隧道",
	// Long 是执行 "flare fast --help" 时显示的详细帮助文案
	Long: "无需账号与域名，一条命令生成临时公网地址。\n" +
		"适合临时分享，Ctrl+C 退出后地址失效。\n" +
		"加 --relay 则走自建中继服务器（TCP/UDP，需先 flare relay init）。",
	// Args 校验命令行参数个数：ExactArgs(1) 表示必须恰好传 1 个参数，
	// 例如 "flare fast 3000"，参数不足或过多都会直接报错
	Args: func(cmd *cobra.Command, args []string) error {
		//只允许一个参数，多参数弹错误
		if len(args) != 1 {
			return fmt.Errorf("需要恰好 1 个端口参数")
		}
		// 将输入的命令转化为整形
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("端口必须是 1~65535 的数字，收到: %q", args[0])
		}
		return nil
	},
	// RunE 是命令真正执行时的入口函数；返回 error 时 cobra 会打印错误并以非零码退出
	RunE: func(cmd *cobra.Command, args []string) error {
		// args[0] 即命令行传入的端口号（由上面的 Args 校验保证存在）
		// 中继模式（--relay）：临时占用服务器上一个同号端口，本地端口和远端端口一样
		if fastRelay {
			if fastAuht != "" {
				// 不静默忽略：用户以为加了密码保护，实际没有，这种事必须说清
				fmt.Println("注意: 中继模式暂不支持 --auth 密码保护，本次穿透无验证")
			}
			return daemon.StartRelayQuick(args[0], fastProto)
		}
		// 带 --auth 时先架一层登录网关，再让 cloudflared 指向网关
		if fastAuht != "" {
			user, pwd, err := parseAuth(fastAuht)
			if err != nil {
				return err
			}
			return daemon.StartFastWithAuth(args[0], user, pwd)
		}
		return daemon.StartFast(args[0])
	},
}

// init 在包初始化阶段执行，用于注册子命令和定义旗标
func init() {
	// StringVar(目标变量, 旗标名, 默认值, 帮助文案)：
	// 把 --auth 旗标的字符串值绑定到包级变量 fastAuht 上
	fastCmd.Flags().StringVar(&fastAuht, "auth", "", "启用密码保护(格式：用户名：密码)")
	// 中继模式：不依赖 Cloudflare，走自建服务器（TCP/UDP 不限协议）
	fastCmd.Flags().BoolVar(&fastRelay, "relay", false, "用中继模式穿透（需先 flare relay init）")
	fastCmd.Flags().StringVar(&fastProto, "proto", "tcp", "中继协议：tcp 或 udp，仅 --relay 时有效")
	// 把 fastCmd 注册为 systemCmd 的子命令，用户才能通过 CLI 调用到它
	systemCmd.AddCommand(fastCmd)
}
