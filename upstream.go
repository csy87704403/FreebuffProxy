package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type UpstreamClient struct {
	baseURL      string
	httpClient   *http.Client
	userAgent    string // chat/completions 用的 UA (Python UA_CHAT)
	sessionUA    string // session/run 用的 UA (Python UA_SESSION)
	actingUserID string // x-freebuff-acting-user-id 头 (Python 已验证)
}

func NewUpstreamClient(cfg Config) *UpstreamClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.HTTPProxy != "" {
		if proxyURL, err := url.Parse(cfg.HTTPProxy); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}

	return &UpstreamClient{
		baseURL: cfg.UpstreamBaseURL,
		httpClient: &http.Client{
			Timeout:   cfg.RequestTimeout,
			Transport: transport,
		},
		userAgent:    cfg.UserAgent,
		sessionUA:    "Bun/1.3.14",
		actingUserID: cfg.ActingUserID,
	}
}

func (c *UpstreamClient) StartRun(ctx context.Context, authToken, agentID string) (string, error) {
	payload := map[string]any{
		"action":  "START",
		"agentId": agentID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal start run request: %w", err)
	}

	resp, err := c.doJSONWithUA(ctx, authToken, "/api/v1/agent-runs", body, c.sessionUA)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read start run response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("start run failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}

	var parsed struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(responseBody, &parsed); err != nil {
		return "", fmt.Errorf("decode start run response: %w", err)
	}
	if strings.TrimSpace(parsed.RunID) == "" {
		return "", fmt.Errorf("start run response missing runId: %s", strings.TrimSpace(string(responseBody)))
	}

	return parsed.RunID, nil
}

func (c *UpstreamClient) FinishRun(ctx context.Context, authToken, runID string, totalSteps int) error {
	payload := map[string]any{
		"action":        "FINISH",
		"runId":         runID,
		"status":        "completed",
		"totalSteps":    totalSteps,
		"directCredits": 0,
		"totalCredits":  0,
	}
	// Python 已验证协议: steps 数组 (含 stepNumber/status/completed)
	steps := make([]map[string]any, 0, totalSteps)
	for i := 1; i <= totalSteps; i++ {
		steps = append(steps, map[string]any{
			"id":          generateUUID(),
			"stepNumber":  i,
			"credits":     0,
			"childRunIds": []any{},
			"messageId":   nil,
			"status":      "completed",
			"startTime":   time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	payload["steps"] = steps

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal finish run request: %w", err)
	}

	resp, err := c.doJSONWithUA(ctx, authToken, "/api/v1/agent-runs", body, c.sessionUA)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read finish run response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("finish run failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	return nil
}

func (c *UpstreamClient) ChatCompletions(ctx context.Context, authToken string, body []byte) (*http.Response, []byte, error) {
	resp, err := c.doJSON(ctx, authToken, "/api/v1/chat/completions", body)
	if err != nil {
		return nil, nil, err
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil, nil
	}

	responseBody, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, nil, fmt.Errorf("read upstream error response: %w", readErr)
	}
	return resp, responseBody, nil
}

func (c *UpstreamClient) doJSON(ctx context.Context, authToken, path string, body []byte) (*http.Response, error) {
	return c.doJSONWithUA(ctx, authToken, path, body, c.userAgent)
}

func (c *UpstreamClient) doJSONWithUA(ctx context.Context, authToken, path string, body []byte, ua string) (*http.Response, error) {
	requestURL, err := url.JoinPath(c.baseURL, path)
	if err != nil {
		return nil, fmt.Errorf("build upstream url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", ua)
	if c.actingUserID != "" {
		req.Header.Set("x-freebuff-acting-user-id", c.actingUserID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send upstream request: %w", err)
	}
	return resp, nil
}

func retryAfterDuration(headerValue string) time.Duration {
	headerValue = strings.TrimSpace(headerValue)
	if headerValue == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(headerValue); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}
