// Copyright 2018 fatedier, fatedier@gmail.com
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sub

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fatedier/frp/pkg/config"
	"github.com/fatedier/frp/pkg/consts"
)

func init() {
	RegisterCommonFlags(httpCmd)

	httpCmd.PersistentFlags().StringVarP(&proxyName, "proxy_name", "n", "", "隧道名称")
	httpCmd.PersistentFlags().StringVarP(&localIP, "local_ip", "i", "127.0.0.1", "本地 IP")
	httpCmd.PersistentFlags().IntVarP(&localPort, "local_port", "l", 0, "本地端口")
	httpCmd.PersistentFlags().StringVarP(&customDomains, "custom_domain", "d", "", "自定义域名")
	httpCmd.PersistentFlags().StringVarP(&subDomain, "sd", "", "", "子域名")
	httpCmd.PersistentFlags().StringVarP(&locations, "locations", "", "", "Locations")
	httpCmd.PersistentFlags().StringVarP(&httpUser, "http_user", "", "", "HTTP 认证用户")
	httpCmd.PersistentFlags().StringVarP(&httpPwd, "http_pwd", "", "", "HTTP 认证密码")
	httpCmd.PersistentFlags().StringVarP(&hostHeaderRewrite, "host_header_rewrite", "", "", "Host Header Rewrite")
	httpCmd.PersistentFlags().BoolVarP(&useEncryption, "ue", "", false, "启用加密")
	httpCmd.PersistentFlags().BoolVarP(&useCompression, "uc", "", false, "启用压缩")
	httpCmd.PersistentFlags().StringVarP(&bandwidthLimit, "bandwidth_limit", "", "", "带宽限制")
	httpCmd.PersistentFlags().StringVarP(&bandwidthLimitMode, "bandwidth_limit_mode", "", config.BandwidthLimitModeClient, "带宽限制模式")

	rootCmd.AddCommand(httpCmd)
}

var httpCmd = &cobra.Command{
	Use:   "http",
	Short: "启动 [http] 隧道",
	RunE: func(cmd *cobra.Command, args []string) error {
		clientCfg, err := parseClientCommonCfgFromCmd()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		cfg := &config.HTTPProxyConf{}
		var prefix string
		if user != "" {
			prefix = user + "."
		}
		cfg.ProxyName = prefix + proxyName
		cfg.ProxyType = consts.HTTPProxy
		cfg.LocalIP = localIP
		cfg.LocalPort = localPort
		cfg.CustomDomains = strings.Split(customDomains, ",")
		cfg.SubDomain = subDomain
		cfg.Locations = strings.Split(locations, ",")
		cfg.HTTPUser = httpUser
		cfg.HTTPPwd = httpPwd
		cfg.HostHeaderRewrite = hostHeaderRewrite
		cfg.UseEncryption = useEncryption
		cfg.UseCompression = useCompression
		cfg.BandwidthLimit, err = config.NewBandwidthQuantity(bandwidthLimit)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		cfg.BandwidthLimitMode = bandwidthLimitMode

		err = cfg.ValidateForClient()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		proxyConfs := map[string]config.ProxyConf{
			cfg.ProxyName: cfg,
		}
		err = startService(clientCfg, proxyConfs, nil, "")
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		return nil
	},
}
