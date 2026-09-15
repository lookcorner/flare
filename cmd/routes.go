package cmd

import (
	"flare/internal/cfapi"
	"flare/internal/config"
	"flare/internal/ops"
)

// pushIngress：把本地路由表全量翻译成云端 ingress 规则并推送。
// 实现已下沉到 internal/ops，CLI 与桌面 GUI 共用同一份
func pushIngress(client *cfapi.Client, cfg *config.Config) error {
	return ops.PushIngress(client, cfg)
}

func parseAuth(s string) (string, string, error) {
	return ops.ParseAuth(s)
}
