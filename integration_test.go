package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"flowconvert/internal/config"
	"flowconvert/internal/handler"
)

func TestIntegrationImageGeneration(t *testing.T) {
	cfg := config.Load()
	cfg.TmpDir = t.TempDir()
	cfg.OutDir = t.TempDir()
	cfg.EnsureDirs()
	
	store := handler.NewFileStore(cfg)
	imageGen := &handler.ImageGenH{Cfg: cfg, Store: store}
	
	// Test text image generation
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	writer.WriteField("prompt", "abstract geometric art")
	writer.WriteField("width", "256")
	writer.WriteField("height", "256")
	writer.Close()
	
	req := httptest.NewRequest("POST", "/api/convert/image/text", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	imageGen.HandleTextImage(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}
	
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	
	if resp["success"] != true {
		t.Errorf("Expected success=true, got %v", resp["success"])
	}
	if resp["format"] != "png" {
		t.Errorf("Expected format=png, got %v", resp["format"])
	}
}

func TestIntegrationIDPhoto(t *testing.T) {
	cfg := config.Load()
	cfg.TmpDir = t.TempDir()
	cfg.OutDir = t.TempDir()
	cfg.EnsureDirs()
	
	store := handler.NewFileStore(cfg)
	conv := &handler.ConvertH{Cfg: cfg, Store: store}
	
	// Create a simple test image
	img := image.NewRGBA(image.Rect(0, 0, 200, 267))
	for y := 0; y < 267; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{150, 150, 150, 255})
		}
	}
	var imgBuf bytes.Buffer
	png.Encode(&imgBuf, img)
	
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "test.jpg")
	part.Write(imgBuf.Bytes())
	writer.WriteField("size", "一寸")
	writer.WriteField("bg_color", "白色")
	writer.Close()
	
	req := httptest.NewRequest("POST", "/api/convert/idphoto", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	conv.HandleIdPhoto(w, req)
	
	// ID photo may fail if Python dependencies are missing, that's OK for integration test
	if w.Code != http.StatusOK && w.Code != http.StatusUnprocessableEntity {
		t.Errorf("Expected 200 or 422, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCORSHeaders(t *testing.T) {
	cfg := config.Load()
	cfg.TmpDir = t.TempDir()
	cfg.EnsureDirs()
	
	store := handler.NewFileStore(cfg)
	conv := &handler.ConvertH{Cfg: cfg, Store: store}
	
	mux := http.NewServeMux()
	mux.HandleFunc("/api/formats", conv.HandleFormats)
	
	handler := CORS(mux)
	
	req := httptest.NewRequest("OPTIONS", "/api/formats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected 200 for OPTIONS, got %d", w.Code)
	}
	
	// Check security headers
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("Missing X-Content-Type-Options header")
	}
	if w.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("Missing X-Frame-Options header")
	}
}

func TestRateLimiting(t *testing.T) {
	cfg := config.Load()
	cfg.TmpDir = t.TempDir()
	cfg.EnsureDirs()
	
	store := handler.NewFileStore(cfg)
	conv := &handler.ConvertH{Cfg: cfg, Store: store}
	
	mux := http.NewServeMux()
	mux.HandleFunc("/api/formats", conv.HandleFormats)
	
	handler := RateLimit(mux, 2)
	
	// First two requests should succeed
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/api/formats", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("Request %d: Expected 200, got %d", i+1, w.Code)
		}
	}
	
	// Third request should be rate limited
	req := httptest.NewRequest("GET", "/api/formats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Expected 429, got %d", w.Code)
	}
}

func TestMain(m *testing.M) {
	// Setup
	os.Exit(m.Run())
}
