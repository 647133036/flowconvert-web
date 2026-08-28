package service

import (
	"testing"
)

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
