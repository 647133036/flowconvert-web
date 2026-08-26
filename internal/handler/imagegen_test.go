package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"flowconvert/internal/config"
)

func TestHandleTextImage(t *testing.T) {
	cfg := &config.Config{
		TmpDir: t.TempDir(),
	}
	store := NewFileStore(cfg)
	h := &ImageGenH{Cfg: cfg, Store: store}
	
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	writer.WriteField("prompt", "test abstract art")
	writer.WriteField("width", "256")
	writer.WriteField("height", "256")
	writer.Close()
	
	r := httptest.NewRequest("POST", "/api/convert/image/text", body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	
	w := httptest.NewRecorder()
	h.HandleTextImage(w, r)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	
	if resp["success"] != true {
		t.Errorf("Expected success=true, got %v", resp["success"])
	}
}

func TestHandleEditImage(t *testing.T) {
	cfg := &config.Config{
		TmpDir: t.TempDir(),
	}
	store := NewFileStore(cfg)
	h := &ImageGenH{Cfg: cfg, Store: store}
	
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var imgBuf bytes.Buffer
	png.Encode(&imgBuf, img)
	
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	writer.WriteField("prompt", "sepia filter")
	writer.WriteField("size", "original")
	part, _ := writer.CreateFormFile("image", "test.png")
	part.Write(imgBuf.Bytes())
	writer.Close()
	
	r := httptest.NewRequest("POST", "/api/convert/image/edit", body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	
	w := httptest.NewRecorder()
	h.HandleEditImage(w, r)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestHandleComposeImage(t *testing.T) {
	cfg := &config.Config{
		TmpDir: t.TempDir(),
	}
	store := NewFileStore(cfg)
	h := &ImageGenH{Cfg: cfg, Store: store}
	
	var imgBufs [][]byte
	for i := 0; i < 2; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 50, 50))
		for y := 0; y < 50; y++ {
			for x := 0; x < 50; x++ {
				img.Set(x, y, color.RGBA{uint8(x * 5), uint8(y * 5), 100, 255})
			}
		}
		var buf bytes.Buffer
		png.Encode(&buf, img)
		imgBufs = append(imgBufs, buf.Bytes())
	}
	
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	writer.WriteField("prompt", "compose test")
	writer.WriteField("width", "256")
	writer.WriteField("height", "256")
	for i, data := range imgBufs {
		part, _ := writer.CreateFormFile(fmt.Sprintf("ref_%d", i), fmt.Sprintf("ref_%d.png", i))
		part.Write(data)
	}
	writer.Close()
	
	r := httptest.NewRequest("POST", "/api/convert/image/compose", body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	
	w := httptest.NewRecorder()
	h.HandleComposeImage(w, r)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestHandleTextImageBadRequest(t *testing.T) {
	cfg := &config.Config{
		TmpDir: t.TempDir(),
	}
	store := NewFileStore(cfg)
	h := &ImageGenH{Cfg: cfg, Store: store}
	
	body := bytes.NewBufferString(`prompt=`)
	req := httptest.NewRequest("POST", "/api/convert/image/text", body)
	w := httptest.NewRecorder()
	
	h.HandleTextImage(w, req)
	
	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}
