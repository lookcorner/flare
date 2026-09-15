package download

import (
	"flare/internal/config"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// FrpVersion 返回锁定的 frp 版本号。
// 为什么要导出？远程安装脚本（flare relay server setup）要按同一个版本号拼下载地址，
// 版本号只能有一个出处，否则本机下的和服务器上装的对不上
func FrpVersion() string { return frpVersion }

// FrpsPath 返回 frps 在本机的存放路径（与 cloudflared、frpc 同一个 bin 目录）
func FrpsPath() string {
	name := "frps"
	if runtime.GOOS == "windows" {
		name = "frps.exe"
	}
	return filepath.Join(config.Dir(), "bin", name)
}

// frpsEntry 是压缩包内我们要取出的那个文件名。
// frps 和 frpc 打在同一个发布包里（frp_<版本>_<系统>_<架构>.tar.gz），
// 区别只在取哪个 entry：客户端取 frpc，服务端取 frps
func frpsEntry() string {
	if runtime.GOOS == "windows" {
		return "frps.exe"
	}
	return "frps"
}

// frpsName 拼出 GitHub 发布页上的资产文件名，同时排除不支持的平台。
// 与 frpc 的资产包完全相同——内容一致，只是本文件取的是 frps entry，
// 所以这里没有复用 frpcName：两者的错误提示要说清是哪个组件不支持
func frpsName() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64":
		return fmt.Sprintf("frp_%s_%s_%s.tar.gz", frpVersion, runtime.GOOS, runtime.GOARCH), nil
	case "windows/amd64", "windows/arm64":
		return fmt.Sprintf("frp_%s_%s_%s.zip", frpVersion, runtime.GOOS, runtime.GOARCH), nil
	default:
		return "", fmt.Errorf("frps 不支持的平台: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

// Frps 首次调用时下载 frps（中继服务端），已存在则直接用。返回可执行文件路径。
// 与 Frpc 共用同包的 fetch/mirrors：镜像源、超时、失败不留半截文件这些行为保持一致
func Frps() (string, error) {
	dest := FrpsPath()
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	name, err := frpsName()
	if err != nil {
		return "", err
	}
	origin := "https://github.com/fatedier/frp/releases/download/v" + frpVersion + "/"
	if err := fetch(dest, origin, name, frpsEntry()); err != nil {
		return "", fmt.Errorf("下载 frps 失败: %w", err)
	}
	return dest, nil
}
