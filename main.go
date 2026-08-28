package main

import (
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"flowconvert/internal/config"
	"flowconvert/internal/handler"
	"flowconvert/internal/service"
	"flowconvert/web"
)

func main() {
	cfg := config.Load()
	if err := cfg.EnsureDirs(); err != nil {
		log.Fatalf("初始化数据目录失败: %v", err)
	}

	store := handler.NewFileStore(cfg)
	ai := service.NewAIClient(cfg.AgnesBaseURL, cfg.AgnesAPIKey, cfg.SenseNovaBase, cfg.SenseNovaKey)
	conv := &handler.ConvertH{Cfg: cfg, Store: store}
	translator := &handler.TranslateH{Cfg: cfg, Store: store}
	imageGen := &handler.ImageGenH{Cfg: cfg, Store: store, AI: ai}
	videoGen := &handler.VideoGenH{Cfg: cfg, Store: store, AI: ai, Jobs: handler.NewVideoJobStore(30 * time.Minute)}

	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/formats", conv.HandleFormats)
	mux.HandleFunc("/api/convert/upload", conv.HandleUploadVectorize)
	mux.HandleFunc("/api/convert/url", conv.HandleURLVectorize)
	mux.HandleFunc("/api/convert/pdf-to-office", conv.HandlePdfToOffice)
	mux.HandleFunc("/api/convert/sketch", conv.HandleSketch)
	mux.HandleFunc("/api/convert/idphoto", conv.HandleIdPhoto)
	mux.HandleFunc("/api/translate", translator.HandleTranslate)
	mux.HandleFunc("/api/translate/file", translator.HandleTranslateFile)
	mux.HandleFunc("/api/convert/image/text", imageGen.HandleTextImage)
	mux.HandleFunc("/api/convert/image/edit", imageGen.HandleEditImage)
	mux.HandleFunc("/api/convert/image/compose", imageGen.HandleComposeImage)
	mux.HandleFunc("/api/convert/video/text", videoGen.HandleTextVideo)
	mux.HandleFunc("/api/convert/video/keyframe", videoGen.HandleKeyframeVideo)
	mux.HandleFunc("/api/convert/video/ref", videoGen.HandleRefVideo)
	mux.HandleFunc("/api/convert/video/task/", videoGen.HandleVideoTaskStatus)
	mux.HandleFunc("/api/download/", store.DownloadHandler)

	// Static + pages
	mux.HandleFunc("/", pageHandler)

	// Apply middleware
	handler := CORS(mux)
	handler = RateLimit(handler, 100)

	addr := ":" + cfg.Port
	log.Printf("FlowConvert 启动于 http://0.0.0.0%s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}

// pageHandler serves static assets and multi-page HTML.
func pageHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if strings.HasPrefix(path, "/static/") {
		serveStatic(w, r, strings.TrimPrefix(path, "/"))
		return
	}

	switch path {
	case "/", "/index.html":
		servePage(w, "index.html")
	case "/idphoto":
		servePage(w, "idphoto.html")
	case "/translate":
		servePage(w, "translate.html")
	case "/video":
		servePage(w, "video.html")
	case "/image":
		servePage(w, "image.html")
	case "/about":
		servePage(w, "about.html")
	case "/donate":
		servePage(w, "donate.html")
	default:
		http.NotFound(w, r)
	}
}

func serveStatic(w http.ResponseWriter, r *http.Request, name string) {
	contentType := contentTypeByName(name)
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	b, err := fs.ReadFile(web.Assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(b)
}

func servePage(w http.ResponseWriter, name string) {
	b, err := fs.ReadFile(web.Assets, name)
	if err != nil {
		http.Error(w, "页面不存在", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func contentTypeByName(name string) string {
	switch {
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".webp"):
		return "image/webp"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	default:
		return ""
	}
}
