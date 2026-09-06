package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

func MakeTextVideo(tmpDir, prompt, aspectRatio string, duration int) (string, error) {
	if duration <= 0 {
		duration = 3
	}
	if duration > 60 {
		duration = 60
	}
	dest := filepath.Join(tmpDir, "video.mp4")
	payloadPath := filepath.Join(tmpDir, "video_payload.json")
	payload, err := marshalVideoPayload(map[string]interface{}{
		"prompt":       prompt,
		"duration":     duration,
		"aspect_ratio": aspectRatio,
	})
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
//
// For segments after the first, which are generated in keyframe mode anchored
// to the previous segment's last frame, the prompt also demands a strict
// continuation of character, wardrobe, setting and lighting. Without that
// directive the model re-samples those elements even when a first frame is
// supplied, which is what made long videos jump between scenes at each seam.
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
	base := fmt.Sprintf("%s。本段聚焦：%s。叙事：%s", prompt, focus, stage)
	if i == 0 {
		return base + "。画面以一个明确动作开场并自然推进发展，运镜平稳，避免同一动作反复循环"
	}
	return base + "。严格延续上一段画面：保持同一人物、同一服装、同一场景、同一光影与同一色调，动作自然接续并持续推进、避免反复循环，不要重新生成新场景"
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

// probeFPS returns the video stream frame rate from a file, 0 on failure.
func probeFPS(path string) float64 {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=r_frame_rate", "-of", "csv=p=0", path).CombinedOutput()
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(out))
	if s == "" || s == "0/0" {
		return 0
	}
	var num, den float64
	if _, err := fmt.Sscanf(s, "%f/%f", &num, &den); err != nil || den == 0 {
		return 0
	}
	if r := num / den; r >= 1 {
		return math.Ceil(r)
	}
	return 0
}

// cfrArgs returns the ffmpeg flags for constant-frame-rate output.
//
// -fps_mode was introduced in ffmpeg 5.0 and -vsync was removed in 7.0, so
// neither flag is universally available. Probe the installed version once and
// pick the spelling the binary actually understands.
var (
	cfrArgsOnce sync.Once
	cfrArgsOut  = []string{"-fps_mode", "cfr"}
)

func cfrArgs() []string {
	cfrArgsOnce.Do(func() {
		out, err := exec.Command("ffmpeg", "-version").Output()
		if err != nil {
			return
		}
		var major int
		if _, err := fmt.Sscanf(string(out), "ffmpeg version %d.", &major); err != nil {
			return
		}
		if major < 5 {
			cfrArgsOut = []string{"-vsync", "cfr"}
		}
	})
	return cfrArgsOut
}

// concatVideos merges multiple MP4 segments.
//
// Segments come from independent generation jobs, so their resolution, frame
// rate, GOP layout and start PTS all differ. Stream-copying them ("c copy")
// writes those mismatches straight into the file: a player can only decode
// from the nearest keyframe, so the junction shows backward-going timestamps
// and dropped frames. Measured with a 30fps/180-frame and a 24fps/144-frame
// pair, plain "c copy" produced 15 negative PTS intervals.
//
// Normalizing only the concat output is not enough either — the CFR filter
// drops frames when inputs run at different rates (324 input frames became
// 290). Normalize each segment first to a common resolution, frame rate and
// zero-based timestamps, then re-encode the concat. That preserves every
// frame (360 at 30fps = 12.000s exactly) with no negative PTS.
func concatVideos(tmpDir string, segPaths []string, dest string) (string, error) {
	w, h, probeErr := probeResolution(segPaths[0])
	if probeErr != nil {
		return "", fmt.Errorf("视频拼接失败: 无法获取分辨率: %v", probeErr)
	}
	fps := probeFPS(segPaths[0])
	if fps == 0 {
		fps = 30
	}
	fpsStr := fmt.Sprintf("%.0f", fps)
	vf := fmt.Sprintf("scale=%d:%d,setsar=1", w, h)

	normPaths := make([]string, 0, len(segPaths))
	for i, p := range segPaths {
		norm := filepath.Join(tmpDir, fmt.Sprintf("norm_%03d.mp4", i))
		normArgs := append([]string{"-y", "-i", p, "-vf", vf, "-r", fpsStr}, cfrArgs()...)
		normArgs = append(normArgs,
			"-c:v", "libx264", "-preset", "fast", "-crf", "23",
			"-pix_fmt", "yuv420p",
			"-c:a", "aac",
			"-start_time", "0", norm)
		out, err := RunCmdTimeout(5*time.Minute, "ffmpeg", normArgs...)
		if err != nil {
			return "", fmt.Errorf("视频分段归一化失败: %s", strings.TrimSpace(out))
		}
		normPaths = append(normPaths, norm)
	}

	listPath, err := writeConcatList(tmpDir, normPaths)
	if err != nil {
		return "", err
	}

	concatArgs := append([]string{"-y", "-fflags", "+genpts",
		"-f", "concat", "-safe", "0", "-i", listPath,
		"-vf", vf, "-r", fpsStr}, cfrArgs()...)
	concatArgs = append(concatArgs,
		"-c:v", "libx264", "-preset", "fast", "-crf", "23",
		"-pix_fmt", "yuv420p",
		"-start_time", "0",
		"-c:a", "aac",
		"-movflags", "+faststart", dest)
	out, err := RunCmdTimeout(10*time.Minute, "ffmpeg", concatArgs...)
	if err != nil {
		return "", fmt.Errorf("视频拼接失败: %s", strings.TrimSpace(out))
	}
	if _, err := os.Stat(dest); err != nil {
		return "", fmt.Errorf("拼接输出文件不存在")
	}
	return dest, nil
}

// writeConcatList writes an ffmpeg concat-demuxer list file.
func writeConcatList(tmpDir string, paths []string) (string, error) {
	listPath := filepath.Join(tmpDir, "concat_list.txt")
	var b strings.Builder
	for _, p := range paths {
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
	return listPath, nil
}

// extractLastFrameDataURI pulls the final frame of a video into a JPEG data URI
// for use as the Agnes keyframe first_frame of the following segment. This is
// what makes consecutive segments share one character, wardrobe and setting:
// each segment is generated from the picture where the previous one ended.
func extractLastFrameDataURI(srcPath, tmpDir string, idx int) (string, error) {
	if srcPath == "" {
		return "", nil
	}
	framePath := filepath.Join(tmpDir, fmt.Sprintf("seg_%03d_lastframe.jpg", idx))
	out, err := RunCmdTimeout(60*time.Second, "ffmpeg",
		"-y", "-v", "error", "-sseof", "-0.2", "-i", srcPath,
		"-frames:v", "1", "-q:v", "3", framePath)
	if err != nil {
		return "", fmt.Errorf("提取末帧失败: %s", strings.TrimSpace(out))
	}
	if _, err := os.Stat(framePath); err != nil {
		return "", fmt.Errorf("末帧文件不存在")
	}
	dataURI, err := FileToDataURI(framePath)
	if err != nil {
		return "", fmt.Errorf("末帧编码失败: %v", err)
	}
	return dataURI, nil
}

// MakeLongTextVideoAI generates a long video by splitting the requested
// duration into sub-12s segments, generating each via Agnes 2.5 Flash, then
// concatenating with ffmpeg.
//
// Segments are chained rather than generated independently: segment 0 is a
// plain text generation, and every later segment runs in keyframe mode with
// segment i-1's last frame as its first frame. Generating each segment from
// text alone made Agnes re-invent the character's appearance, wardrobe and
// location on every 10s boundary, which is why the output looked like a hard
// cut in the middle of one scene.
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
			time.Sleep(agnesInterSegmentDelay)
		}
		segPath := filepath.Join(tmpDir, fmt.Sprintf("seg_%03d.mp4", i))

		params := VideoTaskParams{
			Prompt:      segmentStagePrompt(prompt, i, len(segs)),
			Seconds:     clampSeconds(segDur),
			AspectRatio: aspectRatio,
		}
		if i == 0 {
			params.Mode = "text"
		} else {
			params.Mode = "keyframe"
			if prev := segPaths[i-1]; prev != "" {
				if first, frameErr := extractLastFrameDataURI(prev, tmpDir, i-1); frameErr != nil {
					// Losing the anchor must not kill the whole video: fall back
					// to a text generation so at least this segment is produced.
					fmt.Fprintf(os.Stderr, "[LongVideo] 提取第%d段末帧失败，本段退化为文生视频: %v\n", i, frameErr)
				} else {
					params.FirstFrame = first
				}
			}
		}

		err := client.generateVideoSegment(segPath, params, fmt.Sprintf("text-%d", i+1))
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
			time.Sleep(agnesInterSegmentDelay)
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
			time.Sleep(agnesInterSegmentDelay)
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
	imgURL, _, err := client.GenImageAgnes(agnesImageModel, genPrompt, "1K", "16:9", nil)
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
