package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// FreebuffAuthAPI 官方认证后端（freebuff.com，非旧的 freebuff.llm.pm 第三方）
const authBackendBase = "https://freebuff.com"

// session 生成请求的响应
type authCodeResponse struct {
	FingerprintID   string `json:"fingerprintId"`
	FingerprintHash string `json:"fingerprintHash"`
	LoginURL        string `json:"loginUrl"`
	ExpiresAt       int64  `json:"expiresAt"`
	ExpiresInMs     int64  `json:"expiresInMs"`
}

// 轮询状态响应
type authStatusResponse struct {
	Pending bool `json:"pending"`
	User    *struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		Email          string `json:"email"`
		AuthToken      string `json:"authToken"`
		FingerprintID  string `json:"fingerprintId"`
		FingerprintHash string `json:"fingerprintHash"`
	} `json:"user"`
	Error string `json:"error"`
}
// ============ HTTP Handlers ============

// handleAuthCode 代理 POST /api/auth/cli/code 到官方 freebuff.com
// 官方协议: POST body {"fingerprintId": "..."}, 响应 {fingerprintId, fingerprintHash, loginUrl, expiresAt, expiresInMs}
func (s *Server) handleAuthCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	// 生成官方指纹 ID (codebuff-sdk-随机base36, 对齐官方 CLI)
	fingerprintID := "codebuff-sdk-" + generateClientSessionId()
	body, _ := json.Marshal(map[string]string{"fingerprintId": fingerprintID})

	req, err := http.NewRequest(http.MethodPost, authBackendBase+"/api/auth/cli/code", bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "create request: " + err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "upstream call failed: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "read upstream response: " + err.Error()})
		return
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "upstream status " + fmt.Sprint(resp.StatusCode) + ": " + string(respBody)})
		return
	}

	var parsed authCodeResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "parse upstream response: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, parsed)
}

// handleAuthStatus 代理 GET /api/auth/cli/status 到官方 freebuff.com（轮询拿 token）
// 官方协议: GET ?fingerprintId=..&fingerprintHash=..&expiresAt=..
// 响应: {pending:true} 未完成 | {user:{id,name,email,authToken,fingerprintId,fingerprintHash}} 已登录
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	var bodyReq struct {
		FingerprintID   string `json:"fingerprintId"`
		FingerprintHash string `json:"fingerprintHash"`
		ExpiresAt       int64  `json:"expiresAt"`
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body: " + err.Error()})
		return
	}
	if err := json.Unmarshal(bodyBytes, &bodyReq); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error()})
		return
	}

	// 官方是 GET + query
	q := url.Values{}
	q.Set("fingerprintId", bodyReq.FingerprintID)
	q.Set("fingerprintHash", bodyReq.FingerprintHash)
	q.Set("expiresAt", fmt.Sprint(bodyReq.ExpiresAt))
	req, err := http.NewRequest(http.MethodGet, authBackendBase+"/api/auth/cli/status?"+q.Encode(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "create request: " + err.Error()})
		return
	}

	client := &http.Client{Timeout: 20 * time.Second}
	upstreamResp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "upstream call failed: " + err.Error()})
		return
	}
	defer upstreamResp.Body.Close()

	upstreamBody, err := io.ReadAll(upstreamResp.Body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "read upstream response: " + err.Error()})
		return
	}

	if upstreamResp.StatusCode < 200 || upstreamResp.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "upstream status " + fmt.Sprint(upstreamResp.StatusCode) + ": " + string(upstreamBody)})
		return
	}

	var parsed authStatusResponse
	if err := json.Unmarshal(upstreamBody, &parsed); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "parse upstream response: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, parsed)
}

// handleAuthImport 把拿到的 token 写进 config.json 并热重载
func (s *Server) handleAuthImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body: " + err.Error()})
		return
	}

	var payload struct {
		AuthToken   string `json:"authToken"`
		Email       string `json:"email"`
		Name        string `json:"name"`
		FingerprintID string `json:"fingerprintId"`
	}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error()})
		return
	}

	if payload.AuthToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "authToken is required"})
		return
	}

	// 写进 config.json
	configPath := s.cfg.ConfigPath
	if configPath == "" {
		configPath = "config.json"
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "read config: " + err.Error()})
		return
	}

	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "parse config: " + err.Error()})
		return
	}

	// 追加 token
	tokens, _ := cfg["AUTH_TOKENS"].([]any)
	// 去重
	already := false
	for _, t := range tokens {
		if tStr, ok := t.(string); ok && tStr == payload.AuthToken {
			already = true
			break
		}
	}
	if !already {
		tokens = append(tokens, payload.AuthToken)
		cfg["AUTH_TOKENS"] = tokens
		// 记录邮箱映射 (token -> email)
		if payload.Email != "" {
			meta, _ := cfg["AUTH_META"].(map[string]any)
			if meta == nil {
				meta = make(map[string]any)
			}
			meta[payload.AuthToken] = payload.Email
			cfg["AUTH_META"] = meta
		}
		updated, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.WriteFile(configPath, updated, 0644); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "write config: " + err.Error()})
			return
		}
		// 热重载：加入 token 池
		agentIDs := s.registry.AgentIDs()
		if added, err := s.runs.AddToken(payload.AuthToken, payload.Email, agentIDs); err != nil {
			s.webui.Log("error", "auth", "hot reload token failed: "+err.Error())
		} else if added {
			s.webui.Log("info", "auth", "added token via OAuth: "+payload.Email)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"added":   !already,
		"total":   len(tokens),
		"token":   truncateToken(payload.AuthToken),
	})
}

func truncateToken(t string) string {
	if len(t) > 8 {
		return t[:8] + "***"
	}
	return t + "***"
}
