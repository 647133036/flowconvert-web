package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AIClient manages calls to Agnes and SenseNova AI APIs.
type AIClient struct {
	AgnesBaseURL  string
	AgnesAPIKey   string
	SenseBaseURL  string
	SenseAPIKey   string
	HTTP          *http.Client
	// Rate limiter: max 6 requests per minute for Agnes video API
	agnesMu      sync.Mutex
	agnesSince   time.Time
	agnesCount   int
}

// NewAIClient creates an AI client from config values.
func NewAIClient(agnesBase, agnesKey, senseBase, senseKey string) *AIClient {
	return &AIClient{
		AgnesBaseURL: agnesBase,
		AgnesAPIKey:  agnesKey,
		SenseBaseURL: senseBase,
		SenseAPIKey:  senseKey,
		HTTP:         &http.Client{Timeout: 180 * time.Second},
	}
}

// HasAgnes returns true if Agnes credentials are configured.
func (c *AIClient) HasAgnes() bool {
	return c.AgnesAPIKey != ""
}

// HasSenseNova returns true if SenseNova credentials are configured.
func (c *AIClient) HasSenseNova() bool {
	return c.SenseAPIKey != ""
}

// ── Image Generation ──

// imageGenResponse models the JSON returned by /v1/images/generations.
type imageGenResponse struct {
	Data []struct {
		URL       string `json:"url"`
		B64JSON   string `json:"b64_json"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// GenImageAgnes calls the Agnes image generation API.
// model: "agnes-image-2.1-flash" (text-to-image / image-to-image)
//         "agnes-image-2.0-flash" (image editing / multi-image composition)
// size: "1K", "2K", "3K", "4K"
// ratio: "1:1", "16:9", "9:16", "4:3", "3:4", "2:3", "3:2"
// images: optional input image URLs or data URIs for img2img / composition
func (c *AIClient) GenImageAgnes(model, prompt, size, ratio string, images []string) (imgURL string, b64 string, err error) {
	body := map[string]interface{}{
		"model":  model,
		"prompt": prompt,
		"size":   size,
	}
	if ratio != "" {
		body["ratio"] = ratio
	}
	extra := map[string]interface{}{
		"response_format": "url",
	}
	if len(images) > 0 {
		extra["image"] = images
	}
	body["extra_body"] = extra

	resp, err := c.postJSON(c.AgnesBaseURL+"/images/generations", c.AgnesAPIKey, body)
	if err != nil {
		return "", "", fmt.Errorf("agnes图片API请求失败: %v", err)
	}
	var res imageGenResponse
	if err := json.Unmarshal(resp, &res); err != nil {
		return "", "", fmt.Errorf("agnes图片API响应解析失败: %v", err)
	}
	if res.Error != nil {
		return "", "", fmt.Errorf("agnes图片API错误: %s", res.Error.Message)
	}
	if len(res.Data) == 0 {
		return "", "", fmt.Errorf("agnes图片API返回空数据")
	}
	return res.Data[0].URL, res.Data[0].B64JSON, nil
}

// GenImageSenseNova calls the SenseNova image generation API.
// model: "sensenova-u1.5-lite"
func (c *AIClient) GenImageSenseNova(model, prompt, size, ratio string, images []string) (imgURL string, b64 string, err error) {
	body := map[string]interface{}{
		"model":      model,
		"prompt":     prompt,
		"watermark":  false,
	}
	if size != "" {
		body["size"] = size
	}
	if ratio != "" {
		body["ratio"] = ratio
	}
	if len(images) > 0 {
		body["image"] = images
	}

	resp, err := c.postJSON(c.SenseBaseURL+"/images/generations", c.SenseAPIKey, body)
	if err != nil {
		return "", "", fmt.Errorf("sensenova图片API请求失败: %v", err)
	}
	var res imageGenResponse
	if err := json.Unmarshal(resp, &res); err != nil {
		return "", "", fmt.Errorf("sensenova图片API响应解析失败: %v", err)
	}
	if res.Error != nil {
		return "", "", fmt.Errorf("sensenova图片API错误: %s", res.Error.Message)
	}
	if len(res.Data) == 0 {
		return "", "", fmt.Errorf("sensenova图片API返回空数据")
	}
	return res.Data[0].URL, res.Data[0].B64JSON, nil
}

// DownloadImage downloads a remote image URL (or decodes base64) to destPath.
func (c *AIClient) DownloadImage(imgURL, b64, destPath string) error {
	if b64 != "" {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return fmt.Errorf("base64解码失败: %v", err)
		}
		return os.WriteFile(destPath, data, 0o644)
	}
	if imgURL == "" {
		return fmt.Errorf("无图片URL或base64数据")
	}
	resp, err := c.HTTP.Get(imgURL)
	if err != nil {
		return fmt.Errorf("下载图片失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载图片失败: HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %v", err)
	}
	defer out.Close()
	_, err = io.Copy(out, io.LimitReader(resp.Body, 100<<20))
	return err
}

// FileToDataURI reads a local image file and returns a data: URI string.
func FileToDataURI(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(path)
	mime := "image/png"
	switch ext {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".webp":
		mime = "image/webp"
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", mime, b64), nil
}

// ── Video Generation (Agnes Video 2.5 Flash async API) ──

const agnesVideoModel = "agnes-video-2.5-flash"

type videoTaskResponse struct {
	ID       string `json:"id"`
	TaskID   string `json:"task_id"`
	VideoID  string `json:"video_id"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
	Detail string `json:"detail"`
}

type videoResultResponse struct {
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	URL      string `json:"url"`
	Metadata struct {
		URL string `json:"url"`
	} `json:"metadata"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// VideoTaskParams holds parameters for the 2.5 Flash video API.
type VideoTaskParams struct {
	Prompt      string   // required
	Mode        string   // "text", "keyframe", "reference"
	Seconds     string   // "4"-"12", default "5"
	AspectRatio string   // "16:9", "9:16", "1:1", "4:3", "3:4", "21:9"
	FirstFrame  string   // URL for keyframe mode
	LastFrame   string   // URL for keyframe mode
	Images      []string // URLs for reference mode (max 5)
}

// acquireAgnesToken implements a token bucket rate limiter for Agnes API.
// Allows max 6 requests per minute.
func (c *AIClient) acquireAgnesToken() {
	c.agnesMu.Lock()
	defer c.agnesMu.Unlock()
	now := time.Now()
	// Reset counter if more than 1 minute has passed
	if now.Sub(c.agnesSince) > time.Minute {
		c.agnesCount = 0
		c.agnesSince = now
	}
	// Wait if rate limit reached
	for c.agnesCount >= 6 {
		wait := time.Minute - now.Sub(c.agnesSince)
		if wait > 0 {
			time.Sleep(wait)
		}
		now = time.Now()
		if now.Sub(c.agnesSince) > time.Minute {
			c.agnesCount = 0
			c.agnesSince = now
		}
	}
	c.agnesCount++
}

// CreateVideoTask submits a video generation task to Agnes Video 2.5 Flash.
// Retries on 503 queue-full with exponential backoff.
func (c *AIClient) CreateVideoTask(p VideoTaskParams) (string, error) {
	body := map[string]interface{}{
		"model":  agnesVideoModel,
		"prompt": p.Prompt,
		"mode":   p.Mode,
		"size":   "720P",
	}
	if p.Seconds != "" {
		body["seconds"] = p.Seconds
	} else {
		body["seconds"] = "5"
	}
	if p.AspectRatio != "" {
		body["aspect_ratio"] = p.AspectRatio
	}
	if p.Mode == "keyframe" {
		if p.FirstFrame != "" {
			body["first_frame"] = p.FirstFrame
		}
		if p.LastFrame != "" {
			body["last_frame"] = p.LastFrame
		}
	}
	if p.Mode == "reference" && len(p.Images) > 0 {
		body["images"] = p.Images
	}

	// Retry on 503 queue-full or 429 rate limit with exponential backoff
	maxRetries := 10
	var resp []byte
	var err error
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Acquire rate limit token
		c.acquireAgnesToken()
		resp, err = c.postJSON(c.AgnesBaseURL+"/videos", c.AgnesAPIKey, body)
		if err == nil {
			break
		}
		errStr := err.Error()
		// Check for 503 queue full
		if strings.Contains(errStr, "503") && strings.Contains(errStr, "video_queue_full") {
			backoff := time.Duration(10*(attempt+1)) * time.Second
			if backoff > 5*time.Minute {
				backoff = 5 * time.Minute
			}
			fmt.Fprintf(os.Stderr, "[Agnes] 503 queue full, retry %d/%d after %v\n", attempt+1, maxRetries, backoff)
			time.Sleep(backoff)
			continue
		}
		// Check for 429 rate limit
		if strings.Contains(errStr, "429") || strings.Contains(errStr, "rate_limit") || strings.Contains(errStr, "rate limit") {
			backoff := time.Duration(30*(attempt+1)) * time.Second
			if backoff > 2*time.Minute {
				backoff = 2 * time.Minute
			}
			fmt.Fprintf(os.Stderr, "[Agnes] 429 rate limited, retry %d/%d after %v\n", attempt+1, maxRetries, backoff)
			time.Sleep(backoff)
			continue
		}
		break
	}
	if err != nil {
		return "", fmt.Errorf("agnes视频API创建任务失败: %v", err)
	}
	var res videoTaskResponse
	if err := json.Unmarshal(resp, &res); err != nil {
		return "", fmt.Errorf("agnes视频API响应解析失败: %v", err)
	}
	if res.Detail != "" {
		return "", fmt.Errorf("agnes视频API参数错误: %s", res.Detail)
	}
	if res.Error != nil {
		return "", fmt.Errorf("agnes视频API错误: %s", res.Error.Message)
	}
	vid := res.VideoID
	if vid == "" {
		vid = res.TaskID
	}
	if vid == "" {
		vid = res.ID
	}
	if vid == "" {
		return "", fmt.Errorf("agnes视频API未返回任务ID")
	}
	return vid, nil
}

// PollVideoTask polls the Agnes video task until completion or timeout.
// model_name is always passed to support all modes.
func (c *AIClient) PollVideoTask(videoID string, timeout time.Duration) (string, error) {
	base := strings.TrimSuffix(c.AgnesBaseURL, "/v1")
	pollURL := base + "/agnesapi?video_id=" + url.QueryEscape(videoID) + "&model_name=" + agnesVideoModel

	deadline := time.Now().Add(timeout)
	interval := 3 * time.Second
	for time.Now().Before(deadline) {
		req, err := http.NewRequest("GET", pollURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+c.AgnesAPIKey)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			time.Sleep(interval)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var res videoResultResponse
		if err := json.Unmarshal(body, &res); err != nil {
			time.Sleep(interval)
			continue
		}
		if res.Status == "completed" {
			if res.URL != "" {
				return res.URL, nil
			}
			if res.Metadata.URL != "" {
				return res.Metadata.URL, nil
			}
			return "", fmt.Errorf("视频已完成但未返回URL")
		}
		if res.Status == "failed" {
			msg := "生成失败"
			if res.Error != nil {
				msg = res.Error.Message
			}
			return "", fmt.Errorf("视频生成失败: %s", msg)
		}
		time.Sleep(interval)
	}
	return "", fmt.Errorf("视频生成超时")
}

// segmentAttempts is the number of times a single video segment is retried
// when the upstream generator fails transiently (e.g. "DiffGenerator
// returned no result") or the API rate-limits/queues us.
const segmentAttempts = 3

// isTransientVideoErr reports whether a segment generation error is worth
// retrying. DiffGenerator returning no result and 429/503 are transient.
func isTransientVideoErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "DiffGenerator returned no result") ||
		strings.Contains(s, "no result") ||
		strings.Contains(s, "429") ||
		strings.Contains(s, "rate_limit") ||
		strings.Contains(s, "rate limit") ||
		strings.Contains(s, "503") ||
		strings.Contains(s, "video_queue_full")
}

// generateVideoSegment submits, polls and downloads a single video segment,
// retrying on transient failures so that a long multi-segment video does not
// silently lose segments.
func (c *AIClient) generateVideoSegment(segPath string, params VideoTaskParams, label string) error {
	var lastErr error
	for attempt := 0; attempt < segmentAttempts; attempt++ {
		if attempt > 0 {
			fmt.Fprintf(os.Stderr, "[Agnes] 段%s第%d次重试\n", label, attempt+1)
			time.Sleep(5 * time.Second)
		}
		videoID, err := c.CreateVideoTask(params)
		if err != nil {
			lastErr = err
			if isTransientVideoErr(err) {
				continue
			}
			return err
		}
		videoURL, err := c.PollVideoTask(videoID, 30*time.Minute)
		if err != nil {
			lastErr = err
			if isTransientVideoErr(err) {
				continue
			}
			return err
		}
		if err := c.DownloadVideo(videoURL, segPath); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

// DownloadVideo downloads a remote video URL to destPath.
func (c *AIClient) DownloadVideo(videoURL, destPath string) error {
	resp, err := c.HTTP.Get(videoURL)
	if err != nil {
		return fmt.Errorf("下载视频失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载视频失败: HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %v", err)
	}
	defer out.Close()
	_, err = io.Copy(out, io.LimitReader(resp.Body, 500<<20))
	return err
}

// ── HTTP helper ──

func (c *AIClient) postJSON(fullURL, apiKey string, body interface{}) ([]byte, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", fullURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}
