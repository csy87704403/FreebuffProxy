package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// 模型启用状态（内存中持久化，不默认存文件）
var (
	modelEnabledMu sync.RWMutex
	modelEnabled   = map[string]bool{} // 默认空 = 全部启用
)

// ============ 账号删除 ============

func (s *Server) handleAuthAccountDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var payload struct{ Token string `json:"token"` }
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "token required"})
		return
	}

	// 从 config 删除 (AUTH_TOKENS + AUTH_META)
	cfgPath := s.cfg.ConfigPath
	if cfgPath == "" {
		cfgPath = "config.json"
	}
	raw, err := os.ReadFile(cfgPath)
	if err == nil {
		var cfg map[string]any
		if json.Unmarshal(raw, &cfg) == nil {
			tokens, _ := cfg["AUTH_TOKENS"].([]any)
			newTokens := make([]any, 0, len(tokens))
			for _, t := range tokens {
				if tStr, ok := t.(string); ok && tStr != payload.Token {
					newTokens = append(newTokens, tStr)
				}
			}
			cfg["AUTH_TOKENS"] = newTokens
			// 同步删除邮箱映射
			if meta, ok := cfg["AUTH_META"].(map[string]any); ok {
				delete(meta, payload.Token)
				cfg["AUTH_META"] = meta
			}
			updated, _ := json.MarshalIndent(cfg, "", "  ")
			os.WriteFile(cfgPath, updated, 0644)
		}
	}

	// 从运行池移除（关键: 之前只改 config 不删池, 导致刷新后账号还在）
	s.runs.RemoveToken(payload.Token)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ============ 模型管理 ============

func (s *Server) handleWebUIModels(w http.ResponseWriter, r *http.Request) {
	models := s.registry.Models()
	results := make([]map[string]any, 0, len(models))

	modelEnabledMu.RLock()
	defer modelEnabledMu.RUnlock()

	for _, m := range models {
		enabled := true
		if v, ok := modelEnabled[m]; ok {
			enabled = v
		}
		results = append(results, map[string]any{
			"id":      m,
			"enabled": enabled,
			"status":  "unknown",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": results})
}

func (s *Server) handleWebUIModelToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var payload struct {
		Model   string `json:"model"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "model and enabled required"})
		return
	}
	modelEnabledMu.Lock()
	modelEnabled[payload.Model] = payload.Enabled
	modelEnabledMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ============ API Keys 管理 ============

func (s *Server) handleWebUIApis(w http.ResponseWriter, r *http.Request) {
	// 对外 API base_url = 本代理对外地址（给客户端用的 OpenAI 兼容入口）
	// 不是上游 freebuff 的 UPSTREAM_BASE_URL
	baseURL := publicBaseURL(r, s.cfg.ListenAddr)
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url": baseURL,
		"apis":     s.cfg.APIKeys,
	})
}

// publicBaseURL 推导本服务对外 base url：
// 优先 X-Forwarded-*（Cloudflare 隧道/反代）→ Host → ListenAddr
func publicBaseURL(r *http.Request, listenAddr string) string {
	base := ""
	if host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); host != "" {
		scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
		if scheme == "" {
			scheme = "https"
		}
		base = scheme + "://" + host
	} else if host := strings.TrimSpace(r.Host); host != "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		base = scheme + "://" + host
	} else {
		addr := strings.TrimSpace(listenAddr)
		if addr == "" {
			addr = ":16880"
		}
		if strings.HasPrefix(addr, ":") {
			base = "http://127.0.0.1" + addr
		} else {
			base = "http://" + addr
		}
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base
}

func (s *Server) handleWebUIApiCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var payload struct{ Key string `json:"key"` }
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "key required"})
		return
	}
	// 去重
	for _, k := range s.cfg.APIKeys {
		if k == payload.Key {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": false})
			return
		}
	}
	s.cfg.APIKeys = append(s.cfg.APIKeys, payload.Key)
	s.saveConfig()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": true})
}

func (s *Server) handleWebUIApiDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var payload struct{ Key string `json:"key"` }
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "key required"})
		return
	}
	newKeys := make([]string, 0, len(s.cfg.APIKeys))
	for _, k := range s.cfg.APIKeys {
		if k != payload.Key {
			newKeys = append(newKeys, k)
		}
	}
	s.cfg.APIKeys = newKeys
	s.saveConfig()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ============ 设置 ============

func (s *Server) handleWebUISettingsPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var payload struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	if payload.NewPassword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "new password required"})
		return
	}

	// 更新 APIKeys 中的管理密码（第一个 key 作为管理密码）
	if len(s.cfg.APIKeys) > 0 {
		// 验证旧密码
		matched := false
		for _, k := range s.cfg.APIKeys {
			if k == payload.OldPassword {
				matched = true
				break
			}
		}
		if !matched {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "旧密码错误"})
			return
		}
		s.cfg.APIKeys = append([]string{payload.NewPassword}, s.cfg.APIKeys...)
	} else {
		s.cfg.APIKeys = []string{payload.NewPassword}
	}
	s.saveConfig()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ============ Proxy Pool ============

func (s *Server) handleProxyPool(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"entries": s.proxyPool.Entries()})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var payload struct {
		Action string   `json:"action"`
		Addrs  []string `json:"addrs"`
		Addr   string   `json:"addr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	switch payload.Action {
	case "add":
		n := 0
		for _, addr := range payload.Addrs {
			addr = strings.TrimSpace(addr)
			if addr != "" && strings.Contains(addr, ":") {
				s.proxyPool.AddBatch(addr)
				n++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": n})
	case "remove":
		ok := s.proxyPool.Remove(payload.Addr)
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
	case "save":
		s.proxyPool.save()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "check":
		// 单个 IP 检测: 复用 CheckProxy (连通性 + FULL/LIMITED 模式)
		addr := strings.TrimSpace(payload.Addr)
		if addr == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "addr required"})
			return
		}
		authToken := ""
		if len(s.cfg.AuthTokens) > 0 {
			authToken = s.cfg.AuthTokens[0]
		}
		entry := s.proxyPool.Find(addr)
		if entry == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "addr not found"})
			return
		}
		s.proxyPool.CheckProxy(entry, authToken)
		s.proxyPool.save()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"addr":        entry.Addr,
			"mode":        entry.Mode,
			"latency_ms":  entry.LatencyMs,
			"alive":       entry.Alive,
			"fail_count":  entry.FailCount,
			"cooldown_until": entry.CooldownUntil,
		})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action"})
	}
}

func (s *Server) handleProxyRefresh(w http.ResponseWriter, r *http.Request) {
	// SSE 流式返回检测进度，避免长时间阻塞无反馈
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	writeSSE := func(data map[string]any) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	authToken := ""
	if len(s.cfg.AuthTokens) > 0 {
		authToken = s.cfg.AuthTokens[0]
	}
	entries := s.proxyPool.Entries()
	total := len(entries)
	for i, e := range entries {
		s.proxyPool.CheckProxy(e, authToken)
		// 逐个推送进度
		writeSSE(map[string]any{
			"type":     "progress",
			"done":     i + 1,
			"total":    total,
			"addr":     e.Addr,
			"mode":     e.Mode,
			"latency_ms": e.LatencyMs,
			"alive":    e.Alive,
		})
		select {
		case <-r.Context().Done():
			return
		default:
		}
		time.Sleep(200 * time.Millisecond) // 避免太频繁
	}
	s.proxyPool.save()
	writeSSE(map[string]any{"type": "done", "checked": total})
}

func (s *Server) handleProxySelect(w http.ResponseWriter, r *http.Request) {
	best := s.proxyPool.SelectBest()
	writeJSON(w, http.StatusOK, map[string]any{
		"selected": best,
		"current":  s.proxyPool.Current(),
	})
}

// ============ 辅助方法 ============

func (s *Server) saveConfig() {
	cfgPath := s.cfg.ConfigPath
	if cfgPath == "" {
		cfgPath = "config.json"
	}
	// 将 Config 转成 map 输出
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	var cfg map[string]any
	if json.Unmarshal(raw, &cfg) != nil {
		return
	}
	cfg["API_KEYS"] = s.cfg.APIKeys
	updated, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(cfgPath, updated, 0644)
}