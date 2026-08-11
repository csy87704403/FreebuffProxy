package main

import (
	_ "embed"
	"net/http"
)

//go:embed webui.html
var webUIPage string

//go:embed webui.js
var webUIScript string

// handleWebUIIndex 返回前端页面（嵌入式 HTML）
func (s *Server) handleWebUIIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(webUIPage))
}

// handleWebUIScript 返回前端 JS（嵌入式）
func (s *Server) handleWebUIScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(webUIScript))
}