// Package selfupdate 让 flare 自己更新自己。
//
// 为什么需要它：flare 是单文件分发的命令行工具，用户把它放在 ~/bin、
// /usr/local/bin 这类地方，没有 brew/npm 帮你记账。没有自更新，用户就得
// 手动比对版本、手动下载、手动 chmod —— 大多数人不会做，于是长期停在旧版本。
package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"flare/internal/download"
)

// repo 是发布仓库（owner/name 形式）。故意留空、由编译期注入：
//
//	go build -ldflags "-X flare/internal/selfupdate.repo=owner/name"
//
// 为什么不写死：写死意味着"仓库改名要改代码"，而且别人 fork 出去的构建
// 会悄悄去下载原仓库的二进制 —— 那等于把不是自己发布的东西装进用户机器。
var repo = ""

// 两个超时分开设：查版本是几 KB 的小请求，下载是几十 MB 的大文件，
// 用同一个超时要么查得太久、要么下到大半被掐断
const (
	apiTimeout = 30 * time.Second
	dlTimeout  = 5 * time.Minute
)

// release 只取我们真正要用的字段；GitHub 的响应体很大，全解出来没意义
type release struct {
	TagName string `json:"tag_name"`
}

// errNoRepo 是"没注入仓库地址"时的统一错误。
// 报错必须同时说清"为什么没有"和"怎么修"，否则用户只会看到一句莫名其妙的失败
func errNoRepo() error {
	return errors.New("当前构建未配置发布仓库（发版时用 -ldflags -X flare/internal/selfupdate.repo=owner/name 注入）")
}

// LatestVersion 查询最新发布版本，返回形如 v1.2.3 的标签
func LatestVersion() (string, error) {
	if repo == "" {
		return "", errNoRepo()
	}
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// GitHub API 对没有 User-Agent 的请求直接 403，必须显式带一个
	req.Header.Set("User-Agent", "flare-selfupdate")

	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 GitHub 失败: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// 仓库不存在和"还没发过 release"的响应码都是 404，分不清就一起说
		return "", fmt.Errorf("仓库 %s 没有发布版本（或仓库不存在）", repo)
	case http.StatusForbidden, http.StatusTooManyRequests:
		// GitHub 对匿名请求按 IP 限流：公司/机房出口 IP 很容易撞上，
		// 报一句"稍后重试"比只给个状态码强
		return "", fmt.Errorf("查询失败: HTTP %d（GitHub API 限流，稍后重试）", resp.StatusCode)
	default:
		return "", fmt.Errorf("查询失败: HTTP %d", resp.StatusCode)
	}

	var r release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}
	if r.TagName == "" {
		return "", fmt.Errorf("仓库 %s 的最新发布没有版本号", repo)
	}
	return r.TagName, nil
}

// Update 下载指定版本，并替换当前可执行文件。
//
// 全程"先落到临时文件、最后 rename"：rename 在同一个文件系统上是原子操作，
// 用户要么拿到完整的新版本，要么还是原来的旧版本，不会出现半个二进制。
func Update(version string) error {
	if repo == "" {
		return errNoRepo()
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return errors.New("版本号为空")
	}
	asset, err := assetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	url := assetURL(version, asset)

	client := &http.Client{Timeout: dlTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 失败: HTTP %d（该版本可能没有 %s/%s 的发布包）",
			url, resp.StatusCode, runtime.GOOS, runtime.GOARCH)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位当前可执行文件失败: %w", err)
	}
	// 软链安装（/usr/local/bin/flare -> 真正的文件）时替换真实文件，
	// 否则会把软链本身换成一个普通文件，原目录下的旧版本永远留在磁盘上
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}

	// 临时文件必须与目标同目录：跨文件系统的 rename 会直接失败（EXDEV），
	// 而 /tmp 和 /usr/local/bin 经常不在一块盘上
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".flare-update-*")
	if err != nil {
		return fmt.Errorf("无法在 %s 下创建临时文件（%v），更新需要该目录可写：试试 sudo flare update，或手动替换",
			filepath.Dir(exe), err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath) // 成功路径上文件已被改名，这里的删除自然失败

	// 解压复用 download 的安装逻辑：同一套"下载后改名"的写法，
	// 失败时不会在目标位置留下半截文件
	if err := download.InstallArchive(resp.Body, tmpPath, asset, binaryEntry(runtime.GOOS)); err != nil {
		return fmt.Errorf("安装新版本失败: %w", err)
	}

	leftover, err := replaceSelf(exe, tmpPath)
	if err != nil {
		return err
	}
	if leftover != "" {
		// Windows 特例：旧版本不能删（映像还在使用中），只能改名留着
		fmt.Printf("提示: 旧版本已改名为 %s，不再需要时可手动删除\n", leftover)
	}
	return nil
}

// assetName 按发布约定拼出资产文件名：flare_<goos>_<goarch>.tar.gz。
// windows 用 .zip（Windows 用户不需要额外装解压工具）。
// 用白名单而不是"什么平台都拼一个名字"：拼得出来、下载却 404 的错误，
// 用户很难看懂；这里直接说"这个平台没有发布包"
func assetName(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64":
		return fmt.Sprintf("flare_%s_%s.tar.gz", goos, goarch), nil
	case "windows/amd64", "windows/arm64":
		return fmt.Sprintf("flare_%s_%s.zip", goos, goarch), nil
	default:
		return "", fmt.Errorf("flare 不支持的平台: %s/%s", goos, goarch)
	}
}

// binaryEntry 是发布包内可执行文件的名字
func binaryEntry(goos string) string {
	if goos == "windows" {
		return "flare.exe"
	}
	return "flare"
}

// assetURL 拼出发布资产的下载地址（GitHub Releases 的地址格式是固定的）
func assetURL(tag, asset string) string {
	return "https://github.com/" + repo + "/releases/download/" + tag + "/" + asset
}

// IsNewer 判断 latest 是否比 current 新。
// current 为 dev（源码直接编译、版本号没注入）时，任何正式发布都算新 ——
// 否则开发者永远收不到更新提示
func IsNewer(current, latest string) bool {
	if strings.TrimSpace(latest) == "" {
		return false
	}
	return compareVersion(latest, current) > 0
}

// compareVersion 比较两个版本号：a<b 返回 -1，相等返回 0，a>b 返回 1。
// 规则取 semver 的常用部分，够用即可：
//   - 前缀 v 可有可无（GitHub 的 tag 习惯带 v，编译期注入的通常不带）
//   - 逐段按数字比："1.10.0" 大于 "1.9.0"（按字符串比会判反）
//   - 段数不齐时缺的补 0："1.2" 等于 "1.2.0"
//   - 带预发布后缀（-rc1）的比同版本正式版小；两个预发布版按后缀字符串比
//   - dev / unknown / 空 这类不是版本号的值一律算最小
func compareVersion(a, b string) int {
	an, apre := splitVersion(a)
	bn, bpre := splitVersion(b)
	if an == nil || bn == nil {
		switch {
		case an == nil && bn == nil:
			return 0
		case an == nil:
			return -1
		default:
			return 1
		}
	}

	for i := 0; i < len(an) || i < len(bn); i++ {
		av, bv := 0, 0
		if i < len(an) {
			av = an[i]
		}
		if i < len(bn) {
			bv = bn[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	// 数字部分相同，看预发布后缀：没有后缀的才是正式版，正式版更大
	switch {
	case apre == bpre:
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	case apre < bpre:
		return -1
	default:
		return 1
	}
}

// splitVersion 拆出版本的数字段和预发布后缀；不像版本号时返回 nil
func splitVersion(v string) (nums []int, pre string) {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	if s == "" {
		return nil, ""
	}
	// 1.2.3-rc1 / 1.2.3+build7：后缀单独留下
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		pre, s = s[i+1:], s[:i]
	}
	if s == "" {
		return nil, ""
	}
	parts := strings.Split(s, ".")
	nums = make([]int, 0, len(parts))
	for _, p := range parts {
		// 任一段不是纯数字（dev、unknown、1.2.x）就当作"不是版本号"，
		// 不要在比较里猜大小 —— 猜错会把旧版本当成新版本让人"降级"
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, ""
		}
		nums = append(nums, n)
	}
	return nums, pre
}

// replaceSelf 用 newBin 替换 exe。
// 成功时返回需要用户手动清理的残留文件（只有 Windows 会有），否则返回空串。
func replaceSelf(exe, newBin string) (string, error) {
	err := os.Rename(newBin, exe)
	if err == nil {
		return "", nil
	}
	if runtime.GOOS == "windows" {
		if leftover, werr := swapOnWindows(exe, newBin); werr == nil {
			return leftover, nil
		}
	}
	// 替换不成，把下载好的新版本留在显眼的位置：用户按错误信息里的路径
	// 手动覆盖即可，不用再下载一遍
	keep := exe + ".new"
	if e := os.Rename(newBin, keep); e != nil {
		keep = newBin
	}
	return "", fmt.Errorf("替换 %s 失败: %w\n新版本已放在 %s，关闭所有 flare 进程后手动替换即可", exe, err, keep)
}

// swapOnWindows 处理"运行中的 exe 不能被覆盖"。
// Windows 允许给运行中的 exe 改名（映像被映射着，删不掉、但可以改名），
// 于是先把它挪成 .old，再把新文件放到原名字上
func swapOnWindows(exe, newBin string) (string, error) {
	old := exe + ".old"
	os.Remove(old) // 上次更新留下的，先清掉；删不掉也不影响下面的改名
	if err := os.Rename(exe, old); err != nil {
		return "", err
	}
	if err := os.Rename(newBin, exe); err != nil {
		// 旧的必须放回原位，否则用户连 flare 都没得用
		os.Rename(old, exe)
		return "", err
	}
	return old, nil
}
