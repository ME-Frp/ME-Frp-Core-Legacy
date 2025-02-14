// Copyright 2017 fatedier, fatedier@gmail.com
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

package client

import (
	"context"
	"fmt"
	"io"
	"net"
	"runtime/debug"
	"strings"
	"time"

	"github.com/fatedier/golib/control/shutdown"
	"github.com/fatedier/golib/crypto"

	"github.com/fatedier/frp/client/proxy"
	"github.com/fatedier/frp/client/visitor"
	"github.com/fatedier/frp/pkg/auth"
	"github.com/fatedier/frp/pkg/config"
	"github.com/fatedier/frp/pkg/msg"
	"github.com/fatedier/frp/pkg/transport"
	"github.com/fatedier/frp/pkg/util/xlog"
)

type Control struct {
	// service context
	ctx context.Context
	xl  *xlog.Logger

	// Unique ID obtained from frps.
	// It should be attached to the login message when reconnecting.
	runID string

	// manage all proxies
	pxyCfgs map[string]config.ProxyConf
	pm      *proxy.Manager

	// manage all visitors
	vm *visitor.Manager

	// control connection
	conn net.Conn

	cm *ConnectionManager

	// put a message in this channel to send it over control connection to server
	sendCh chan (msg.Message)

	// read from this channel to get the next message sent by server
	readCh chan (msg.Message)

	// goroutines can block by reading from this channel, it will be closed only in reader() when control connection is closed
	closedCh chan struct{}

	closedDoneCh chan struct{}

	// last time got the Pong message
	lastPong time.Time

	// The client configuration
	clientCfg config.ClientCommonConf

	readerShutdown     *shutdown.Shutdown
	writerShutdown     *shutdown.Shutdown
	msgHandlerShutdown *shutdown.Shutdown

	// sets authentication based on selected method
	authSetter auth.Setter

	msgTransporter transport.MessageTransporter
}

func NewControl(
	ctx context.Context, runID string, conn net.Conn, cm *ConnectionManager,
	clientCfg config.ClientCommonConf,
	pxyCfgs map[string]config.ProxyConf,
	visitorCfgs map[string]config.VisitorConf,
	authSetter auth.Setter,
) *Control {
	// new xlog instance
	ctl := &Control{
		ctx:                ctx,
		xl:                 xlog.FromContextSafe(ctx),
		runID:              runID,
		conn:               conn,
		cm:                 cm,
		pxyCfgs:            pxyCfgs,
		sendCh:             make(chan msg.Message, 100),
		readCh:             make(chan msg.Message, 100),
		closedCh:           make(chan struct{}),
		closedDoneCh:       make(chan struct{}),
		clientCfg:          clientCfg,
		readerShutdown:     shutdown.New(),
		writerShutdown:     shutdown.New(),
		msgHandlerShutdown: shutdown.New(),
		authSetter:         authSetter,
	}
	ctl.msgTransporter = transport.NewMessageTransporter(ctl.sendCh)
	ctl.pm = proxy.NewManager(ctl.ctx, clientCfg, ctl.msgTransporter)

	ctl.vm = visitor.NewManager(ctl.ctx, ctl.runID, ctl.clientCfg, ctl.connectServer, ctl.msgTransporter)
	ctl.vm.Reload(visitorCfgs)
	return ctl
}

func (ctl *Control) Run() {
	go ctl.worker()

	// start all proxies
	ctl.pm.Reload(ctl.pxyCfgs)

	// start all visitors
	go ctl.vm.Run()
}

func (ctl *Control) HandleReqWorkConn(_ *msg.ReqWorkConn) {
	xl := ctl.xl
	workConn, err := ctl.connectServer()
	if err != nil {
		xl.Warn("启动新连接到服务器失败: %v", err)
		return
	}

	m := &msg.NewWorkConn{
		RunID: ctl.runID,
	}
	if err = ctl.authSetter.SetNewWorkConn(m); err != nil {
		xl.Warn("NewWorkConn 认证失败: %v", err)
		return
	}
	if err = msg.WriteMsg(workConn, m); err != nil {
		xl.Warn("工作连接写入服务器失败: %v", err)
		workConn.Close()
		return
	}

	var startMsg msg.StartWorkConn
	if err = msg.ReadMsgInto(workConn, &startMsg); err != nil {
		xl.Trace("工作连接在响应 StartWorkConn 消息之前关闭: %v", err)
		workConn.Close()
		return
	}
	if startMsg.Error != "" {
		xl.Error("StartWorkConn 包含错误: %s", startMsg.Error)
		workConn.Close()
		return
	}

	// dispatch this work connection to related proxy
	ctl.pm.HandleWorkConn(startMsg.ProxyName, workConn, &startMsg)
}

func (ctl *Control) HandleNewProxyResp(inMsg *msg.NewProxyResp) {
	xl := ctl.xl
	// Server will return NewProxyResp message to each NewProxy message.
	// Start a new proxy handler if no error got
	err := ctl.pm.StartProxy(inMsg.ProxyName, inMsg.RemoteAddr, inMsg.Error)
	if err != nil {
		xl.Warn("启动隧道 [%s] 失败: %v", inMsg.ProxyName, err)
	} else {
		cfg, ok := ctl.pxyCfgs[inMsg.ProxyName]
		if !ok {
			xl.Warn("内部错误：隧道 [%s] 未找到, 您可以继续使用本隧道", inMsg.ProxyName)
		}
		proxyAddr := inMsg.RemoteAddr
		// 如果不是 http/https 类型，需要加上服务器地址
		if cfg.GetBaseConfig().ProxyType != "http" && cfg.GetBaseConfig().ProxyType != "https" {
			proxyAddr = fmt.Sprintf("%s%s", ctl.clientCfg.ServerAddr, inMsg.RemoteAddr)
		} else {
			// 对于 HTTP/HTTPS 类型，显示所有域名
			var domains []string
			switch cfg.GetBaseConfig().ProxyType {
			case "http":
				if httpCfg, ok := cfg.(*config.HTTPProxyConf); ok {
					domains = httpCfg.CustomDomains
				}
			case "https":
				if httpsCfg, ok := cfg.(*config.HTTPSProxyConf); ok {
					domains = httpsCfg.CustomDomains
				}
			}
			if len(domains) > 0 {
				var addrs []string
				for _, domain := range domains {
					addrs = append(addrs, fmt.Sprintf("%s://%s", cfg.GetBaseConfig().ProxyType, domain))
				}
				proxyAddr = strings.Join(addrs, " 或 ")
			}
		}
		xl.Info("启动 [%s] 隧道 [%s] 成功, 您可以使用 [%s] 访问您的服务",
			cfg.GetBaseConfig().ProxyType,
			inMsg.ProxyName,
			proxyAddr)
	}
}

func (ctl *Control) HandleNatHoleResp(inMsg *msg.NatHoleResp) {
	xl := ctl.xl

	// Dispatch the NatHoleResp message to the related proxy.
	ok := ctl.msgTransporter.DispatchWithType(inMsg, msg.TypeNameNatHoleResp, inMsg.TransactionID)
	if !ok {
		xl.Trace("分发 NatHoleResp 消息到相关隧道错误")
	}
}

func (ctl *Control) handleGetProxyBandwidthLimitResp(m *msg.GetProxyBandwidthLimitResp) {
	xl := ctl.xl
	xl.Info("隧道 [%s] 带宽限制: %d Mbps ↑ , %d Mbps ↓", m.ProxyName, m.OutBound, m.InBound)
	cfg, ok := ctl.pxyCfgs[m.ProxyName]
	if !ok {
		xl.Warn("内部错误：隧道 [%s] 未找到, 您可以继续使用", m.ProxyName)
		return
	}

	var limit int64
	if m.InBound > 0 {
		limit = m.InBound
	} else {
		limit = m.OutBound
	}
	baseCfg := cfg.GetBaseConfig()
	_ = baseCfg.BandwidthLimit.UnmarshalString(fmt.Sprintf("%dMB", limit))
}

func (ctl *Control) Close() error {
	return ctl.GracefulClose(0)
}

func (ctl *Control) GracefulClose(d time.Duration) error {
	ctl.pm.Close()
	ctl.vm.Close()

	time.Sleep(d)

	ctl.conn.Close()
	ctl.cm.Close()
	return nil
}

// ClosedDoneCh returns a channel that will be closed after all resources are released
func (ctl *Control) ClosedDoneCh() <-chan struct{} {
	return ctl.closedDoneCh
}

// connectServer return a new connection to frps
func (ctl *Control) connectServer() (conn net.Conn, err error) {
	return ctl.cm.Connect()
}

// reader read all messages from frps and send to readCh
func (ctl *Control) reader() {
	xl := ctl.xl
	defer func() {
		if err := recover(); err != nil {
			xl.Error("panic error: %v", err)
			xl.Error(string(debug.Stack()))
		}
	}()
	defer ctl.readerShutdown.Done()
	defer close(ctl.closedCh)

	encReader := crypto.NewReader(ctl.conn, []byte(ctl.clientCfg.Token))
	for {
		m, err := msg.ReadMsg(encReader)
		if err != nil {
			if err == io.EOF {
				xl.Debug("控制连接读取到 EOF")
				return
			}
			xl.Warn("读取失败: %v", err)
			ctl.conn.Close()
			return
		}
		ctl.readCh <- m
	}
}

// writer writes messages got from sendCh to frps
func (ctl *Control) writer() {
	xl := ctl.xl
	defer ctl.writerShutdown.Done()
	encWriter, err := crypto.NewWriter(ctl.conn, []byte(ctl.clientCfg.Token))
	if err != nil {
		xl.Error("加密新的写入器失败: %v", err)
		ctl.conn.Close()
		return
	}
	for {
		m, ok := <-ctl.sendCh
		if !ok {
			xl.Info("控制写入器正在关闭")
			return
		}

		if err := msg.WriteMsg(encWriter, m); err != nil {
			xl.Warn("向控制连接写入消息失败: %v", err)
			return
		}
	}
}

// msgHandler handles all channel events and performs corresponding operations.
func (ctl *Control) msgHandler() {
	xl := ctl.xl
	defer func() {
		if err := recover(); err != nil {
			xl.Error("panic error: %v", err)
			xl.Error(string(debug.Stack()))
		}
	}()
	defer ctl.msgHandlerShutdown.Done()

	var hbSendCh <-chan time.Time
	// TODO(fatedier): disable heartbeat if TCPMux is enabled.
	// Just keep it here to keep compatible with old version frps.
	if ctl.clientCfg.HeartbeatInterval > 0 {
		hbSend := time.NewTicker(time.Duration(ctl.clientCfg.HeartbeatInterval) * time.Second)
		defer hbSend.Stop()
		hbSendCh = hbSend.C
	}

	var hbCheckCh <-chan time.Time
	// Check heartbeat timeout only if TCPMux is not enabled and users don't disable heartbeat feature.
	if ctl.clientCfg.HeartbeatInterval > 0 && ctl.clientCfg.HeartbeatTimeout > 0 && !ctl.clientCfg.TCPMux {
		hbCheck := time.NewTicker(time.Second)
		defer hbCheck.Stop()
		hbCheckCh = hbCheck.C
	}

	ctl.lastPong = time.Now()
	for {
		select {
		case <-hbSendCh:
			// send heartbeat to server
			xl.Debug("发送心跳到服务器")
			pingMsg := &msg.Ping{}
			if err := ctl.authSetter.SetPing(pingMsg); err != nil {
				xl.Warn("Ping 认证失败: %v", err)
				return
			}
			ctl.sendCh <- pingMsg
		case <-hbCheckCh:
			if time.Since(ctl.lastPong) > time.Duration(ctl.clientCfg.HeartbeatTimeout)*time.Second {
				xl.Warn("心跳超时")
				// let reader() stop
				ctl.conn.Close()
				return
			}
		case rawMsg, ok := <-ctl.readCh:
			if !ok {
				return
			}

			switch m := rawMsg.(type) {
			case *msg.ReqWorkConn:
				go ctl.HandleReqWorkConn(m)
			case *msg.NewProxyResp:
				ctl.HandleNewProxyResp(m)
			case *msg.NatHoleResp:
				ctl.HandleNatHoleResp(m)
			case *msg.GetProxyBandwidthLimitResp:
				ctl.handleGetProxyBandwidthLimitResp(m)
			case *msg.Pong:
				if m.Error != "" {
					xl.Error("Pong 包含错误: %s", m.Error)
					ctl.conn.Close()
					return
				}
				ctl.lastPong = time.Now()
				xl.Debug("收到服务器心跳")
			}
		}
	}
}

// If controler is notified by closedCh, reader and writer and handler will exit
func (ctl *Control) worker() {
	go ctl.msgHandler()
	go ctl.reader()
	go ctl.writer()

	<-ctl.closedCh
	// close related channels and wait until other goroutines done
	close(ctl.readCh)
	ctl.readerShutdown.WaitDone()
	ctl.msgHandlerShutdown.WaitDone()

	close(ctl.sendCh)
	ctl.writerShutdown.WaitDone()

	ctl.pm.Close()
	ctl.vm.Close()

	close(ctl.closedDoneCh)
	ctl.cm.Close()
}

func (ctl *Control) ReloadConf(pxyCfgs map[string]config.ProxyConf, visitorCfgs map[string]config.VisitorConf) error {
	ctl.vm.Reload(visitorCfgs)
	ctl.pm.Reload(pxyCfgs)
	return nil
}
