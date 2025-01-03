package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/golang/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	_ "github.com/v2fly/v2ray-core/v5/app/proxyman/inbound"
	_ "github.com/v2fly/v2ray-core/v5/app/proxyman/outbound"

	core "github.com/v2fly/v2ray-core/v5"
	vlog "github.com/v2fly/v2ray-core/v5/app/log"
	clog "github.com/v2fly/v2ray-core/v5/common/log"

	"github.com/v2fly/v2ray-core/v5/app/dispatcher"
	"github.com/v2fly/v2ray-core/v5/app/proxyman"
	"github.com/v2fly/v2ray-core/v5/common/net"
	"github.com/v2fly/v2ray-core/v5/common/protocol"
	"github.com/v2fly/v2ray-core/v5/common/serial"
	"github.com/v2fly/v2ray-core/v5/proxy/dokodemo"
	"github.com/v2fly/v2ray-core/v5/proxy/freedom"
	"github.com/v2fly/v2ray-core/v5/transport/internet"
	"github.com/v2fly/v2ray-core/v5/transport/internet/websocket"
)

var (
	VERSION = "custom"

	localAddr  = flag.String("localAddr", "127.0.0.1", "local address to listen on.")
	localPort  = flag.String("localPort", "1984", "local port to listen on.")
	remoteAddr = flag.String("remoteAddr", "127.0.0.1", "remote address to forward.")
	remotePort = flag.String("remotePort", "1080", "remote port to forward.")
	path       = flag.String("path", "/", "URL path for websocket.")
	host       = flag.String("host", "cloudfront.com", "Hostname for server.")
	mode       = flag.String("mode", "websocket", "Transport mode: websocket, quic (enforced tls).")
	mux        = flag.Int("mux", 1, "Concurrent multiplexed connections (websocket client mode only).")
	version    = flag.Bool("version", false, "Show current version of v2ray-plugin")
)

func logConfig() *vlog.Config {
	config := &vlog.Config{
		Error:  &vlog.LogSpecification{Level: clog.Severity_Error, Type: vlog.LogType_Console},
		Access: &vlog.LogSpecification{Type: vlog.LogType_None},
	}
	config.Error.Level = clog.Severity_Error
	return config
}

func parseLocalAddr(localAddr string) []string {
	return strings.Split(localAddr, "|")
}

func generateConfig() (*core.Config, error) {
	lport, err := net.PortFromString(*localPort)
	if err != nil {
		return nil, newError("invalid localPort:", *localPort).Base(err)
	}
	rport, err := strconv.ParseUint(*remotePort, 10, 32)
	if err != nil {
		return nil, newError("invalid remotePort:", *remotePort).Base(err)
	}
	outboundProxy := serial.ToTypedMessage(&freedom.Config{
		DestinationOverride: &freedom.DestinationOverride{
			Server: &protocol.ServerEndpoint{
				Address: net.NewIPOrDomain(net.ParseAddress(*remoteAddr)),
				Port:    uint32(rport),
			},
		},
	})

	var transportSettings proto.Message
	var connectionReuse bool
	switch *mode {
	case "websocket":
		transportSettings = &websocket.Config{
			Path: *path,
			Header: []*websocket.Header{
				{Key: "Host", Value: *host},
			},
		}
		if *mux != 0 {
			connectionReuse = true
		}
	default:
		return nil, newError("unsupported mode:", *mode)
	}

	streamConfig := internet.StreamConfig{
		ProtocolName: *mode,
		TransportSettings: []*internet.TransportConfig{{
			ProtocolName: *mode,
			Settings:     serial.ToTypedMessage(transportSettings),
		}},
	}
	apps := []*anypb.Any{
		serial.ToTypedMessage(&dispatcher.Config{}),
		serial.ToTypedMessage(&proxyman.InboundConfig{}),
		serial.ToTypedMessage(&proxyman.OutboundConfig{}),
		serial.ToTypedMessage(logConfig()),
	}

	proxyAddress := net.LocalHostIP
	if connectionReuse {
		// This address is required when mux is used on client.
		// dokodemo is not aware of mux connections by itself.
		proxyAddress = net.ParseAddress("v1.mux.cool")
	}
	localAddrs := parseLocalAddr(*localAddr)
	inbounds := make([]*core.InboundHandlerConfig, len(localAddrs))

	for i := 0; i < len(localAddrs); i++ {
		inbounds[i] = &core.InboundHandlerConfig{
			ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
				PortRange:      net.SinglePortRange(lport),
				Listen:         net.NewIPOrDomain(net.ParseAddress(localAddrs[i])),
				StreamSettings: &streamConfig,
			}),
			ProxySettings: serial.ToTypedMessage(&dokodemo.Config{
				Address:  net.NewIPOrDomain(proxyAddress),
				Networks: []net.Network{net.Network_TCP},
			}),
		}
	}

	return &core.Config{
		Inbound: inbounds,
		Outbound: []*core.OutboundHandlerConfig{{
			ProxySettings: outboundProxy,
		}},
		App: apps,
	}, nil

}

func startV2Ray() (core.Server, error) {

	opts, err := parseEnv()

	if err == nil {
		if c, b := opts.Get("mode"); b {
			*mode = c
		}
		if c, b := opts.Get("mux"); b {
			if i, err := strconv.Atoi(c); err == nil {
				*mux = i
			} else {
				logWarn("failed to parse mux, use default value")
			}
		}

		if c, b := opts.Get("host"); b {
			*host = c
		}
		if c, b := opts.Get("path"); b {
			*path = c
		}

		if c, b := opts.Get("localAddr"); b {
			*remoteAddr = c
		}
		if c, b := opts.Get("localPort"); b {
			*remotePort = c
		}
		if c, b := opts.Get("remoteAddr"); b {
			*localAddr = c
		}
		if c, b := opts.Get("remotePort"); b {
			*localPort = c
		}

	}

	config, err := generateConfig()
	fmt.Println(config)
	if err != nil {
		return nil, newError("failed to parse config").Base(err)
	}
	instance, err := core.New(config)
	if err != nil {
		return nil, newError("failed to create v2ray instance").Base(err)
	}
	return instance, nil
}

func printVersion() {
	fmt.Println("v2ray-plugin", VERSION)
	fmt.Println("Go version", runtime.Version())
	fmt.Println("Yet another SIP003 plugin for shadowsocks")

	version := core.VersionStatement()
	for _, s := range version {
		logInfo(s)
	}
}

func main() {
	flag.Parse()

	if *version {
		printVersion()
		return
	}

	logInit()

	server, err := startV2Ray()
	if err != nil {
		logFatal(err.Error())
		// Configuration error. Exit with a special value to prevent systemd from restarting.
		os.Exit(23)
	}
	if err := server.Start(); err != nil {
		logFatal("failed to start server:", err.Error())
		os.Exit(1)
	}

	defer func() {
		err := server.Close()
		if err != nil {
			logWarn(err.Error())
		}
	}()

	{
		osSignals := make(chan os.Signal, 1)
		signal.Notify(osSignals, os.Interrupt, os.Kill, syscall.SIGTERM)
		<-osSignals
	}
}
