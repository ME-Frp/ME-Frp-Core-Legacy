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

	"github.com/spf13/cobra"

	"github.com/fatedier/frp/pkg/config"
	"github.com/fatedier/frp/pkg/consts"
)

func init() {
	RegisterCommonFlags(xtcpCmd)

	xtcpCmd.PersistentFlags().StringVarP(&proxyName, "proxy_name", "n", "", "隧道名称")
	xtcpCmd.PersistentFlags().StringVarP(&role, "role", "", "server", "角色")
	xtcpCmd.PersistentFlags().StringVarP(&sk, "sk", "", "", "密钥")
	xtcpCmd.PersistentFlags().StringVarP(&serverName, "server_name", "", "", "服务器名称")
	xtcpCmd.PersistentFlags().StringVarP(&localIP, "local_ip", "i", "127.0.0.1", "本地 IP")
	xtcpCmd.PersistentFlags().IntVarP(&localPort, "local_port", "l", 0, "本地端口")
	xtcpCmd.PersistentFlags().StringVarP(&bindAddr, "bind_addr", "", "", "绑定地址")
	xtcpCmd.PersistentFlags().IntVarP(&bindPort, "bind_port", "", 0, "绑定端口")
	xtcpCmd.PersistentFlags().BoolVarP(&useEncryption, "ue", "", false, "启用加密")
	xtcpCmd.PersistentFlags().BoolVarP(&useCompression, "uc", "", false, "启用压缩")
	xtcpCmd.PersistentFlags().StringVarP(&bandwidthLimit, "bandwidth_limit", "", "", "带宽限制")
	xtcpCmd.PersistentFlags().StringVarP(&bandwidthLimitMode, "bandwidth_limit_mode", "", config.BandwidthLimitModeClient, "带宽限制模式")

	rootCmd.AddCommand(xtcpCmd)
}

var xtcpCmd = &cobra.Command{
	Use:   "xtcp",
	Short: "启动 [xtcp] 隧道",
	RunE: func(cmd *cobra.Command, args []string) error {
		clientCfg, err := parseClientCommonCfgFromCmd()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		proxyConfs := make(map[string]config.ProxyConf)
		visitorConfs := make(map[string]config.VisitorConf)

		var prefix string
		if user != "" {
			prefix = user + "."
		}

		switch role {
		case "server":
			cfg := &config.XTCPProxyConf{}
			cfg.ProxyName = prefix + proxyName
			cfg.ProxyType = consts.XTCPProxy
			cfg.UseEncryption = useEncryption
			cfg.UseCompression = useCompression
			cfg.Role = role
			cfg.Sk = sk
			cfg.LocalIP = localIP
			cfg.LocalPort = localPort
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
			proxyConfs[cfg.ProxyName] = cfg
		case "visitor":
			cfg := &config.XTCPVisitorConf{}
			cfg.ProxyName = prefix + proxyName
			cfg.ProxyType = consts.XTCPProxy
			cfg.UseEncryption = useEncryption
			cfg.UseCompression = useCompression
			cfg.Role = role
			cfg.Sk = sk
			cfg.ServerName = serverName
			cfg.BindAddr = bindAddr
			cfg.BindPort = bindPort
			err = cfg.Validate()
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
			visitorConfs[cfg.ProxyName] = cfg
		default:
			fmt.Println("无效的角色")
			os.Exit(1)
		}

		err = startService(clientCfg, proxyConfs, visitorConfs, "")
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		return nil
	},
}
