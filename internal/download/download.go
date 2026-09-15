package download

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"flare/internal/config"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// 镜像源列表：前面是 GitHub 加速镜像（针对国内网络）
var mirrors = []string{
	"https://ghfast.top/",
	"https://gh-proxy.com/",
	"",
}

// 不实现隧道协议——它指挥一个现成的官方程序 cloudflared（Cloudflare 出品的隧道客户端）。就像 npm 不实现 JS 引擎、只负责调度 node 一样。
//
// 于是出现第一个问题：用户机器上没有 cloudflared。
// 解决：首次使用时自动下载。这带来四个子问题，逐个攻破：
// 1. 文件名因平台而异：darwin 是 cloudflared-darwin-arm64.tgz，linux 是 cloudflared-linux-amd64，Windows 是 cloudflared-windows-amd64.exe
// 2. 有的平台是压缩包（.tgz 要解压），有的是裸二进制（直接存）
// 3. GitHub 下载可能被墙/超时 → 多镜像源轮询
// 4. 下到一半失败 → 超时控制、错误报告，且绝不留下"半截文件"
// //////////////////////////////////
// Path 返回cloudflared 本机数据目录的路径
func Path() string {
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name = "cloudflared.exe"
	}
	return filepath.Join(config.Dir(), "bin", name)
}

// filename 根据"操作系统/架构"拼出 GitHub 上的发布文件名
func filename() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return "cloudflared-darwin-arm64.tgz", nil
	case "darwin/amd64":
		return "cloudflared-darwin-amd64.tgz", nil
	case "linux/amd64":
		return "cloudflared-linux-amd64", nil // linux 给裸二进制
	case "linux/arm64":
		return "cloudflared-linux-arm64", nil
	case "windows/amd64":
		return "cloudflared-windows-amd64.exe", nil
	default:
		// 编译期不知道用户会在哪跑，所以平台错误只能发生在运行时
		return "", fmt.Errorf("不支持的平台: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

}

// Download 依次尝试每个源，全失败才报错。返回目标路径
func Download() (string, error) {
	dest := Path()

	if _, err := os.Stat(dest); err == nil {
		return dest, nil // 已存在 直接返回，不重复下载
	}
	name, err := filename()
	if err != nil {
		return "", err
	}
	// GitHub Releases 的"最新版"下载地址是固定格式
	origin := "https://github.com/cloudflare/cloudflared/releases/latest/download/"
	if err := fetch(dest, origin, name, "cloudflared"); err != nil {
		return "", fmt.Errorf("下载 cloudflared 失败: %w", err)
	}
	return dest, nil
}

// fetch：把 origin+name 下载到 dest 并安装成可执行文件。
// 顺带承担三件事：建目录、轮询镜像源、超时控制。
// 任何一步失败都不会在 dest 留下文件——下一个"已存在就直接用"的判断才可信。
func fetch(dest, origin, name, entry string) error {
	// 整个请求最长等 120 秒——没有超时的 HTTP 请求会永久挂起
	client := &http.Client{Timeout: 120 * time.Second}

	var lastErr error
	for _, mirror := range mirrors {
		url := mirror + origin + name
		fmt.Printf("尝试下载: %s ...\n", mirrorDesc(mirror))
		resp, err := client.Get(url)
		if err != nil {
			fmt.Printf("  连接失败: %v\n", err)
			lastErr = err
			continue
		}
		// http状态码非 200 也换源（比如镜像没同步该文件）
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}

		// 保存并解压（依据文件名后缀选择路径）
		err = InstallArchive(resp.Body, dest, name, entry)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		fmt.Printf("已下载到 %s\n", dest)
		return nil

	}
	return fmt.Errorf("所有下载源均失败，最后错误: %w", lastErr)
}

func mirrorDesc(m string) string {
	if m == "" {
		return "github 原始地址"
	}
	return m
}

// InstallArchive 从发布包（.tgz/.tar.gz/.zip 或裸二进制）里取出 entry 并装到 dest。
// 导出是给自更新（selfupdate）复用的：它下载的是同一套发布包，没必要再写一份解压逻辑。
// 目录不存在也自己建：调用方不该关心 bin 目录有没有被创建过
//
// 后缀要认两种：cloudflared 的 mac 包叫 .tgz，frp/flare 自己的发布包叫 .tar.gz，内容完全一样
func InstallArchive(body io.Reader, dest, name, entry string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(name, ".tgz"), strings.HasSuffix(name, ".tar.gz"):
		return extractTgz(body, dest, entry)
	case strings.HasSuffix(name, ".zip"):
		return extractZip(body, dest, entry)
	default:
		return writeBinary(body, dest)
	}
}

// writeBinary 把字节流落成可执行文件：先写临时文件，成功后再改名到 dest。
// 直接写 dest 的话，下到一半断网就会留下一个半截文件——
// 下次启动时 os.Stat 看到文件存在，会当作"已经装好了"，问题极难排查。
func writeBinary(src io.Reader, dest string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*")
	if err != nil {
		return err
	}
	// 成功路径上临时文件已被改名，这里的 Remove 自然失败；失败路径上则负责清扫
	defer os.Remove(tmp.Name())

	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0755); err != nil {
		return err
	}
	// Windows 上 rename 不允许覆盖已存在的文件，先删掉可能残留的旧文件
	os.Remove(dest)
	return os.Rename(tmp.Name(), dest)
}

// extractTgz 用两层 reader 拆包：gzip 解压 或者 tar 遍历条目  找 entry
func extractTgz(body io.Reader, dest, entry string) error {
	gr, err := gzip.NewReader(body) // 第 1 层：把 .tgz 解成 .tar 流

	if err != nil {
		return fmt.Errorf("解压失败: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr) // 第 2 层：逐条读 tar 里的文件
	for {
		hander, err := tr.Next() // 移到下一条目；读完返回 io.EOF
		if err == io.EOF {
			return fmt.Errorf("tgz 中未找到 %s", entry)
		}

		if err != nil {
			return fmt.Errorf("解压失败: %w", err)
		}
		// tar 条目名是 文件夹/frpc，只比对文件名部分
		if filepath.Base(hander.Name) != entry {
			continue
		}
		return writeBinary(tr, dest)
	}
}

// extractZip：Windows 的 frp 发布包是 .zip（cloudflared 没有 zip，frpc 有）。
// zip 要先能随机访问目录表才能取单个文件，所以整个响应先落到临时文件再打开。
func extractZip(body io.Reader, dest, entry string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if _, err := io.Copy(tmp, body); err != nil {
		return err
	}
	zr, err := zip.OpenReader(tmp.Name())
	if err != nil {
		return fmt.Errorf("解压失败: %w", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		if filepath.Base(f.Name) != entry {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		return writeBinary(rc, dest)
	}
	return fmt.Errorf("zip 中未找到 %s", entry)
}
