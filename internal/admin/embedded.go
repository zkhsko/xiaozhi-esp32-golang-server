package admin

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed dist/*
var distFS embed.FS

// getStaticFS 优先尝试从本地磁盘加载 dist 资源（便于本地开发构建后立即生效），若不存在则回退至嵌入文件系统。
func getStaticFS() fs.FS {
	for _, dir := range []string{"internal/admin/dist", "dist"} {
		if fi, err := os.Stat(filepath.Join(dir, "index.html")); err == nil && !fi.IsDir() {
			return os.DirFS(dir)
		}
	}
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Handler 返回用于承载管理前端 SPA 静态资源的 HTTP Handler。
func Handler() http.Handler {
	subFS := getStaticFS()
	fileServer := http.FileServer(http.FS(subFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}

		// 检查静态文件是否存在
		f, err := subFS.Open(path)
		if err == nil {
			stat, err := f.Stat()
			_ = f.Close()
			if err == nil && !stat.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// 文件不存在或为目录时，SPA 路由 fallback 到 index.html
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}
