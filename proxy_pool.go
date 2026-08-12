package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ProxyEntry 单个出口代理状态
type ProxyEntry struct {
	Addr        string    `json:"addr"`            // 127.0.0.1:8002
	Mode        string    `json:"mode"`            // full / limited / unknown
	Detail      string    `json:"detail,omitempty"` // 细分原因: country_blocked / auth_failed / timeout / offline / unknown
	LatencyMs   int64     `json:"latency_ms"`
	Alive       bool      `json:"alive"`
	LastChecked time.Time `json:"last_checked"`
	LastOK      time.Time `json:"last_ok"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
	FailCount   int       `json:"fail_count"`
	InUse       bool      `json:"in_use,omitempty"`
}

// ProxyPool 出口代理池
type ProxyPool struct {
	mu      sync.Mutex
	entries []*ProxyEntry
	path    string
	client  *http.Client
	// 当前选中的出口
	current string
}

func NewProxyPool(path string) *ProxyPool {
	pool := &ProxyPool{
		entries: []*ProxyEntry{},
		path:    path,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
	pool.load()
	return pool
}

func (p *ProxyPool) load() {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return
	}
	var entries []*ProxyEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return
	}
	p.entries = entries
	// 关键修复: 加载后立即选出口, 否则 p.current 为空,
	// 下游 prewarm/session 创建会走直连(数据中心IP) → freebuff country_blocked 403
	p.current = ""
	for _, e := range p.entries {
		if e.Mode == "full" && e.Alive {
			p.current = e.Addr
			break
		}
	}
}

func (p *ProxyPool) save() {
	data, _ := json.MarshalIndent(p.entries, "", "  ")
	os.WriteFile(p.path, data, 0644)
}

// AddBatch 批量添加代理地址
func (p *ProxyPool) AddBatch(addrs string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	added := 0
	for _, line := range strings.Split(addrs, "\n") {
		raw := strings.TrimSpace(line)
		if raw == "" {
			continue
		}
		// 规范化: 支持 http:// / socks5:// / 无 scheme (默认 http)
		addr := normalizeProxyAddr(raw)
		if addr == "" {
			continue
		}
		// 校验合法性: 必须能解析出 host:port
		u, err := url.Parse(addr)
		if err != nil || u.Host == "" {
			continue
		}
		// 去重
		exists := false
		for _, e := range p.entries {
			if e.Addr == addr {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		p.entries = append(p.entries, &ProxyEntry{
			Addr: addr,
			Mode: "unknown",
		})
		added++
	}
	p.save()
	return added
}

// Remove 删除代理
func (p *ProxyPool) Remove(addr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, e := range p.entries {
		if e.Addr == addr {
			p.entries = append(p.entries[:i], p.entries[i+1:]...)
			p.save()
			return true
		}
	}
	return false
}
// Find 按地址查找代理条目
func (p *ProxyPool) Find(addr string) *ProxyEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		if e.Addr == addr {
			return e
		}
	}
	return nil
}

// ============ 检测 ============

// CheckProxy 检测单个代理的连通性和 FULL/LIMITED 模式
// authToken 非空时做模式检测，空时只测连通性
func (p *ProxyPool) CheckProxy(entry *ProxyEntry, authToken string) {
	start := time.Now()

	// 构造通过该代理的 HTTP client（支持 http/socks5 + 认证/无认证）
	transport, terr := buildTransport(entry.Addr)
	var client *http.Client
	if terr == nil {
		client = &http.Client{Transport: transport, Timeout: 15 * time.Second}
	} else {
		// transport 构建失败 -> 记录 detail, 回落直连（但标记不可用）
		client = &http.Client{Timeout: 15 * time.Second}
	}

	// 步骤1: 连通性检测 - 访问 freebuff.com
	alive := false
	offline := false
	connErr := ""
	resp, err := client.Get("https://www.codebuff.com/")
	if err != nil {
		offline = true
		connErr = err.Error()
	} else if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		alive = true
		resp.Body.Close()
	}

	latency := time.Since(start).Milliseconds()

	p.mu.Lock()
	entry.Alive = alive
	entry.LatencyMs = latency
	entry.LastChecked = time.Now()

	if !alive {
		entry.FailCount++
		if offline && (strings.Contains(connErr, "timeout") || strings.Contains(connErr, "context deadline") || strings.Contains(connErr, "connection refused") || strings.Contains(connErr, "no such host") || strings.Contains(connErr, "EOF")) {
			entry.Detail = "offline"
		} else if offline {
			entry.Detail = "timeout"
		} else {
			entry.Detail = "offline"
		}
		entry.Mode = "unknown"
		if entry.FailCount >= 3 {
			entry.CooldownUntil = time.Now().Add(30 * time.Minute)
		}
	} else {
		entry.FailCount = 0
		entry.LastOK = time.Now()
		// 步骤2: 模式检测（有 token 时）
		if authToken != "" {
			entry.Mode, entry.Detail = p.detectMode(client, authToken)
		}
	}
	p.mu.Unlock()

	p.save()
}

// detectMode 用 token 发最小请求判断 FULL/LIMITED，返回 (模式, 细分原因)
func (p *ProxyPool) detectMode(client *http.Client, authToken string) (string, string) {
	// 发一个最小 free session 请求（和真实流程一致）
	req, err := http.NewRequest("POST", "https://www.codebuff.com/api/v1/freebuff/session", strings.NewReader(`{}`))
	if err != nil {
		return "unknown", "timeout"
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Bun/1.3.14")

	resp, err := client.Do(req)
	if err != nil {
		// 发请求失败 -> 按错误类型细分
		msg := err.Error()
		switch {
		case strings.Contains(msg, "timeout"), strings.Contains(msg, "context deadline"):
			return "unknown", "timeout"
		case strings.Contains(msg, "EOF"), strings.Contains(msg, "connection refused"):
			return "unknown", "offline"
		default:
			return "unknown", "network_error"
		}
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return "limited", "rate_limited"
	case resp.StatusCode == http.StatusBadRequest:
		return "limited", "quota"
	case resp.StatusCode == http.StatusNotFound:
		return "full", ""
	case resp.StatusCode == http.StatusOK:
		return "full", ""
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return "full", ""
	case resp.StatusCode == http.StatusForbidden:
		// 403: freebuff 风控 —— 区分 country_blocked / 其他
		if bodyContains(body, "country_blocked", "proxy traffic", "anonymous") {
			return "unknown", "country_blocked"
		}
		return "unknown", "forbidden"
	case resp.StatusCode == http.StatusUnauthorized:
		return "unknown", "auth_failed"
	case strings.Contains(body, "limited"):
		return "limited", "rate_limited"
	case strings.Contains(body, "rate"):
		return "limited", "rate_limited"
	default:
		return "unknown", fmt.Sprintf("http_%d", resp.StatusCode)
	}
}

func bodyContains(body string, subs ...string) bool {
	for _, s := range subs {
		if strings.Contains(body, s) {
			return true
		}
	}
	return false
}

// ============ 定时自检 ============

const proxySelfCheckInterval = 5 * time.Minute

// Start 启动后台定时自检：每 5 分钟检测一次全部 IP 的连通性和 FULL/LIMITED 模式
func (p *ProxyPool) Start(ctx context.Context, authToken string, logger *log.Logger) {
	go func() {
		ticker := time.NewTicker(proxySelfCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if logger != nil {
					logger.Printf("[proxy] 定时自检开始 (%d 个 IP)", len(p.entries))
				}
				p.checkAll(authToken, logger)
			}
		}
	}()
}

// checkAll 遍历检测全部 IP
func (p *ProxyPool) checkAll(authToken string, logger *log.Logger) {
	entries := p.Entries()
	changed := false
	for _, e := range entries {
		beforeMode, beforeDetail := e.Mode, e.Detail
		p.CheckProxy(e, authToken)
		if e.Mode != beforeMode || e.Detail != beforeDetail {
			changed = true
		}
	}
	if changed {
		p.save()
	}
	if logger != nil {
		logger.Printf("[proxy] 定时自检完成: full=%d limited=%d unknown=%d",
			p.countMode("full"), p.countMode("limited"), p.countMode("unknown"))
	}
}

func (p *ProxyPool) countMode(mode string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.entries {
		if e.Mode == mode {
			n++
		}
	}
	return n
}

// ============ 选择最优出口 ============

// SelectBest 选择延迟最低的可用 FULL 出口；无 FULL 时选 LIMITED
func (p *ProxyPool) SelectBest() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	var bestFull, bestLimited *ProxyEntry
	var bestFullLat, bestLimitedLat int64 = 1<<62, 1<<62

	for _, e := range p.entries {
		// 跳过冷却中、不可用
		if !e.Alive || now.Before(e.CooldownUntil) {
			continue
		}
		if e.Mode == "full" && e.LatencyMs < bestFullLat {
			bestFull = e
			bestFullLat = e.LatencyMs
		} else if e.Mode == "limited" && e.LatencyMs < bestLimitedLat {
			bestLimited = e
			bestLimitedLat = e.LatencyMs
		}
	}

	if bestFull != nil {
		p.current = bestFull.Addr
		return bestFull.Addr
	}
	if bestLimited != nil {
		p.current = bestLimited.Addr
		return bestLimited.Addr
	}
	return ""
}

// Current 返回当前选中的出口
func (p *ProxyPool) Current() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

// Entries 返回所有代理的快照
func (p *ProxyPool) Entries() []*ProxyEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*ProxyEntry, len(p.entries))
	for i, e := range p.entries {
		out[i] = e
	}
	return out
}
