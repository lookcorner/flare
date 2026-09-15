package download

import (
	"flare/internal/config"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// frpVersion：锁定的 frp 版本。
// 为什么不能像 cloudflared 那样用 latest/download 固定地址？
// 因为 frp 的资产文件名里带着版本号（frp_0.71.0_linux_amd64.tar.gz），
// 不先知道版本号就拼不出下载地址。升级只需改这一行。
const frpVersion = "0.71.0"

// FrpcPath 返回 frpc 在本机的存放路径（与 cloudflared 同一个 bin 目录）
func FrpcPath() string {
	name := "frpc"
	if runtime.GOOS == "windows" {
		name = "frpc.exe"
	}
	return filepath.Join(config.Dir(), "bin", name)
}

// frpcEntry 是压缩包内我们要取出的那个文件的文件名
func frpcEntry() string {
	if runtime.GOOS == "windows" {
		return "frpc.exe"
	}
	return "frpc"
}

// frpcName 拼出 GitHub 发布页上的资产文件名，同时排除不支持的平台。
// frp 只给 windows 发 .zip，其余平台都是 .tar.gz——这就是 install 要认两种后缀的原因
func frpcName() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64":
		return fmt.Sprintf("frp_%s_%s_%s.tar.gz", frpVersion, runtime.GOOS, runtime.GOARCH), nil
	case "windows/amd64", "windows/arm64":
		return fmt.Sprintf("frp_%s_%s_%s.zip", frpVersion, runtime.GOOS, runtime.GOARCH), nil
	default:
		return "", fmt.Errorf("frpc 不支持的平台: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

// Frpc 首次调用时下载 frpc（中继模式的客户端），已存在则直接用。返回可执行文件路径
func Frpc() (string, error) {
	dest := FrpcPath()
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	name, err := frpcName()
	if err != nil {
		return "", err
	}
	origin := "https://github.com/fatedier/frp/releases/download/v" + frpVersion + "/"
	if err := fetch(dest, origin, name, frpcEntry()); err != nil {
		return "", fmt.Errorf("下载 frpc 失败: %w", err)
	}
	return dest, nil
}
