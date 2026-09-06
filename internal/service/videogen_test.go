package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestConcatVideosMergesMismatchedSegments(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}

	dir := t.TempDir()

	// Two segments with different resolution, frame rate and GOP layout,
	// mimicking independently generated AI segments.
	mk := func(name, size string, fps, dur, gop int) (string, int) {
		path := filepath.Join(dir, name)
		cmd := exec.CommandContext(ctxWithTimeout(t, 3*time.Minute), "ffmpeg", "-y", "-v", "error",
			"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%s:rate=%d:duration=%d", size, fps, dur),
			"-r", strconv.Itoa(fps), "-c:v", "libx264", "-preset", "fast",
			"-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-sc_threshold", "0",
			"-pix_fmt", "yuv420p", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("segment %s failed: %v %s", name, err, out)
		}
		n, err := countVideoFrames(path)
		if err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		return path, n
	}

	a, na := mk("a.mp4", "1280x720", 30, 6, 60)
	b, nb := mk("b.mp4", "1216x704", 24, 6, 32)
	if na == 0 || nb == 0 {
		t.Fatalf("empty segment: a=%d b=%d", na, nb)
	}

	dest, err := concatVideos(dir, []string{a, b}, filepath.Join(dir, "out.mp4"))
	if err != nil {
		t.Fatalf("concatVideos failed: %v", err)
	}

	// No input frame may be dropped. A 30fps+24fps pair must resolve to
	// 360 frames at the output rate, not the 290 a plain concat produces.
	total := float64(na)/30 + float64(nb)/24
	wantFrames := int(math.Round(total * 30))
	got, err := countVideoFrames(dest)
	if err != nil {
		t.Fatalf("count output frames: %v", err)
	}
	if got != wantFrames {
		t.Errorf("frame count = %d, want %d (input %d@30 + %d@24)", got, wantFrames, na, nb)
	}

	// Backward PTS at the splice is the frame-jump symptom: stream-copying
	// mismatched segments produced 15 of them.
	neg, err := countBackwardPTS(dest)
	if err != nil {
		t.Fatalf("probe pts: %v", err)
	}
	if neg != 0 {
		t.Errorf("concat produced %d backward PTS intervals at the splice", neg)
	}

	dur, err := videoDuration(dest)
	if err != nil {
		t.Fatalf("probe duration: %v", err)
	}
	if math.Abs(dur-total) > 0.2 {
		t.Errorf("duration = %.3fs, want ~%.3fs", dur, total)
	}
}

func ctxWithTimeout(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func countVideoFrames(path string) (int, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-count_frames", "-show_entries", "stream=nb_read_frames", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

func videoDuration(path string) (float64, error) {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

func countBackwardPTS(path string) (int, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "frame=pts_time", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0, err
	}
	neg, prev, first := 0, 0.0, true
	for _, line := range strings.Split(string(out), "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		if !first && v < prev {
			neg++
		}
		prev, first = v, false
	}
	return neg, nil
}

func TestClampSeconds(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want string
	}{
		{"below_min", 1, "4"},
		{"min", 4, "4"},
		{"mid", 8, "8"},
		{"max", 12, "12"},
		{"above_max", 60, "12"},
		{"zero", 0, "4"},
		{"negative", -5, "4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clampSeconds(tt.in)
			if got != tt.want {
				t.Errorf("clampSeconds(%d) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestSplitDuration(t *testing.T) {
	tests := []struct {
		name   string
		total  int
		want   []int
		checks func(t *testing.T, segs []int)
	}{
		{
			name:  "below_minimum_segment",
			total: 3,
			checks: func(t *testing.T, segs []int) {
				if len(segs) != 1 {
					t.Errorf("expected 1 segment, got %d", len(segs))
				}
				if segs[0] != 4 {
					t.Errorf("expected segment=4, got %d", segs[0])
				}
			},
		},
		{
			name:  "exactly_4",
			total: 4,
			checks: func(t *testing.T, segs []int) {
				if len(segs) != 1 {
					t.Errorf("expected 1 segment, got %d", len(segs))
				}
			},
		},
		{
			name:  "within_single_segment",
			total: 8,
			checks: func(t *testing.T, segs []int) {
				if len(segs) != 1 {
					t.Errorf("expected 1 segment for 8s, got %d", len(segs))
				}
				if segs[0] != 8 {
					t.Errorf("expected 8, got %d", segs[0])
				}
			},
		},
		{
			name:  "exactly_12",
			total: 12,
			checks: func(t *testing.T, segs []int) {
				if len(segs) != 1 {
					t.Errorf("expected 1 segment for 12s, got %d", len(segs))
				}
			},
		},
		{
			name:  "24_seconds_two_segments",
			total: 24,
			checks: func(t *testing.T, segs []int) {
				sum := 0
				for _, s := range segs {
					sum += s
				}
				if sum != 24 {
					t.Errorf("segments should sum to 24, got %d", sum)
				}
			},
		},
		{
			name:  "60_seconds",
			total: 60,
			checks: func(t *testing.T, segs []int) {
				sum := 0
				for _, s := range segs {
					sum += s
				}
				if sum != 60 {
					t.Errorf("segments should sum to 60, got %d", sum)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitDuration(tt.total)
			tt.checks(t, got)
		})
	}
}

func TestSplitDurationSum(t *testing.T) {
	for _, total := range []int{4, 5, 8, 12, 13, 24, 30, 60, 100} {
		segs := splitDuration(total)
		sum := 0
		for _, s := range segs {
			sum += s
		}
		if sum != total {
			t.Errorf("splitDuration(%d) segments sum to %d, want %d", total, sum, total)
		}
	}
}

func TestSplitPromptClauses(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   int
	}{
		{"chinese_comma_separated", "枫叶红在路的两边，两个人，回忆往事", 3},
		{"chinese_period", "天晴了。出门走走。心情不错。", 3},
		{"ascii_comma", "a cat, a dog, a bird", 3},
		{"ascii_period", "First. Second. Third.", 3},
		{"mixed", "你好，world。test!", 3},
		{"empty", "", 0},
		{"single", "just one phrase", 1},
		{"exclamation", "你好！世界！再见！", 3},
		{"question", "what？why？how？", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitPromptClauses(tt.prompt)
			if len(got) != tt.want {
				t.Errorf("splitPromptClauses(%q) = %d clauses, want %d: %v", tt.prompt, len(got), tt.want, got)
			}
		})
	}
}

func TestSplitPromptClausesTrimmed(t *testing.T) {
	segs := splitPromptClauses("  hello  ，  world  ")
	for _, s := range segs {
		if s != "hello" && s != "world" {
			t.Errorf("clause not trimmed: %q", s)
		}
	}
}

func TestSegmentStagePrompt(t *testing.T) {
	prompt := "枫叶红，两个人，回忆往事"
	n := 3

	prompts := make([]string, n)
	for i := 0; i < n; i++ {
		prompts[i] = segmentStagePrompt(prompt, i, n)
	}

	if prompts[0] == prompts[1] {
		t.Error("segment 0 and 1 should differ")
	}
	if prompts[1] == prompts[2] {
		t.Error("segment 1 and 2 should differ")
	}

	for _, p := range prompts {
		if p == "" {
			t.Error("segment prompt should not be empty")
		}
	}
}

func TestSegmentStagePromptStageLabels(t *testing.T) {
	prompt := "test"
	n := 4

	p0 := segmentStagePrompt(prompt, 0, n)
	if p0 == "" {
		t.Error("stage 0 prompt should not be empty")
	}
	pLast := segmentStagePrompt(prompt, n-1, n)
	if pLast == "" {
		t.Error("last stage prompt should not be empty")
	}
	pMid := segmentStagePrompt(prompt, 1, n)
	if pMid == "" {
		t.Error("mid stage prompt should not be empty")
	}
}

func TestSegmentStagePromptSingleClause(t *testing.T) {
	prompt := "just one clause"
	n := 3

	for i := 0; i < n; i++ {
		p := segmentStagePrompt(prompt, i, n)
		if p == "" {
			t.Errorf("segment %d prompt should not be empty", i)
		}
	}
}

func TestSegmentStagePromptCyclesClauses(t *testing.T) {
	prompt := "a，b，c"
	n := 6

	for i := 0; i < n; i++ {
		p := segmentStagePrompt(prompt, i, n)
		if p == "" {
			t.Errorf("segment %d should not be empty", i)
		}
	}
}

func TestMarshalVideoPayloadTrickyPrompts(t *testing.T) {
	tricky := []string{
		`quote " inside`,
		`backslash \ inside`,
		"newline \n inside",
		"tab \t inside",
		"carriage \r return",
		"unicode 中文 text",
		"line separator \u2028 and paragraph separator \u2029",
		"control \x00 \x01 \x1f chars",
		"``double backticks`` and 'single quotes'",
		strings.Repeat("a", 2000),
		"",
	}
	for _, p := range tricky {
		payload, err := marshalVideoPayload(map[string]interface{}{"prompt": p, "duration": 8})
		if err != nil {
			t.Fatalf("marshal failed for %q: %v", p, err)
		}
		if !json.Valid(payload) {
			t.Fatalf("payload is not valid JSON for %q: %s", p, payload)
		}
		var back map[string]interface{}
		if err := json.Unmarshal(payload, &back); err != nil {
			t.Fatalf("round-trip failed for %q: %v", p, err)
		}
		if back["prompt"] != p {
			t.Errorf("round-trip mismatch for %q: got %q", p, back["prompt"])
		}
	}
}

func TestMarshalVideoPayloadRefsArray(t *testing.T) {
	refs := []string{"/tmp/a b/photo 1.png", "/tmp/中文/图\"2\".png"}
	payload, err := marshalVideoPayload(map[string]interface{}{
		"prompt":   `prompt with "quotes"`,
		"refs":     refs,
		"duration": 12,
	})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !json.Valid(payload) {
		t.Fatalf("payload is not valid JSON: %s", payload)
	}
	var back map[string]interface{}
	if err := json.Unmarshal(payload, &back); err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	gotRefs, ok := back["refs"].([]interface{})
	if !ok || len(gotRefs) != len(refs) {
		t.Fatalf("refs round-trip mismatch: %v", back["refs"])
	}
	for i, r := range gotRefs {
		if r.(string) != refs[i] {
			t.Errorf("refs[%d] = %q, want %q", i, r.(string), refs[i])
		}
	}
}

func TestSegmentStagePromptContinuityDirective(t *testing.T) {
	prompt := "枫叶红，两个人，回忆往事"

	if got := segmentStagePrompt(prompt, 0, 3); strings.Contains(got, "严格延续上一段画面") {
		t.Errorf("first segment must not carry a continuity directive: %s", got)
	}

	for i := 1; i < 3; i++ {
		p := segmentStagePrompt(prompt, i, 3)
		for _, want := range []string{"严格延续上一段画面", "同一人物", "同一服装", "同一场景"} {
			if !strings.Contains(p, want) {
				t.Errorf("segment %d missing continuity requirement %q", i, want)
			}
		}
		if !strings.Contains(p, prompt) {
			t.Errorf("segment %d lost the user prompt: %s", i, p)
		}
	}
}

func TestSegmentStagePromptAntiLoopDirective(t *testing.T) {
	prompt := "一位穿红色连衣裙的年轻女子站在秋日枫树林间的小路上"

	if got := segmentStagePrompt(prompt, 0, 4); !strings.Contains(got, "避免同一动作反复循环") {
		t.Errorf("first segment must demand a non-looping action arc: %s", got)
	}

	for i := 1; i < 4; i++ {
		p := segmentStagePrompt(prompt, i, 4)
		if !strings.Contains(p, "避免反复循环") {
			t.Errorf("segment %d missing anti-loop directive: %s", i, p)
		}
	}
}

func TestExtractLastFrameDataURI(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	if out, err := exec.CommandContext(ctxWithTimeout(t, 3*time.Minute), "ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=2",
		"-r", "30", "-c:v", "libx264", "-preset", "fast", "-pix_fmt", "yuv420p", src).CombinedOutput(); err != nil {
		t.Fatalf("mkvideo failed: %v %s", err, out)
	}

	uri, err := extractLastFrameDataURI(src, dir, 0)
	if err != nil {
		t.Fatalf("extractLastFrameDataURI failed: %v", err)
	}
	if !strings.HasPrefix(uri, "data:image/jpeg;base64,") {
		t.Fatalf("unexpected data URI prefix: %.40s", uri)
	}
	if payload := strings.TrimPrefix(uri, "data:image/jpeg;base64,"); len(payload) < 5000 {
		t.Errorf("data URI payload too small (%d chars), frame may be blank", len(payload))
	}

	framePath := filepath.Join(dir, "seg_000_lastframe.jpg")
	jpg, err := os.ReadFile(framePath)
	if err != nil {
		t.Fatalf("frame file not written: %v", err)
	}
	if len(jpg) < 3 || jpg[0] != 0xFF || jpg[1] != 0xD8 || jpg[2] != 0xFF {
		t.Errorf("extracted frame is not a JPEG (magic % x)", jpg[:3])
	}

	// The seek must land on the final frame, not the first: testsrc2 paints a
	// different frame every tick, so the two endpoints cannot hash alike.
	lastPNG := filepath.Join(dir, "last.png")
	firstPNG := filepath.Join(dir, "first.png")
	if out, err := exec.CommandContext(ctxWithTimeout(t, time.Minute), "ffmpeg", "-y", "-v", "error",
		"-i", framePath, "-frames:v", "1", "-f", "image2", "-vf", "scale=160:-2", lastPNG).CombinedOutput(); err != nil {
		t.Fatalf("convert last frame: %v %s", err, out)
	}
	if out, err := exec.CommandContext(ctxWithTimeout(t, time.Minute), "ffmpeg", "-y", "-v", "error",
		"-i", src, "-frames:v", "1", "-f", "image2", "-vf", "scale=160:-2", firstPNG).CombinedOutput(); err != nil {
		t.Fatalf("convert first frame: %v %s", err, out)
	}
	first, err := os.ReadFile(firstPNG)
	if err != nil {
		t.Fatalf("read first png: %v", err)
	}
	last, err := os.ReadFile(lastPNG)
	if err != nil {
		t.Fatalf("read last png: %v", err)
	}
	if string(first) == string(last) {
		t.Error("extracted last frame is identical to the first frame; the -sseof seek is broken")
	}
}

func TestExtractLastFrameDataURIEmptyPath(t *testing.T) {
	if uri, err := extractLastFrameDataURI("", t.TempDir(), 0); err != nil || uri != "" {
		t.Errorf("empty path should be a no-op, got (%q, %v)", uri, err)
	}
}
