package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// normalizeProxyAddr 规范化代理地址。
// 支持格式：
//   - 127.0.0.1:8002                （无 scheme，默认 HTTP）
//   - http://host:port              （HTTP）
//   - http://user:pass@host:port    （HTTP + 认证）
//   - socks5://host:port            （SOCKS5）
//   - socks5://user:pass@host:port  （SOCKS5 + 认证）
//
// 返回带 scheme 的完整地址。
func normalizeProxyAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	// 已带 scheme 则原样返回
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") ||
		strings.HasPrefix(addr, "socks5://") || strings.HasPrefix(addr, "socks5h://") ||
		strings.HasPrefix(addr, "socks4://") {
		return addr
	}
	// 无 scheme -> 默认 HTTP
	return "http://" + addr
}

// buildTransport 根据代理地址构造带正确拨号方式的 http.Transport。
// addr 可为本机/外部 HTTP 或 SOCKS5，支持认证/无认证。
func buildTransport(addr string) (*http.Transport, error) {
	full := normalizeProxyAddr(addr)
	u, err := url.Parse(full)
	if err != nil {
		return nil, fmt.Errorf("parse proxy addr %q: %w", addr, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid proxy addr %q: missing host", addr)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // 我们自己控制拨号

	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme == "http" || scheme == "https":
		// HTTP(S) 代理: 认证信息在 URL 里, http.ProxyURL 自动处理
		transport.Proxy = http.ProxyURL(u)
	case scheme == "socks5" || scheme == "socks5h":
		// SOCKS5 代理: 支持认证
		var auth *proxy.Auth
		if u.User != nil {
			pass, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pass}
		}
		host := u.Host
		// socks5h 表示远端解析域名, 需要自定义
		dialer, err := proxy.SOCKS5("tcp", host, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("create socks5 dialer for %q: %w", addr, err)
		}
		if scheme == "socks5h" {
			// 远端解析域名
			dialer = &socks5hDialer{base: dialer, host: host, auth: auth}
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		}
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q in %q", u.Scheme, addr)
	}
	transport.DialContext = withTimeout(transport.DialContext)
	transport.ResponseHeaderTimeout = 15 * time.Second
	return transport, nil
}

// withTimeout 给拨号加超时
func withTimeout(dial func(ctx context.Context, network, addr string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return dial(dctx, network, addr)
	}
}

// socks5hDialer 实现 SOCKS5 远端域名解析 (socks5h)
type socks5hDialer struct {
	base proxy.Dialer
	host string
	auth *proxy.Auth
}

func (d *socks5hDialer) Dial(network, addr string) (net.Conn, error) {
	// 重新用 socks5h 逻辑: 把所有域名交给远端解析
	// 简化: 复用 base，但保持接口
	return d.base.Dial(network, addr)
}
