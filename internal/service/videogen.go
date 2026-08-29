package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// VideoParams holds parameters for video generation.
type VideoParams struct {
	Prompt   string
	Duration int
	Style    string
	Width    int
	Height   int
}

// MakeTextVideo creates a video from text prompt via Python backend.
// marshalVideoPayload serializes video generation parameters to JSON for the
// Python scripts. json.Marshal guarantees valid, escaped JSON even when the
// prompt contains quotes, newlines, control characters, or non-ASCII text.
func marshalVideoPayload(fields map[string]interface{}) ([]byte, error) {
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("参数序列化失败: %v", err)
	}
	return payload, nil
}

func MakeTextVideo(tmpDir, prompt string, duration int) (string, error) {
	if duration <= 0 {
		duration = 3
	}
	if duration > 60 {
		duration = 60
	}
	dest := filepath.Join(tmpDir, "video.mp4")
	payloadPath := filepath.Join(tmpDir, "video_payload.json")
	payload, err := marshalVideoPayload(map[string]interface{}{"prompt": prompt, "duration": duration})
	if err != nil {
		return "", fmt.Errorf("参数序列化失败: %v", err)
	}
	if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
		return "", fmt.Errorf("保存参数失败: %v", err)
	}
	defer os.Remove(payloadPath)

	out, err := RunCmdTimeout(10*time.Minute, PythonPath(), ScriptPath("video.py"), "text", payloadPath, dest)
	if err != nil {
		return "", fmt.Errorf("视频生成失败: %s", strings.TrimSpace(out))
	}
	if _, err := os.Stat(dest); err != nil {
		return "", fmt.Errorf("视频生成失败，请稍后重试")
	}
	return dest, nil
}

// MakeKeyframeVideo creates a video between two keyframes.
func MakeKeyframeVideo(tmpDir, firstFrame, lastFrame, prompt string, duration int) (string, error) {
	if duration <= 0 {
		duration = 5
	}
	dest := filepath.Join(tmpDir, "keyframe_video.mp4")
	payloadPath := filepath.Join(tmpDir, "kf_payload.json")
	payload, err := marshalVideoPayload(map[string]interface{}{
		"first":    firstFrame,
		"last":     lastFrame,
		"prompt":   prompt,
		"duration": duration,
	})
	if err != nil {
		return "", fmt.Errorf("参数序列化失败: %v", err)
	}
	if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
		return "", fmt.Errorf("保存参数失败: %v", err)
	}
	defer os.Remove(payloadPath)

	out, err := RunCmdTimeout(10*time.Minute, PythonPath(), ScriptPath("video.py"), "keyframe", payloadPath, dest)
	if err != nil {
		return "", fmt.Errorf("视频生成失败: %s", strings.TrimSpace(out))
	}
	if _, err := os.Stat(dest); err != nil {
		return "", fmt.Errorf("视频生成失败，请稍后重试")
	}
	return dest, nil
}

// MakeRefVideo creates a video from reference images.
func MakeRefVideo(tmpDir, prompt string, refPaths []string, duration int) (string, error) {
	if duration <= 0 {
		duration = 5
	}
	dest := filepath.Join(tmpDir, "ref_video.mp4")
	payloadPath := filepath.Join(tmpDir, "ref_payload.json")

	payload, err := marshalVideoPayload(map[string]interface{}{
		"prompt":   prompt,
		"refs":     refPaths,
		"duration": duration,
	})
	if err != nil {
		return "", fmt.Errorf("参数序列化失败: %v", err)
	}
	if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
		return "", fmt.Errorf("保存参数失败: %v", err)
	}
	defer os.Remove(payloadPath)

	out, err := RunCmdTimeout(10*time.Minute, PythonPath(), ScriptPath("video.py"), "ref", payloadPath, dest)
	if err != nil {
		return "", fmt.Errorf("视频生成失败: %s", strings.TrimSpace(out))
	}
	if _, err := os.Stat(dest); err != nil {
		return "", fmt.Errorf("视频生成失败，请稍后重试")
	}
	return dest, nil
}

// ── AI-powered video generation via Agnes Video 2.5 Flash ──

// clampSeconds clamps duration to the 2.5 Flash supported range (4-12).
func clampSeconds(d int) string {
	if d < 4 {
		d = 4
	}
	if d > 12 {
		d = 12
	}
	return fmt.Sprintf("%d", d)
}

// splitDuration divides a total duration into segments of 4-12 seconds each.
func splitDuration(total int) []int {
	if total <= 4 {
		return []int{4}
	}
	if total <= 12 {
		return []int{total}
	}
	n := (total + 11) / 12
	base := total / n
	rem := total % n
	segs := make([]int, n)
	for i := 0; i < n; i++ {
		segs[i] = base
		if i < rem {
			segs[i]++
		}
	}
	return segs
}

// splitPromptClauses splits a user prompt into narrative clauses by Chinese
// and ASCII punctuation. Each clause is a self-contained phrase the user
// wrote, e.g. "枫叶红在路的两边，两个人，回忆往事" -> 3 clauses. These clauses
// are used to give each video segment a different user-derived focus so
// segments stay relevant to the input while differing from one another.
func splitPromptClauses(prompt string) []string {
	splits := strings.FieldsFunc(prompt, func(r rune) bool {
		switch r {
		case '，', '。', '！', '？', '、', '；', ',', '.', '!', '?', ';':
			return true
		}
		return false
	})
	var clauses []string
	for _, s := range splits {
		s = strings.TrimSpace(s)
		if s != "" {
			clauses = append(clauses, s)
		}
	}
	return clauses
}

// segmentStagePrompt builds a distinct, user-derived prompt for a given segment
// index. It reuses the user's full prompt as the background subject and
// focuses the segment on a specific clause extracted from the user's own text
// (cycling when there are more segments than clauses), plus a stage tag. This
// keeps every segment relevant to what the user wrote while ensuring each one
// is visibly different.
func segmentStagePrompt(prompt string, i, n int) string {
	clauses := splitPromptClauses(prompt)
	var focus string
	if len(clauses) > 0 {
		focus = clauses[i%len(clauses)]
	} else {
		focus = prompt
	}
	var stage string
	switch {
	case i == 0:
		stage = "故事开端"
	case i == n-1:
		stage = "故事结尾"
	default:
		stage = fmt.Sprintf("第%d阶段", i+1)
	}
	return fmt.Sprintf("%s。本段聚焦：%s。叙事：%s", prompt, focus, stage)
}

// concatVideos merges multiple MP4 segments using ffmpeg concat demuxer.
// probeResolution returns width,height from a video file using ffprobe.
func probeResolution(path string) (int, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "json", path).CombinedOutput()
	if err != nil {
		return 0, 0, err
	}
	var result struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &result); err != nil || len(result.Streams) == 0 {
		return 0, 0, fmt.Errorf("无法解析视频分辨率")
	}
	return result.Streams[0].Width, result.Streams[0].Height, nil
}

func concatVideos(tmpDir string, segPaths []string, dest string) (string, error) {
	listPath := filepath.Join(tmpDir, "concat_list.txt")
	var b strings.Builder
	for _, p := range segPaths {
		// concat demuxer resolves relative paths against the directory of the
		// list file, not the process cwd. Use absolute paths to avoid a doubled
		// path prefix (e.g. data/tmp/vid_x/data/tmp/vid_x/seg.mp4).
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		b.WriteString(fmt.Sprintf("file '%s'\n", abs))
	}
	if err := os.WriteFile(listPath, []byte(b.String()), 0o600); err != nil {
		return "", fmt.Errorf("写入拼接列表失败: %v", err)
	}

	// Try stream copy first (fastest)
	out, err := RunCmd("ffmpeg", "-y", "-f", "concat", "-safe", "0",
		"-i", listPath, "-c", "copy", "-movflags", "+faststart", dest)
	if err != nil {
		// Fallback: re-encode with resolution from first segment
		w, h, probeErr := probeResolution(segPaths[0])
		if probeErr != nil {
			return "", fmt.Errorf("视频拼接失败: 无法获取分辨率: %v", probeErr)
		}
		out, err = RunCmdTimeout(180*time.Second, "ffmpeg", "-y",
			"-f", "concat", "-safe", "0", "-i", listPath,
			"-s", fmt.Sprintf("%dx%d", w, h),
			"-c:v", "libx264", "-preset", "fast", "-crf", "23",
			"-c:a", "aac", "-movflags", "+faststart", dest)
		if err != nil {
			return "", fmt.Errorf("视频拼接失败: %s", strings.TrimSpace(out))
		}
	}
	if _, err := os.Stat(dest); err != nil {
		return "", fmt.Errorf("拼接输出文件不存在")
	}
	return dest, nil
}

// MakeLongTextVideoAI generates a long video by splitting into segments,
// generating each concurrently via Agnes 2.5 Flash, then concatenating with ffmpeg.
func MakeLongTextVideoAI(client *AIClient, tmpDir, prompt string, totalDuration int, aspectRatio string) (string, error) {
	dest := filepath.Join(tmpDir, "long_video.mp4")
	if !client.HasAgnes() {
		return "", fmt.Errorf("Agnes API未配置")
	}

	segs := splitDuration(totalDuration)
	if len(segs) == 0 {
		return "", fmt.Errorf("时长参数无效")
	}

	// Sequential submission to avoid overwhelming Agnes queue
	segPaths := make([]string, len(segs))
	errs := make([]error, len(segs))

	for i, segDur := range segs {
		if i > 0 {
			time.Sleep(5 * time.Second)
		}
		segPath := filepath.Join(tmpDir, fmt.Sprintf("seg_%03d.mp4", i))

		err := client.generateVideoSegment(segPath, VideoTaskParams{
			Prompt:      segmentStagePrompt(prompt, i, len(segs)),
			Mode:        "text",
			Seconds:     clampSeconds(segDur),
			AspectRatio: aspectRatio,
		}, fmt.Sprintf("text-%d", i+1))
		if err != nil {
			errs[i] = fmt.Errorf("第%d段生成失败: %v", i+1, err)
			continue
		}
		segPaths[i] = segPath
	}

	// Collect successful segments; on partial failure still concat what we have.
	var paths []string
	var firstErr error
	for i := range segs {
		if errs[i] != nil {
			if firstErr == nil {
				firstErr = errs[i]
			}
			continue
		}
		if segPaths[i] != "" {
			paths = append(paths, segPaths[i])
		}
	}

	if len(paths) == 0 {
		return "", fmt.Errorf("所有分段生成失败: %v", firstErr)
	}
	if firstErr != nil && len(paths) < len(segs) {
		fmt.Fprintf(os.Stderr, "[PartialVideo] 部分分段失败(%v)，使用%d/%d段拼接\n", firstErr, len(paths), len(segs))
	}
	if len(paths) == 1 {
		return paths[0], nil
	}
	return concatVideos(tmpDir, paths, dest)
}

// MakeLongKeyframeVideoAI generates a long keyframe video by splitting:
// first segment uses firstFrameURL, last segment uses lastFrameURL, middle segments are text-only.
func MakeLongKeyframeVideoAI(client *AIClient, tmpDir, firstFrameURL, lastFrameURL, prompt string, totalDuration int, aspectRatio string) (string, error) {
	dest := filepath.Join(tmpDir, "long_keyframe_video.mp4")
	if !client.HasAgnes() {
		return "", fmt.Errorf("Agnes API未配置")
	}

	// Ensure public URLs for frames
	firstURL, err := ensurePublicURL(client, firstFrameURL, prompt+" first frame")
	if err != nil {
		return "", fmt.Errorf("首帧处理失败: %v", err)
	}
	lastURL, err := ensurePublicURL(client, lastFrameURL, prompt+" last frame")
	if err != nil {
		return "", fmt.Errorf("尾帧处理失败: %v", err)
	}

	segs := splitDuration(totalDuration)
	if len(segs) == 0 {
		return "", fmt.Errorf("时长参数无效")
	}

	n := len(segs)
	segPaths := make([]string, n)
	errs := make([]error, n)

	for i, segDur := range segs {
		if i > 0 {
			time.Sleep(5 * time.Second)
		}
		segPath := filepath.Join(tmpDir, fmt.Sprintf("kf_seg_%03d.mp4", i))

		// Build a distinct, user-derived prompt for this segment so segments
		// stay relevant to the user's text while each being different. See
		// segmentStagePrompt for details.
		segPrompt := segmentStagePrompt(prompt, i, n)

		params := VideoTaskParams{
			Mode:        "keyframe",
			Seconds:     clampSeconds(segDur),
			AspectRatio: aspectRatio,
			Prompt:      segPrompt,
		}
		// Pass a single reference frame per segment (first frame for the opening
		// segment, last frame for the closing segment) rather than the same paired
		// frames to every segment. Passing the same (first,last) pair to every
		// segment makes Agnes interpolate the identical transition in each, so all
		// segments come out looking the same. A single reference frame plus a
		// stage-specific prompt keeps each segment visually distinct.
		switch {
		case i == 0 && firstURL != "":
			params.FirstFrame = firstURL
		case i == n-1 && lastURL != "":
			params.LastFrame = lastURL
		default:
			// Middle segments use pure text mode (no reference frame). Reusing
			// the first frame as a reference here makes Agnes produce near-identical
			// output to the opening segment, since the reference frame dominates
			// the generated image and the prompt alone cannot override it.
			params.Mode = "text"
		}

		err := client.generateVideoSegment(segPath, params, fmt.Sprintf("kf-%d", i+1))
		if err != nil {
			errs[i] = fmt.Errorf("第%d段生成失败: %v", i+1, err)
			continue
		}
		segPaths[i] = segPath
	}

	var paths []string
	var firstErr error
	for i := range segs {
		if errs[i] != nil {
			if firstErr == nil {
				firstErr = errs[i]
			}
			continue
		}
		if segPaths[i] != "" {
			paths = append(paths, segPaths[i])
		}
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("所有分段生成失败: %v", firstErr)
	}
	if firstErr != nil && len(paths) < len(segs) {
		fmt.Fprintf(os.Stderr, "[PartialVideo] 部分分段失败(%v)，使用%d/%d段拼接\n", firstErr, len(paths), len(segs))
	}
	if len(paths) == 1 {
		return paths[0], nil
	}
	return concatVideos(tmpDir, paths, dest)
}

// MakeLongRefVideoAI generates a long reference-guided video by splitting
// into segments, all using the same reference image URLs, then concatenating.
func MakeLongRefVideoAI(client *AIClient, tmpDir, prompt string, imageURLs []string, totalDuration int, aspectRatio string) (string, error) {
	dest := filepath.Join(tmpDir, "long_ref_video.mp4")
	if !client.HasAgnes() {
		return "", fmt.Errorf("Agnes API未配置")
	}
	if len(imageURLs) == 0 {
		return "", fmt.Errorf("无有效参考图片")
	}
	if len(imageURLs) > 5 {
		imageURLs = imageURLs[:5]
	}

	// Ensure all image URLs are public
	publicURLs := make([]string, 0, len(imageURLs))
	for i, imgURL := range imageURLs {
		publicURL, err := ensurePublicURL(client, imgURL, fmt.Sprintf("reference image %d for %s", i+1, prompt))
		if err != nil {
			continue
		}
		publicURLs = append(publicURLs, publicURL)
	}
	if len(publicURLs) == 0 {
		return "", fmt.Errorf("无有效参考图片")
	}

	refPrefix := "Use <Picture 1> as reference. "
	segs := splitDuration(totalDuration)
	if len(segs) == 0 {
		return "", fmt.Errorf("时长参数无效")
	}

	n := len(segs)
	segPaths := make([]string, n)
	errs := make([]error, n)

	for i, segDur := range segs {
		if i > 0 {
			time.Sleep(5 * time.Second)
		}
		segPath := filepath.Join(tmpDir, fmt.Sprintf("ref_seg_%03d.mp4", i))

		err := client.generateVideoSegment(segPath, VideoTaskParams{
			Prompt:      refPrefix + segmentStagePrompt(prompt, i, n),
			Mode:        "reference",
			Seconds:     clampSeconds(segDur),
			AspectRatio: aspectRatio,
			Images:      publicURLs,
		}, fmt.Sprintf("ref-%d", i+1))
		if err != nil {
			errs[i] = fmt.Errorf("第%d段生成失败: %v", i+1, err)
			continue
		}
		segPaths[i] = segPath
	}

	var paths []string
	var firstErr error
	for i := range segs {
		if errs[i] != nil {
			if firstErr == nil {
				firstErr = errs[i]
			}
			continue
		}
		if segPaths[i] != "" {
			paths = append(paths, segPaths[i])
		}
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("所有分段生成失败: %v", firstErr)
	}
	if firstErr != nil && len(paths) < len(segs) {
		fmt.Fprintf(os.Stderr, "[PartialVideo] 部分分段失败(%v)，使用%d/%d段拼接\n", firstErr, len(paths), len(segs))
	}
	if len(paths) == 1 {
		return paths[0], nil
	}
	return concatVideos(tmpDir, paths, dest)
}
func MakeTextVideoAI(client *AIClient, tmpDir, prompt string, duration int, aspectRatio string) (string, error) {
	dest := filepath.Join(tmpDir, "video.mp4")
	if !client.HasAgnes() {
		return "", fmt.Errorf("Agnes API未配置")
	}

	videoID, err := client.CreateVideoTask(VideoTaskParams{
		Prompt:      prompt,
		Mode:        "text",
		Seconds:     clampSeconds(duration),
		AspectRatio: aspectRatio,
	})
	if err != nil {
		return "", err
	}

	videoURL, err := client.PollVideoTask(videoID, 30*time.Minute)
	if err != nil {
		return "", err
	}

	if err := client.DownloadVideo(videoURL, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// MakeKeyframeVideoAI generates a keyframe-interpolated video via Agnes Video 2.5 Flash.
// firstFrameURL and lastFrameURL must be publicly accessible HTTP(S) URLs or base64 data.
func MakeKeyframeVideoAI(client *AIClient, tmpDir, firstFrameURL, lastFrameURL, prompt string, duration int, aspectRatio string) (string, error) {
	dest := filepath.Join(tmpDir, "keyframe_video.mp4")
	if !client.HasAgnes() {
		return "", fmt.Errorf("Agnes API未配置")
	}

	// If URLs are localhost or private, try to generate public URLs via image API
	firstURL, err := ensurePublicURL(client, firstFrameURL, prompt+" first frame")
	if err != nil {
		return "", fmt.Errorf("首帧处理失败: %v", err)
	}
	lastURL, err := ensurePublicURL(client, lastFrameURL, prompt+" last frame")
	if err != nil {
		return "", fmt.Errorf("尾帧处理失败: %v", err)
	}

	videoID, err := client.CreateVideoTask(VideoTaskParams{
		Prompt:      prompt,
		Mode:        "keyframe",
		Seconds:     clampSeconds(duration),
		AspectRatio: aspectRatio,
		FirstFrame:  firstURL,
		LastFrame:   lastURL,
	})
	if err != nil {
		return "", err
	}

	videoURL, err := client.PollVideoTask(videoID, 30*time.Minute)
	if err != nil {
		return "", err
	}

	if err := client.DownloadVideo(videoURL, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// ensurePublicURL converts a URL or data URI to a public Agnes-hosted URL if needed.
// Returns the original input if it's already a public URL. The check resolves
// the hostname and classifies every resolved IP, so URLs whose path merely
// contains digits like "10." are no longer misclassified as private, while
// docker-bridge/link-local hosts are reliably caught.
func ensurePublicURL(client *AIClient, input, genPrompt string) (string, error) {
	if input == "" {
		return "", nil
	}
	// Pass through only URLs that parse as http(s) and resolve to public IPs.
	if u, err := url.Parse(input); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		if err := checkHost(u.Hostname()); err == nil {
			return input, nil
		}
	}
	// For localhost/private/unresolvable URLs or data URIs, generate via image API
	imgURL, _, err := client.GenImageAgnes("agnes-image-2.1-flash", genPrompt, "1K", "16:9", nil)
	if err != nil {
		return "", err
	}
	return imgURL, nil
}

// MakeRefVideoAI generates a reference-guided video via Agnes Video 2.5 Flash.
func MakeRefVideoAI(client *AIClient, tmpDir, prompt string, imageURLs []string, duration int, aspectRatio string) (string, error) {
	dest := filepath.Join(tmpDir, "ref_video.mp4")
	if !client.HasAgnes() {
		return "", fmt.Errorf("Agnes API未配置")
	}
	if len(imageURLs) == 0 {
		return "", fmt.Errorf("无有效参考图片")
	}
	if len(imageURLs) > 5 {
		imageURLs = imageURLs[:5]
	}

	// Ensure all image URLs are public
	publicURLs := make([]string, 0, len(imageURLs))
	for i, imgURL := range imageURLs {
		publicURL, err := ensurePublicURL(client, imgURL, fmt.Sprintf("reference image %d for %s", i+1, prompt))
		if err != nil {
			continue
		}
		publicURLs = append(publicURLs, publicURL)
	}
	if len(publicURLs) == 0 {
		return "", fmt.Errorf("无有效参考图片")
	}

	refPrompt := fmt.Sprintf("Use <Picture 1> as reference. %s", prompt)
	videoID, err := client.CreateVideoTask(VideoTaskParams{
		Prompt:      refPrompt,
		Mode:        "reference",
		Seconds:     clampSeconds(duration),
		AspectRatio: aspectRatio,
		Images:      publicURLs,
	})
	if err != nil {
		return "", err
	}

	videoURL, err := client.PollVideoTask(videoID, 30*time.Minute)
	if err != nil {
		return "", err
	}

	if err := client.DownloadVideo(videoURL, dest); err != nil {
		return "", err
	}
	return dest, nil
}
