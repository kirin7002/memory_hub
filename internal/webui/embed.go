// Package webui 将设备演示页面打包进 Go 服务；不需要额外前端构建或 CDN。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed assets/*
var files embed.FS

func Handler() http.Handler {
	sub, _ := fs.Sub(files, "assets")
	static := http.StripPrefix("/assets/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/" || r.URL.Path == "/client" || r.URL.Path == "/phone" || r.URL.Path == "/car" || r.URL.Path == "/cloud" {
			path := "assets/index.html"
			if r.URL.Path != "/" {
				path = "assets/client.html"
			}
			raw, _ := files.ReadFile(path)
			// 专用入口固定角色；账户、会话和连接状态由各页独立控制。
			role := map[string]string{"/phone": "phone", "/car": "car", "/cloud": "cloud"}[r.URL.Path]
			if role != "" {
				raw = []byte(strings.Replace(string(raw), `data-client-mode="single"`, `data-client-mode="single" data-client-role="`+role+`"`, 1))
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(raw)
			return
		}
		static.ServeHTTP(w, r)
	})
}
