#!/usr/bin/env bash
set -euo pipefail

# flare 中继服务端（frps）一键安装脚本 —— 仅 Linux + systemd
#
# 用法:
#   sudo bash install-relay.sh
#   RELAY_PORT=7100 sudo bash install-relay.sh    # 换控制端口
#
# 做的事: 下载 frps → 装到 /usr/local/bin → 生成 /etc/frps/frps.toml（随机令牌）
#        → 注册并启动 systemd 服务 flare-frps
# 重复执行是安全的: 会沿用已有的 Token，已配好的客户端不会作废

FRP_VERSION="0.71.0"
BIND_PORT="${RELAY_PORT:-7000}"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/frps"
SERVICE_NAME="flare-frps"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# ---- 前置检查: 这些条件不满足，后面每一步都会以难懂的方式失败 ----
[ "$(id -u)" -eq 0 ] || error "请用 root 运行（sudo bash install-relay.sh）"
[ "$(uname -s)" = "Linux" ] || error "此脚本仅支持 Linux"
command -v systemctl >/dev/null 2>&1 || error "找不到 systemctl（本机没有 systemd），请改用 Docker 部署: 见仓库 docker/relay-server/"
command -v curl >/dev/null 2>&1 || error "找不到 curl，请先安装（apt install curl / yum install curl）"
case "$BIND_PORT" in
    *[!0-9]*|"") error "RELAY_PORT 必须是数字，收到: $BIND_PORT" ;;
esac
[ "$BIND_PORT" -ge 1 ] && [ "$BIND_PORT" -le 65535 ] || error "RELAY_PORT 需在 1~65535，收到: $BIND_PORT"

# ---- 架构映射: uname -m → frp 发布包里的架构名 ----
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  FRP_ARCH="amd64" ;;
    aarch64) FRP_ARCH="arm64" ;;
    armv7l)  FRP_ARCH="arm" ;;
    *)       error "不支持的架构: $ARCH" ;;
esac

FILENAME="frp_${FRP_VERSION}_linux_${FRP_ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/fatedier/frp/releases/download/v${FRP_VERSION}/${FILENAME}"
# 镜像源与 flare 内置下载器保持一致；最后一项为空串 = GitHub 原始地址
MIRRORS=("https://ghfast.top/" "https://gh-proxy.com/" "")

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

# ---- 下载: 逐个镜像源试，全失败才退出 ----
download_ok=false
for mirror in "${MIRRORS[@]}"; do
    info "尝试下载: ${mirror:-GitHub 原始地址} ..."
    if curl -fsSL --connect-timeout 10 -o "$TMP_DIR/$FILENAME" "${mirror}${DOWNLOAD_URL}"; then
        download_ok=true
        info "下载成功"
        break
    fi
    warn "下载失败，换下一个源"
done
[ "$download_ok" = true ] || error "所有下载源均失败，请检查本机网络"

info "正在解压..."
tar -xzf "$TMP_DIR/$FILENAME" -C "$TMP_DIR"
install -m 755 "$TMP_DIR/frp_${FRP_VERSION}_linux_${FRP_ARCH}/frps" "$INSTALL_DIR/frps"
info "frps 已安装到 $INSTALL_DIR/frps"

# ---- Token: 已有配置就沿用，没有才生成 ----
TOKEN=""
if [ -f "$CONFIG_DIR/frps.toml" ]; then
    TOKEN=$(sed -n 's/^auth\.token *= *"\(.*\)"/\1/p' "$CONFIG_DIR/frps.toml" | head -n1)
fi
if [ -n "$TOKEN" ]; then
    info "沿用已有配置里的 Token"
elif command -v openssl >/dev/null 2>&1; then
    TOKEN=$(openssl rand -hex 16)   # 128 位随机数
else
    TOKEN=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')   # 没有 openssl 也能生成
fi

# ---- 写配置与 systemd 服务 ----
mkdir -p "$CONFIG_DIR"
cat > "$CONFIG_DIR/frps.toml" <<EOF
bindPort = ${BIND_PORT}
auth.token = "${TOKEN}"
EOF
chmod 600 "$CONFIG_DIR/frps.toml"   # 里面有令牌，权限按密码对待
info "配置文件: $CONFIG_DIR/frps.toml"

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
info "systemd 服务 $SERVICE_NAME 已启动（端口 ${BIND_PORT}）"

# ---- 取本机公网 IP，直接把客户端命令拼好；取不到就留占位符 ----
SERVER_IP=$(curl -s --connect-timeout 5 https://api.ipify.org 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}' || true)
SERVER_IP=${SERVER_IP:-<服务器公网IP>}

echo ""
echo "frps 安装完成"
echo "  二进制: $INSTALL_DIR/frps"
echo "  配置:   $CONFIG_DIR/frps.toml"
echo "  端口:   $BIND_PORT"
echo "  日志:   journalctl -u $SERVICE_NAME -f"
echo ""
echo "在客户端机器上执行:"
echo "  flare relay init ${SERVER_IP}:${BIND_PORT} --token ${TOKEN}"
echo "  flare relay add <名称> --local <端口>"
echo "  flare relay up"
echo ""
echo "也可以用 Docker 部署: 见仓库 docker/relay-server/"
