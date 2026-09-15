package cmd

import (
	"flare/internal/config"
	"flare/internal/download"
	"flare/internal/log"
	"flare/internal/sshutil"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var (
	setupHost     string
	setupPort     int
	setupUser     string
	setupKeyPath  string
	setupFrpsPort int
)

var relayServerSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "通过 SSH 远程安装 frps（远程需为 Linux + systemd）",
	Long: "登录远程 Linux 服务器，自动装好 frps、生成配置并启动 systemd 服务（flare-frps），\n" +
		"最后把服务器地址与 Token 写进本机配置——装完直接 flare relay up 就能用。\n" +
		"密码、2FA 的输入交给系统 ssh 自己完成，flare 不接触、不保存你的密码。\n" +
		"\n" +
		"示例:\n" +
		"  flare relay server setup --host 1.2.3.4 --key ~/.ssh/id_ed25519\n" +
		"  flare relay server setup --host example.com --port 2222 --user root\n" +
		"  flare relay server setup    # 全交互：逐项提问",
	RunE: runRelayServerSetup,
}

func runRelayServerSetup(cmd *cobra.Command, args []string) error {
	if setupFrpsPort < 1 || setupFrpsPort > 65535 {
		return fmt.Errorf("--frps-port 必须是 1~65535 的端口，收到: %d", setupFrpsPort)
	}
	sshCfg, err := collectSSHConfig()
	if err != nil {
		return err
	}

	label := sshCfg.Addr()
	if sshCfg.User != "" {
		label = sshCfg.User + "@" + label
	}
	fmt.Printf("正在连接 %s ...\n", label)
	client, err := sshutil.Connect(*sshCfg)
	if err != nil {
		return err
	}
	fmt.Println("SSH 登录成功")

	if err := preflightChecks(client); err != nil {
		return err
	}

	fmt.Println("\n正在安装 frps ...")
	if err := client.RunScript(buildInstallScript(setupFrpsPort)); err != nil {
		return fmt.Errorf("远程安装失败: %w\n可登录服务器排查: journalctl -u flare-frps -n 50", err)
	}
	log.Auditf("frps 已安装到 %s（端口 %d）", sshCfg.Addr(), setupFrpsPort)

	// 把远程生成的 Token 读回来：客户端要拿它登录，不能让用户自己再登一次服务器去抄
	token, err := client.RunCommandOutput(`sed -n 's/^auth\.token *= *"\(.*\)"/\1/p' /etc/frps/frps.toml`)
	if err != nil || token == "" {
		fmt.Println("没能自动读回 Token，请登录服务器查看 /etc/frps/frps.toml，然后执行:")
		fmt.Printf("  flare relay init %s:%d --token <token>\n", sshCfg.Host, setupFrpsPort)
		return nil
	}

	serverAddr := fmt.Sprintf("%s:%d", sshCfg.Host, setupFrpsPort)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Relay.Server = serverAddr
	cfg.Relay.Token = token
	if err := config.Save(cfg); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("frps 远程安装完成")
	fmt.Printf("  服务器: %s\n", serverAddr)
	fmt.Printf("  Token:  %s\n", token)
	fmt.Printf("  已写入本机配置: %s\n", config.Path())
	fmt.Println()
	fmt.Println("接下来在本机:")
	fmt.Println("  flare relay add <名称> --local <端口>")
	fmt.Println("  flare relay up")
	return nil
}

// collectSSHConfig 收集连接参数：缺 --host 就转成逐项提问（回车 = 用方括号里的默认值）。
// 只问 host/port/user/私钥路径——密码不归我们管，ssh 会在需要时自己提示
func collectSSHConfig() (*sshutil.ConnectConfig, error) {
	host := strings.TrimSpace(setupHost)
	port := setupPort
	user := strings.TrimSpace(setupUser)
	keyPath := strings.TrimSpace(setupKeyPath)

	if host == "" {
		fmt.Println("交互模式（回车 = 使用方括号里的默认值）")
		host = askLine("服务器地址 (IP 或域名)", "")
		if host == "" {
			return nil, fmt.Errorf("服务器地址不能为空（也可以一条命令给全: flare relay server setup --host <IP>）")
		}
		port = askInt("SSH 端口", port)
		user = askLine("SSH 用户名", user)
		if keyPath == "" { // 已经用 --key 给过就不再问，别把用户的参数覆盖掉
			keyPath = askLine("SSH 私钥路径（留空 = 用 ssh 默认密钥/agent）", "")
		}
	}

	// --host 里顺手带了端口（1.2.3.4:2222）时以它为准：这是最明确的写法
	if h, p, ok := splitHostPort(host); ok {
		fmt.Printf("从地址里识别到 SSH 端口: %d\n", p)
		host, port = h, p
	}
	if user == "" {
		user = "root"
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("SSH 端口必须是 1~65535，收到: %d", port)
	}
	return &sshutil.ConnectConfig{Host: host, Port: port, User: user, KeyPath: keyPath}, nil
}

// splitHostPort 识别 "主机:端口"（IPv6 用 [::1]:22）；认不出就返回 false，按纯主机名处理
func splitHostPort(s string) (string, int, bool) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil || host == "" || portStr == "" {
		return "", 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, false
	}
	return host, port, true
}

// askLine 问一行。回车 = 用默认值（默认值为空时就是"不填"）
func askLine(prompt, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", prompt, def)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line := strings.TrimSpace(readLine())
	if line == "" {
		return def
	}
	return line
}

// askInt 问一个端口，输入不合法就用默认值兜住，不用重问
func askInt(prompt string, def int) int {
	line := askLine(prompt, strconv.Itoa(def))
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > 65535 {
		fmt.Printf("  %q 不是合法端口，用默认值 %d\n", line, def)
		return def
	}
	return n
}

// preflightChecks 动手前先把"装不了"的原因查清楚：
// 不是 Linux、不是 root、架构不认识、已经装过、缺 curl/bash/systemd——
// 这些都会让安装脚本中途失败，提前拦住才能给出人话的错误
func preflightChecks(client *sshutil.Client) error {
	osName, err := client.RunCommandOutput("uname -s")
	if err != nil || osName != "Linux" {
		return fmt.Errorf("远程服务器不是 Linux（检测到: %s），远程安装只支持 Linux", osName)
	}
	uid, err := client.RunCommandOutput("id -u")
	if err != nil || uid != "0" {
		return fmt.Errorf("需要 root 权限（当前 uid: %s），请用 --user root 重试", uid)
	}
	arch, err := client.RunCommandOutput("uname -m")
	if err != nil {
		return fmt.Errorf("无法检测远程架构: %w", err)
	}
	switch arch {
	case "x86_64", "aarch64", "armv7l":
		fmt.Printf("远程环境: Linux %s, root\n", arch)
	default:
		return fmt.Errorf("不支持的远程架构: %s（frp 只提供 x86_64 / aarch64 / armv7l 的 Linux 包）", arch)
	}

	// 已经装过就必须停下来：重装会换 Token、抢端口，先把选择权交给用户。
	// frps 是本项目的旧 unit 名（以及别家脚本的默认名），一并检查免得端口撞车
	for _, unit := range []string{"flare-frps", "frps"} {
		if out, _ := client.RunCommandOutput("systemctl is-active " + unit + " 2>/dev/null"); out == "active" {
			return fmt.Errorf("远程服务器上 %s 已在运行（要重装先执行: systemctl disable --now %s）", unit, unit)
		}
	}

	// 脚本要用到的东西：curl 下载、bash 跑脚本、systemctl 注册服务
	for _, bin := range []string{"curl", "bash", "systemctl"} {
		if _, err := client.RunCommandOutput("command -v " + bin); err != nil {
			if bin == "systemctl" {
				return fmt.Errorf("远程服务器没有 systemd（缺 systemctl），请改用 Docker 部署: 见仓库 docker/relay-server/")
			}
			return fmt.Errorf("远程服务器缺少 %s，请先安装（Debian/Ubuntu: apt install %s；CentOS: yum install %s）", bin, bin, bin)
		}
	}
	return nil
}

// buildInstallScript 生成在远程服务器上以 root 执行的安装脚本。
// 抽成纯函数（不改本机状态、不连网）是为了能单测：脚本里的版本号、端口、unit 名、
// frps.toml 内容都是外部可见的契约，改错了要在这里被拦下来。
//
// 与 flare relay server install 的分工：本地 install 用数据目录 + 进程;
// 远程 setup 用系统路径 + systemd——服务器上要的是"开机自启、崩了重启"
func buildInstallScript(bindPort int) string {
	return fmt.Sprintf(`set -euo pipefail

# flare 远程安装 frps（由 flare relay server setup 生成，在服务器上以 root 执行）
FRP_VERSION="%s"
BIND_PORT=%d
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/frps"
SERVICE_NAME="flare-frps"

ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  FRP_ARCH="amd64" ;;
  aarch64) FRP_ARCH="arm64" ;;
  armv7l)  FRP_ARCH="arm" ;;
  *) echo "[ERROR] 不支持的架构: $ARCH"; exit 1 ;;
esac

FILENAME="frp_${FRP_VERSION}_linux_${FRP_ARCH}.tar.gz"
URL="https://github.com/fatedier/frp/releases/download/v${FRP_VERSION}/${FILENAME}"
MIRRORS=("https://ghfast.top/" "https://gh-proxy.com/" "")

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

# 逐个镜像源试，全失败才退出
ok=false
for mirror in "${MIRRORS[@]}"; do
  echo "[INFO] 尝试下载: ${mirror:-GitHub 原始地址} ..."
  if curl -fsSL --connect-timeout 10 -o "$TMP_DIR/$FILENAME" "${mirror}${URL}"; then
    ok=true
    break
  fi
  echo "[WARN] 下载失败，换下一个源"
done
if [ "$ok" != true ]; then
  echo "[ERROR] 所有下载源均失败，请检查服务器网络"
  exit 1
fi

tar -xzf "$TMP_DIR/$FILENAME" -C "$TMP_DIR"
install -m 755 "$TMP_DIR/frp_${FRP_VERSION}_linux_${FRP_ARCH}/frps" "$INSTALL_DIR/frps"
echo "[INFO] frps 已安装到 $INSTALL_DIR/frps"

# 随机 Token：优先 openssl，没有就退回 od（不依赖 xxd，最小化系统也有）
if command -v openssl >/dev/null 2>&1; then
  TOKEN=$(openssl rand -hex 16)
else
  TOKEN=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
fi

mkdir -p "$CONFIG_DIR"
cat > "$CONFIG_DIR/frps.toml" <<EOF
bindPort = ${BIND_PORT}
auth.token = "${TOKEN}"
EOF
chmod 600 "$CONFIG_DIR/frps.toml"
echo "[INFO] 已生成配置 $CONFIG_DIR/frps.toml"

cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=frps relay server (flare)
After=network.target

[Service]
ExecStart=${INSTALL_DIR}/frps -c ${CONFIG_DIR}/frps.toml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now "$SERVICE_NAME"
echo "[INFO] systemd 服务 $SERVICE_NAME 已启动（端口 ${BIND_PORT}）"
`, download.FrpVersion(), bindPort)
}

func init() {
	relayServerSetupCmd.Flags().StringVar(&setupHost, "host", "", "服务器 IP 或域名")
	relayServerSetupCmd.Flags().IntVarP(&setupPort, "port", "p", 22, "SSH 端口")
	relayServerSetupCmd.Flags().StringVar(&setupUser, "user", "root", "SSH 用户名")
	relayServerSetupCmd.Flags().StringVar(&setupKeyPath, "key", "", "SSH 私钥路径（默认让 ssh 自己找 agent/默认密钥）")
	relayServerSetupCmd.Flags().IntVar(&setupFrpsPort, "frps-port", 7000, "frps 在服务器上监听的端口")
	relayServerCmd.AddCommand(relayServerSetupCmd)
}
