// Package sshutil 通过系统 ssh 命令连远程服务器。
//
// 为什么不用 golang.org/x/crypto/ssh：
//  1. flare 的依赖只有 cobra/pflag/yaml，为一个功能引入一整套加密库不划算；
//  2. 系统 ssh 自带对方已有的一切——known_hosts 校验、~/.ssh/config、ssh-agent、
//     密码/2FA 交互、跳板机。用库重写这些，既费代码又容易在安全细节上出错；
//  3. 密码、2FA 的输入交给 ssh 自己在终端上完成，flare 不接触、不保存任何凭据。
//
// 代价：本机要装了 ssh 客户端（macOS/Linux 自带，Windows 10+ 也自带 OpenSSH）。
// 每次调用都是一个独立的 ssh 进程（不是长连接）——远程装服务只跑几条命令，
// 这点开销换来的好处是：中途 Ctrl+C、断网都不会留下半开连接。
package sshutil

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"flare/internal/log"
)

// defaultPort：SSH 默认端口
const defaultPort = 22

// sshBin 是 ssh 可执行文件的名字。声明成变量是为了单测能替换成"假 ssh"，
// 验证参数拼装而不真的连任何服务器
var sshBin = "ssh"

// ConnectConfig SSH 连接参数
type ConnectConfig struct {
	Host    string // IP 或域名
	Port    int    // 留空 = 22
	User    string // 留空 = ssh 用当前用户名
	KeyPath string // 私钥路径；留空 = 让 ssh 自己找（agent / ~/.ssh 下的默认密钥）
}

// Addr 返回 host:port，用于日志和错误提示
func (c ConnectConfig) Addr() string {
	port := c.Port
	if port == 0 {
		port = defaultPort
	}
	return net.JoinHostPort(c.Host, strconv.Itoa(port))
}

// Target 返回 ssh 的目标写法 user@host（User 为空则只有 host）。
// IPv6 字面量要加方括号：ssh 靠 [::1] 才能把冒号和 user@ 分开
func (c ConnectConfig) Target() string {
	host := c.Host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	if c.User == "" {
		return host
	}
	return c.User + "@" + host
}

// baseArgs 拼 ssh 的公共参数，顺序即命令行顺序：
//
//	-p 端口 -i 私钥 -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 user@host
//
// accept-new：第一次连某台机器时自动把指纹记进 known_hosts，之后指纹变了照样拒绝
// 连接（这是防中间人攻击的关键，不能图省事用 no）。
// ConnectTimeout=10：地址打不通时 10 秒内报错，而不是干等几分钟
func (c ConnectConfig) baseArgs() []string {
	port := c.Port
	if port == 0 {
		port = defaultPort
	}
	args := []string{"-p", strconv.Itoa(port)}
	if c.KeyPath != "" {
		args = append(args, "-i", expandHome(c.KeyPath))
	}
	args = append(args,
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
	)
	return append(args, c.Target())
}

// Client 是一条"配置好了但每次现连"的 SSH 通道。
// 不做长连接：每条命令起一个 ssh 进程，失败互不牵连
type Client struct {
	cfg ConnectConfig
}

// Connect 验证"这台服务器能登录"：跑一条 true，成功才算连上。
// 直接用 ssh 的 argv 传命令（不拼 shell 字符串），命令里带空格、引号都不会被本机 shell 解析
func Connect(cfg ConnectConfig) (*Client, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("服务器地址不能为空")
	}
	// 提前拦住"路径写错"：否则 ssh 会退回默认密钥/交互认证，报错含义不清
	if cfg.KeyPath != "" {
		if _, err := os.Stat(expandHome(cfg.KeyPath)); err != nil {
			return nil, fmt.Errorf("读不到私钥 %s（--key 路径写错了？或留空让 ssh 自己找）: %w", cfg.KeyPath, err)
		}
	}
	c := &Client{cfg: cfg}
	if err := c.RunCommand("true"); err != nil {
		return nil, fmt.Errorf("SSH 登录 %s 失败: %w", cfg.Addr(), err)
	}
	return c, nil
}

// RunCommand 执行一条命令或一段脚本，stdout/stderr 直通终端——
// ssh 自己的报错（域名解析、认证失败、主机指纹变了）都摆在用户眼前，不吞掉
func (c *Client) RunCommand(script string) error {
	cmd := exec.Command(sshBin, c.argsFor(script)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return c.run(cmd)
}

// RunCommandOutput 执行命令并取回输出（stdout+stderr 合并、去首尾空白），不回显。
// 用于"看一眼远程系统信息"这类要拿结果的探测。
// 不把 stdin 接给远程命令：密码提示由 ssh 自己从 /dev/tty 读，不受影响
func (c *Client) RunCommandOutput(script string) (string, error) {
	cmd := exec.Command(sshBin, c.argsFor(script)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// RunScript 把多行脚本从 stdin 灌进远程 bash 执行：ssh host bash -s < 脚本。
//
// 为什么不把脚本当命令参数传：
//  1. 远程登录 shell 可能是 dash/sh，脚本里的 set -o pipefail 不一定支持，
//     显式叫 bash 才是确定的；
//  2. 脚本作为 argv 传要经过远程 shell 再解析一遍，多行、引号、$ 都容易走样；
//     走 stdin 是字节原样送达。
//
// 退出码由 ssh 原样带回：脚本失败 → ssh 非零退出 → 这里返回错误
func (c *Client) RunScript(script string) error {
	cmd := exec.Command(sshBin, c.scriptArgs()...)
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return c.run(cmd)
}

// argsFor 单条命令的完整 argv（测试直接断言它，不执行）
func (c *Client) argsFor(script string) []string {
	return append(c.cfg.baseArgs(), script)
}

// scriptArgs 多行脚本走 bash -s 的完整 argv
func (c *Client) scriptArgs() []string {
	return append(c.cfg.baseArgs(), "bash", "-s")
}

func (c *Client) run(cmd *exec.Cmd) error {
	log.Debugf("执行: %s", strings.Join(cmd.Args, " "))
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// expandHome 把 ~ 开头的路径换成家目录。
// exec.Command 不经过 shell，不会帮忙展开 ~，不处理的话 ssh 会去找一个字面叫 "~" 的文件
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
