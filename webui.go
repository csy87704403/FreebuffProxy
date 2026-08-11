package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ============ 内存统计与日志（环形缓冲） ============

// UsageRecord 单次调用的令牌用量
type UsageRecord struct {
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
	Tokens   int    `json:"tokens"`
	Requests int    `json:"requests"`
	Time     string `json:"time"`
}

// LogEntry 单条调用日志
type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Source  string `json:"source"` // model / token / server / probe
	Message string `json:"message"`
}

// WebUI 管理接口状态
type WebUI struct {
	mu      sync.Mutex
	usage   []UsageRecord
	logs    []LogEntry
	started time.Time
	// 环形缓冲上限
	maxLogs int
}

func NewWebUI() *WebUI {
	return &WebUI{
		usage:   make([]UsageRecord, 0, 64),
		logs:    make([]LogEntry, 0, 64),
		started: time.Now(),
		maxLogs: 500,
	}
}

// RecordUsage 记录一次调用（流式或非流式）
func (w *WebUI) RecordUsage(model, apiKey string, tokens int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.usage = append(w.usage, UsageRecord{
		Model:    model,
		APIKey:   apiKey,
		Tokens:   tokens,
		Requests: 1,
		Time:     time.Now().UTC().Format(time.RFC3339),
	})
	// 环形缓冲: 最多保留 2000 条用量
	if len(w.usage) > 2000 {
		w.usage = w.usage[len(w.usage)-2000:]
	}
}

// Log 追加一条日志（自动清理: 超过 maxLogs 淘汰最旧）
func (w *WebUI) Log(level, source, message string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.logs = append(w.logs, LogEntry{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Level:   level,
		Source:  source,
		Message: message,
	})
	if len(w.logs) > w.maxLogs {
		w.logs = w.logs[len(w.logs)-w.maxLogs:]
	}
}

// UsageSummary 按模型 / 按 API Key 聚合用量
func (w *WebUI) UsageSummary(days int) map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()

	cutoff := time.Now().UTC().AddDate(0, 0, -days)

	byModel := map[string]int{}
	byKey := map[string]int{}
	total := 0
	requests := 0

	for _, u := range w.usage {
		t, err := time.Parse(time.RFC3339, u.Time)
		if err != nil || t.Before(cutoff) {
			continue
		}
		byModel[u.Model] += u.Tokens
		byKey[u.APIKey] += u.Tokens
		total += u.Tokens
		requests += u.Requests
	}

	return map[string]any{
		"days":      days,
		"total":     total,
		"requests":  requests,
		"by_model":  byModel,
		"by_api_key": byKey,
	}
}

// Logs 返回滚动日志（最新在前）
func (w *WebUI) Logs() []LogEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]LogEntry, len(w.logs))
	for i := range w.logs {
		out[i] = w.logs[len(w.logs)-1-i]
	}
	return out
}

func (w *WebUI) ClearLogs() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.logs = w.logs[:0]
}

// ============ HTTP Handlers ============

func (s *Server) handleWebUIStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"uptime_sec": int(time.Since(s.started).Seconds()),
		"models":    len(s.registry.Models()),
		"tokens":    len(s.cfg.AuthTokens),
		"api_keys":  len(s.cfg.APIKeys),
		"started":   s.started.UTC(),
	})
}

func (s *Server) handleWebUITokens(w http.ResponseWriter, r *http.Request) {
	snapshots := s.runs.Snapshots()
	writeJSON(w, http.StatusOK, snapshots)
}

func (s *Server) handleWebUIProbe(w http.ResponseWriter, r *http.Request) {
	models := s.registry.Models()
	if len(models) == 0 {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	results := make([]map[string]any, 0, len(models))
	for _, model := range models {
		latency, err := s.probeModel(model)
		result := map[string]any{
			"model":  model,
			"status": "ok",
		}
		if err != nil {
			result["status"] = "error"
			result["error"] = err.Error()
			result["error_code"] = errorCodeOf(err)
		} else {
			result["latency_ms"] = latency
		}
		results = append(results, result)
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleWebUIUsage(w http.ResponseWriter, r *http.Request) {
	days := 1
	if v := r.URL.Query().Get("days"); v != "" {
		fmt.Sscanf(v, "%d", &days)
		if days < 1 || days > 30 {
			days = 1
		}
	}
	writeJSON(w, http.StatusOK, s.webui.UsageSummary(days))
}

func (s *Server) handleWebUILogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"logs":  s.webui.Logs(),
		"total": len(s.webui.Logs()),
	})
}

func (s *Server) handleWebUILogsClear(w http.ResponseWriter, r *http.Request) {
	s.webui.ClearLogs()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// probeModel 探测单个模型: 尝试用代理发起一次最小请求
func (s *Server) probeModel(model string) (int64, error) {
	start := time.Now()
	// 尝试获取一个 run lease
	agentID, ok := s.registry.AgentForModel(model)
	if !ok {
		return 0, fmt.Errorf("no agent for model %s", model)
	}
	// 探测 run 可用性（从 runManager 拿一个 lease）
	lease, err := s.runs.Acquire(context.Background(), agentID)
	if err != nil {
		return 0, err
	}
	defer s.runs.Release(lease)
	// 这里只是探测 run 可用性，不真正发请求
	return time.Since(start).Milliseconds(), nil
}

func errorCodeOf(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// 提取常见的 HTTP 状态码
	for _, code := range []string{"401", "403", "404", "409", "429", "500", "502", "503"} {
		if strings.Contains(msg, code) {
			return code
		}
	}
	return "unknown"
}