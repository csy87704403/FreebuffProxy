package main

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeProxyAddr(t *testing.T) {
	cases := []struct{ in, want string }{
		{"127.0.0.1:8002", "http://127.0.0.1:8002"},
		{"http://1.2.3.4:8080", "http://1.2.3.4:8080"},
		{"http://user:pass@1.2.3.4:8080", "http://user:pass@1.2.3.4:8080"},
		{"socks5://1.2.3.4:1080", "socks5://1.2.3.4:1080"},
		{"socks5://user:pass@1.2.3.4:1080", "socks5://user:pass@1.2.3.4:1080"},
	}
	for _, c := range cases {
		got := normalizeProxyAddr(c.in)
		if got != c.want {
			t.Errorf("normalize(%q) = %q, want %q", c.in, got, c.want)
		}
		fmt.Printf("  OK: %q -> %q\n", c.in, got)
	}
}

func TestBuildTransportAuth(t *testing.T) {
	// 验证能解析出认证信息 (不实际连接)
	cases := []string{
		"http://user:pass@1.2.3.4:8080",
		"socks5://user:pass@1.2.3.4:1080",
		"socks5://1.2.3.4:1080",
		"http://1.2.3.4:8080",
	}
	for _, c := range cases {
		tr, err := buildTransport(c)
		if err != nil {
			t.Errorf("buildTransport(%q) err: %v", c, err)
			continue
		}
		if tr == nil {
			t.Errorf("buildTransport(%q): nil transport", c)
		}
		u, _ := url.Parse(normalizeProxyAddr(c))
		auth := ""
		if u.User != nil {
			auth = u.User.String()
		}
		fmt.Printf("  OK: %q scheme=%s auth=%s\n", c, strings.Split(u.Scheme, ":")[0], auth)
	}
}
