//go:build !windows

package cmd

// checkWindowsVersion 非 Windows 平台空实现（只为让 Execute 里那行调用三平台都能编译）
func checkWindowsVersion() {}
