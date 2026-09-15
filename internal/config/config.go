package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// yaml 标签是反引号字符串，告诉序列化器键名；omitempty = 值为零时省略
type Config struct {
	Version int           `yaml:"version"`
	Auth    AuthConfig    `yaml:"auth"`
	Tunnel  TunnelConfig  `yaml:"tunnel"`
	Routes  []RouteConfig `yaml:"routes"`
	Relay   RelayConfig   `yaml:"relay,omitempty"`
}

// RelayConfig：中继模式配置。Server 形如 "1.2.3.4:7000"（含端口！）
type RelayConfig struct {
	Server string      `yaml:"server,omitempty"`
	Token  string      `yaml:"token,omitempty"`
	Rules  []RelayRule `yaml:"rules,omitempty"`
}

// RelayRule：一条穿透规则 = 把本机哪个端口，映射到服务器的哪个端口
type RelayRule struct {
	Name       string `yaml:"name"`
	Proto      string `yaml:"proto"`              // tcp / udp
	LocalIP    string `yaml:"local_ip,omitempty"` // 默认 127.0.0.1
	LocalPort  int    `yaml:"local_port"`
	RemotePort int    `yaml:"remote_port,omitempty"` // 缺省 = 与 LocalPort 相同
}
type AuthConfig struct {
	ApiToken  string `yaml:"api_token"`
	AccountID string `yaml:"account_id"`
}

type TunnelConfig struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Token string `yaml:"token"`
}

// RouteConfig 是路由表的一行：哪个域名 → 转发到本地哪个服务
type RouteConfig struct {
	Name        string     `yaml:"name"`          // 用户给这条路由起的名字（add 命令的 <名称>）
	Hostname    string     `yaml:"hostname"`      // 公网域名，如 web.example.com
	Service     string     `yaml:"service"`       // 本地地址，如 http://localhost:3000
	ZoneID      string     `yaml:"zone_id"`       // 域名所属 Zone 的 ID
	DNSRecordID string     `yaml:"dns_record_id"` // 云端 DNS 记录的 ID
	Auth        *AuthProxy `yaml:"auth,omitempty"`
}

// AuthProxy 存一份登录配置 + 签名密钥（hex 字符串）
type AuthProxy struct {
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	SigningKey string `yaml:"signing_key"`
	CookieTTL  int    `yaml:"cookie_ttl,omitempty"` // 秒；0 = 用默认 86400
}

// 目录只需要探测一次（进程生命周期内不变），用 sync.Once 缓存结果。
var (
	dirOnce     sync.Once
	dirPath     string
	isPortTable bool
)

// currentVersion 是当前配置格式版本。Load 会校验、Save 会写回。
// 将来改配置结构（改字段名/含义）时，在这里抬版本号，并写清怎么搬旧数据
const currentVersion = 1

// Load 读配置文件。文件不存在时返回"空配置"而不是报错——
// 这样第一次运行（还没 init）也能安全地读、改、存。
func Load() (*Config, error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) { // 区分"文件不存在"和"打不开"两种错误
			return &Config{Version: currentVersion}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	// 版本比本程序新：说明配置是被更新版本的 flare 写的，字段含义可能已经变了。
	// 硬读下去可能把新字段当垃圾丢掉 —— 宁可不干活，也不能写坏用户的配置
	if cfg.Version > currentVersion {
		return nil, fmt.Errorf("配置文件版本 %d 高于本程序支持的 %d，请升级 flare（或删除 %s 重新 init）",
			cfg.Version, currentVersion, Path())
	}
	// Version 为 0 = 早期版本写的文件（那时没写版本号），当作 v1 处理

	cfg.applyEnvOverrides() //  两条路径都过这里
	return &cfg, nil
}

// Save 把配置写回文件。权限 0600 = 只有本人可读写 ——
// 因为文件里会存 API Token，这是安全底线。
func Save(cfg *Config) error {
	if cfg.Version == 0 {
		cfg.Version = currentVersion // 老配置升级：写回时把版本号补上
	}
	if err := os.MkdirAll(Dir(), 0700); err != nil {
		return err
	}
	//data 存放的是 YAML 格式的序列化文本内容[115 101 ....]
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), data, 0600)
}

// dir 返回数据目录。任何包需要"文件放那"都问它
func Dir() string {

	dirOnce.Do(func() { // Do 保证函数只执行一次，天然线程安全
		// 环境变量优先级最高：测试、多环境并存（如放到 U 盘/容器卷）都用它指定目录
		if v := os.Getenv("FLARE_DIR"); v != "" {
			dirPath = v
			return
		}
		exe, err := os.Executable()
		if err == nil {
			// EvalSymlinks：如果程序是软链（如 /usr/local/bin 下常见的链接），
			// 先解析出真实路径，否则 dir 会落在链接所在目录
			if real, err := filepath.EvalSymlinks(exe); err == nil {
				exe = real
			}
			exeDir := filepath.Dir(exe)
			// 便携模式：exe 同级目录存在 portTable 文件时，数据才放 exe 旁边；
			// 否则默认落 ~/.flare（与 cftunnel 的 portable 语义一致）
			if _, err := os.Stat(filepath.Join(exeDir, "portTable")); err == nil {
				dirPath = exeDir
				isPortTable = true
				return
			}

		}

		home, _ := os.UserHomeDir() //获取用户
		dirPath = filepath.Join(home, ".flare")
	})
	return dirPath
}

// PortTable 返回是否便携式(system 命令启动时会提示用户数据放哪里了)
func PortTable() bool {
	Dir() // 保证once 已经跑过（once 内才设置isPortTable）
	return isPortTable
}

// Path 返回配置文件完整路径
func Path() string {
	return filepath.Join(Dir(), "config.yml")
}

// FindRoute 按名字找路由；找不到返回 nil
func (cfg *Config) FindRoute(name string) *RouteConfig {
	for i := range cfg.Routes { //range 取索引，避免拷贝结构体
		if cfg.Routes[i].Name == name {
			return &cfg.Routes[i] //返回指针：调用方可直接改它
		}
	}
	return nil
}

// RemoveRoute 按名字删除路由
func (cfg *Config) RemoveRoute(name string) bool {
	for i, r := range cfg.Routes {
		if r.Name == name {
			// 删除切片的第 i 个元素：后半段整体左移一位
			// 原数组: [ A  B  C  D ]
			//   0  1  2  3
			//   └┬┘     └──┬──┘
			// 前段[:1]   后段[2:]
			//   [A]  +  [C, D]  →  append  →  [A, C, D]
			cfg.Routes = append(cfg.Routes[:i], cfg.Routes[i+1:]...)
			return true
		}
	}
	return false
}

// applyEnvOverrides：环境变量优先级最高。在 Load 的末尾调用。
func (cfg *Config) applyEnvOverrides() {
	if v := os.Getenv("FLARE_API_TOKEN"); v != "" {
		cfg.Auth.ApiToken = v
	}
	// 账户 ID 两个变量名都认：新名字优先，旧名字留着免得已有脚本失效
	if v := os.Getenv("FLARE_ACCOUNT_ID"); v != "" {
		cfg.Auth.AccountID = v
	} else if v := os.Getenv("CFTUNNEL_ACCOUNT_ID"); v != "" {
		cfg.Auth.AccountID = v
	}
}
func (a *AuthProxy) CookieTTLOrDefault() int {
	if a.CookieTTL > 0 {
		return a.CookieTTL
	}
	return 86400
}

// FindRelayRule / RemoveRelayRule：与 FindRoute 完全相同
func (c *Config) FindRelayRule(name string) *RelayRule {
	for i := range c.Relay.Rules {
		if c.Relay.Rules[i].Name == name {
			return &c.Relay.Rules[i]
		}
	}
	return nil
}
func (c *Config) RemoveRelayRule(name string) bool {
	for i, r := range c.Relay.Rules {
		if r.Name == name {
			c.Relay.Rules = append(c.Relay.Rules[:i], c.Relay.Rules[i+1:]...)
			return true
		}
	}
	return false
}
