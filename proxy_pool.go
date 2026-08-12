package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ProxyEntry 单个出口代理状态
type ProxyEntry struct {
	Addr        string    `json:"addr"`                  // 127.0.0.1:8002
	Mode        string    `json:"mode"`                  // full / limited / unknown
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
		addr := strings.TrimSpace(line)
		if addr == "" {
			continue
		}
		// 规范化：确保有端口
		if !strings.Contains(addr, ":") {
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
// ============ 检测 ============

// CheckProxy 检测单个代理的连通性和 FULL/LIMITED 模式
// authToken 非空时做模式检测，空时只测连通性
func (p *ProxyPool) CheckProxy(entry *ProxyEntry, authToken string) {
	start := time.Now()

	// 构造通过该代理的 HTTP client
	proxyURL, err := url.Parse("http://" + entry.Addr)
	var client *http.Client
	if err == nil {
		transport := &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
		client = &http.Client{Transport: transport, Timeout: 10 * time.Second}
	} else {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	// 步骤1: 连通性检测 - 访问 freebuff.com
	alive := false
	resp, err := client.Get("https://www.codebuff.com/")
	if err == nil {
		alive = resp.StatusCode >= 200 && resp.StatusCode < 500
		resp.Body.Close()
	}

	latency := time.Since(start).Milliseconds()

	p.mu.Lock()
	entry.Alive = alive
	entry.LatencyMs = latency
	entry.LastChecked = time.Now()

	if !alive {
		entry.FailCount++
		entry.Mode = "unknown"
		if entry.FailCount >= 3 {
			entry.CooldownUntil = time.Now().Add(30 * time.Minute)
		}
	} else {
		entry.FailCount = 0
		entry.LastOK = time.Now()
		// 步骤2: 模式检测（有 token 时）
		if authToken != "" {
			entry.Mode = p.detectMode(client, authToken)
		}
	}
	p.mu.Unlock()

	p.save()
}

// detectMode 用 token 发最小请求判断 FULL/LIMITED
func (p *ProxyPool) detectMode(client *http.Client, authToken string) string {
	// 发一个最小 free session 请求（和真实流程一致）
	req, err := http.NewRequest("POST", "https://www.codebuff.com/api/v1/freebuff/session", strings.NewReader(`{}`))
	if err != nil {
		return "unknown"
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Bun/1.3.14")

	resp, err := client.Do(req)
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return "limited"
	case resp.StatusCode == http.StatusBadRequest:
		return "limited"
	case resp.StatusCode == http.StatusNotFound:
		return "full"
	case resp.StatusCode == http.StatusOK:
		return "full"
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return "full"
	case strings.Contains(string(bodyBytes), "limited"):
		return "limited"
	case strings.Contains(string(bodyBytes), "rate"):
		return "limited"
	default:
		return "unknown"
	}
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
