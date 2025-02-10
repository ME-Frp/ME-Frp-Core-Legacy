package sub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/auth"
	"github.com/fatedier/frp/pkg/config"
	"github.com/fatedier/frp/pkg/util/log"
	"github.com/fatedier/frp/pkg/util/version"
)

var (
	cfgFile          string
	cfgDir           string
	showVersion      bool
	strictConfigMode bool
	userToken        string
	proxyId          string

	serverAddr      string
	user            string
	protocol        string
	token           string
	logLevel        string
	logFile         string
	logMaxDays      int
	disableLogColor bool
	dnsServer       string

	proxyName          string
	localIP            string
	localPort          int
	remotePort         int
	useEncryption      bool
	useCompression     bool
	bandwidthLimit     string
	bandwidthLimitMode string
	customDomains      string
	subDomain          string
	httpUser           string
	httpPwd            string
	locations          string
	hostHeaderRewrite  string
	role               string
	sk                 string
	multiplexer        string
	serverName         string
	bindAddr           string
	bindPort           int

	tlsEnable     bool
	tlsServerName string
)

type ProxyConfigResp struct {
	ProxyId              int64  `json:"proxyId"`
	Username             string `json:"username"`
	ProxyName            string `json:"proxyName"`
	ProxyType            string `json:"proxyType"`
	IsBanned             bool   `json:"isBanned"`
	IsDisabled           bool   `json:"isDisabled"`
	LocalIp              string `json:"localIp"`
	LocalPort            int32  `json:"localPort"`
	RemotePort           int32  `json:"remotePort"`
	RunId                string `json:"runId"`
	IsOnline             bool   `json:"isOnline"`
	Domain               string `json:"domain"`
	LastStartTime        int64  `json:"lastStartTime"`
	LastCloseTime        int64  `json:"lastCloseTime"`
	ClientVersion        string `json:"clientVersion"`
	ProxyProtocolVersion string `json:"proxyProtocolVersion"`
	UseEncryption        bool   `json:"useEncryption"`
	UseCompression       bool   `json:"useCompression"`
	Location             string `json:"location"`
	AccessKey            string `json:"accessKey"`
	HostHeaderRewrite    string `json:"hostHeaderRewrite"`
	HeaderXFromWhere     string `json:"headerXFromWhere"`
	NodeAddr             string `json:"nodeAddr"`
	NodePort             int32  `json:"nodePort"`
	NodeToken            string `json:"nodeToken"`
}

type APIResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    ProxyConfigResp `json:"data"`
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "ME Frp 客户端配置文件")
	rootCmd.PersistentFlags().StringVarP(&cfgDir, "config_dir", "", "", "配置目录, 为每个配置文件运行一个 ME Frp 隧道")
	rootCmd.PersistentFlags().BoolVarP(&showVersion, "version", "v", false, "ME Frp 客户端版本")
	rootCmd.PersistentFlags().BoolVarP(&strictConfigMode, "strict_config", "", true, "严格配置解析模式, 未知字段将导致错误")
	rootCmd.PersistentFlags().StringVarP(&userToken, "token", "t", "", "快捷启动的用户 Token")
	rootCmd.PersistentFlags().StringVarP(&proxyId, "proxy", "p", "", "快捷启动的隧道 Id")
}

var rootCmd = &cobra.Command{
	Use:   "mefrpc",
	Short: "ME Frp 客户端",
	RunE: func(cmd *cobra.Command, args []string) error {
		if showVersion {
			fmt.Println(version.Full())
			return nil
		}

		if cfgFile != "" {
			err := runClient(cfgFile)
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
			return nil
		} else if userToken != "" && proxyId != "" {
			return runEasyStartup()
		} else if cfgDir != "" {
			return runMultipleClients(cfgDir)
		}

		return fmt.Errorf("请提供配置文件 (-c) 或快捷启动参数 (-t -p)")
	},
}

func runMultipleClients(cfgDir string) error {
	var wg sync.WaitGroup
	err := filepath.WalkDir(cfgDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		wg.Add(1)
		time.Sleep(time.Millisecond)
		go func() {
			defer wg.Done()
			err := runClient(path)
			if err != nil {
				fmt.Printf("ME Frp 服务错误, 配置文件: %s\n", path)
			}
		}()
		return nil
	})
	wg.Wait()
	return err
}

func runMultipleClientsEasyStart(userToken string, proxyId string) error {
	var wg sync.WaitGroup

	// 对于多个隧道，我们需要分割它们
	proxyIdList := strings.Split(proxyId, ",")
	for _, proxyId := range proxyIdList {
		wg.Add(1)
		go func(proxyId string) {
			defer wg.Done()
			err := runClient(proxyId)
			if err != nil {
				fmt.Printf("ME Frp 服务错误, 配置文件: %s\n", proxyId)
			}
		}(proxyId)
	}

	wg.Wait()
	return nil
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func handleTermSignal(svr *client.Service) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	svr.GracefulClose(500 * time.Millisecond)
}

func runClient(cfgFilePath string) error {
	var content string
	if cfgFilePath != "" {
		LocalContent, err := config.GetRenderedConfFromFile(cfgFilePath)
		if err != nil {
			return err
		}
		content = string(LocalContent)
	} else {
		return fmt.Errorf("配置文件路径不能为空")
	}

	cfg, pxyCfgs, visitorCfgs, err := config.ParseClientConfig(content)
	if err != nil {
		fmt.Println(err)
		return err
	}
	return startService(cfg, pxyCfgs, visitorCfgs, cfgFilePath)
}

func startService(
	cfg config.ClientCommonConf,
	pxyCfgs map[string]config.ProxyConf,
	visitorCfgs map[string]config.VisitorConf,
	cfgFile string,
) (err error) {
	log.InitLog(cfg.LogWay, cfg.LogFile, cfg.LogLevel,
		cfg.LogMaxDays, cfg.DisableLogColor)

	if cfgFile != "" {
		log.Info("开始运行 ME Frp 服务, 配置文件: %s", cfgFile)
		defer log.Info("ME Frp 服务已停止, 配置文件: %s", cfgFile)
	}
	svr, errRet := client.NewService(cfg, pxyCfgs, visitorCfgs, cfgFile)
	if errRet != nil {
		err = errRet
		return
	}

	// 创建一个 channel 用于接收系统信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// 创建一个 channel 用于等待服务结束
	doneCh := make(chan error, 1)

	// 在新的 goroutine 中运行服务
	go func() {
		if err := svr.Run(context.Background()); err != nil {
			doneCh <- err
		}
	}()

	// 等待信号或服务结束
	select {
	case <-sigCh:
		svr.GracefulClose(500 * time.Millisecond)
		return nil
	case err := <-doneCh:
		return err
	}
}

func runEasyStartup() error {
	if userToken == "" || proxyId == "" {
		return fmt.Errorf("使用快捷启动时, 用户Token 和 隧道Id 都是必需的")
	}

	proxyIds := strings.Split(proxyId, ",")
	var proxies []ProxyConfigResp

	for _, pid := range proxyIds {
		proxyConfig, err := fetchProxyConfig(pid, userToken)
		if err != nil {
			return fmt.Errorf("获取隧道 [%s] 配置失败: %v", pid, err)
		}
		proxies = append(proxies, proxyConfig)
	}

	if len(proxies) == 0 {
		return fmt.Errorf("没有获取到任何隧道配置")
	}

	cfg := config.GetDefaultClientConf()
	cfg.ServerAddr = proxies[0].NodeAddr
	cfg.ServerPort = int(proxies[0].NodePort)
	cfg.User = userToken
	cfg.Token = proxies[0].NodeToken
	cfg.LogLevel = "info"
	cfg.LogFile = "console"
	cfg.LogMaxDays = 3

	pxyCfgs := make(map[string]config.ProxyConf)
	for _, proxy := range proxies {
		pxyCfg := createProxyConfig(&proxy)
		if pxyCfg == nil {
			return fmt.Errorf("不支持的隧道类型: %s (支持的类型: tcp, udp, http, https)", proxy.ProxyType)
		}
		pxyCfgs[proxy.ProxyName] = pxyCfg
	}

	return startService(cfg, pxyCfgs, nil, "")
}

func createProxyConfig(proxy *ProxyConfigResp) config.ProxyConf {
	var pxyCfg config.ProxyConf

	switch proxy.ProxyType {
	case "tcp":
		cfg := &config.TCPProxyConf{}
		cfg.ProxyName = proxy.ProxyName
		cfg.ProxyType = "tcp"
		cfg.LocalIP = proxy.LocalIp
		cfg.LocalPort = int(proxy.LocalPort)
		cfg.RemotePort = int(proxy.RemotePort)
		cfg.UseEncryption = proxy.UseEncryption
		cfg.UseCompression = proxy.UseCompression
		pxyCfg = cfg
	case "udp":
		cfg := &config.UDPProxyConf{}
		cfg.ProxyName = proxy.ProxyName
		cfg.ProxyType = "udp"
		cfg.LocalIP = proxy.LocalIp
		cfg.LocalPort = int(proxy.LocalPort)
		cfg.RemotePort = int(proxy.RemotePort)
		pxyCfg = cfg
	case "http":
		cfg := &config.HTTPProxyConf{}
		cfg.ProxyName = proxy.ProxyName
		cfg.ProxyType = "http"
		cfg.LocalIP = proxy.LocalIp
		cfg.LocalPort = int(proxy.LocalPort)
		cfg.CustomDomains = []string{proxy.Domain}
		cfg.HostHeaderRewrite = proxy.HostHeaderRewrite
		cfg.Headers = map[string]string{
			"X-From-Where": proxy.HeaderXFromWhere,
		}
		cfg.UseEncryption = proxy.UseEncryption
		cfg.UseCompression = proxy.UseCompression
		pxyCfg = cfg
	case "https":
		cfg := &config.HTTPSProxyConf{}
		cfg.ProxyName = proxy.ProxyName
		cfg.ProxyType = "https"
		cfg.LocalIP = proxy.LocalIp
		cfg.LocalPort = int(proxy.LocalPort)
		cfg.CustomDomains = []string{proxy.Domain}
		cfg.UseEncryption = proxy.UseEncryption
		cfg.UseCompression = proxy.UseCompression
		pxyCfg = cfg
	default:
		return nil
	}

	return pxyCfg
}

func fetchProxyConfig(proxyId string, userToken string) (ProxyConfigResp, error) {
	url := "https://api.mefrp.com/api/auth/easyStartup"
	jsonBody := []byte(fmt.Sprintf(`{"proxyId": %s}`, proxyId))
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return ProxyConfigResp{}, fmt.Errorf("创建请求失败: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+userToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return ProxyConfigResp{}, fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	var proxyResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&proxyResp); err != nil {
		return ProxyConfigResp{}, fmt.Errorf("解析响应失败: %v", err)
	}

	if proxyResp.Code != 200 {
		return ProxyConfigResp{}, fmt.Errorf("API 错误: %s", proxyResp.Message)
	}

	if proxyResp.Data.IsBanned {
		return ProxyConfigResp{}, fmt.Errorf("隧道已被封禁, 请联系管理员")
	}

	if proxyResp.Data.IsDisabled {
		return ProxyConfigResp{}, fmt.Errorf("隧道已被禁用, 请在网页端启用")
	}

	if proxyResp.Data.ProxyType == "" {
		return ProxyConfigResp{}, fmt.Errorf("隧道类型为空, 请检查隧道是否存在")
	}

	if proxyResp.Data.NodeAddr == "" {
		return ProxyConfigResp{}, fmt.Errorf("API 返回的节点地址为空")
	}

	if proxyResp.Data.NodePort == 0 {
		return ProxyConfigResp{}, fmt.Errorf("API 返回的节点端口为空")
	}

	return proxyResp.Data, nil
}

func RegisterCommonFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVarP(&serverAddr, "server_addr", "s", "127.0.0.1:7000", "frp server's address")
	cmd.PersistentFlags().StringVarP(&user, "user", "u", "", "user")
	cmd.PersistentFlags().StringVarP(&protocol, "protocol", "p", "tcp", "tcp, kcp, quic, websocket, wss")
	cmd.PersistentFlags().StringVarP(&token, "token", "t", "", "auth token")
	cmd.PersistentFlags().StringVarP(&logLevel, "log_level", "", "info", "log level")
	cmd.PersistentFlags().StringVarP(&logFile, "log_file", "", "console", "console or file path")
	cmd.PersistentFlags().IntVarP(&logMaxDays, "log_max_days", "", 3, "log file reversed days")
	cmd.PersistentFlags().BoolVarP(&disableLogColor, "disable_log_color", "", false, "disable log color in console")
	cmd.PersistentFlags().BoolVarP(&tlsEnable, "tls_enable", "", true, "enable frpc tls")
	cmd.PersistentFlags().StringVarP(&tlsServerName, "tls_server_name", "", "", "specify the custom server name of tls certificate")
	cmd.PersistentFlags().StringVarP(&dnsServer, "dns_server", "", "", "specify dns server instead of using system default one")
}

func parseClientCommonCfgFromCmd() (cfg config.ClientCommonConf, err error) {
	cfg = config.GetDefaultClientConf()

	ipStr, portStr, err := net.SplitHostPort(serverAddr)
	if err != nil {
		err = fmt.Errorf("无效的 server_addr: %v", err)
		return
	}

	cfg.ServerAddr = ipStr
	cfg.ServerPort, err = strconv.Atoi(portStr)
	if err != nil {
		err = fmt.Errorf("无效的 server_addr: %v", err)
		return
	}

	cfg.User = user
	cfg.Protocol = protocol
	cfg.LogLevel = logLevel
	cfg.LogFile = logFile
	cfg.LogMaxDays = int64(logMaxDays)
	cfg.DisableLogColor = disableLogColor
	cfg.DNSServer = dnsServer

	// Only token authentication is supported in cmd mode
	cfg.ClientConfig = auth.GetDefaultClientConf()
	cfg.Token = token
	cfg.TLSEnable = tlsEnable
	cfg.TLSServerName = tlsServerName

	cfg.Complete()
	if err = cfg.Validate(); err != nil {
		err = fmt.Errorf("解析配置错误: %v", err)
		return
	}
	return
}
