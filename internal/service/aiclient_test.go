package service

import (
	"errors"
	"testing"
)

func TestIsTransientVideoErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"diff generator no result", errors.New("DiffGenerator returned no result"), true},
		{"bare no result", errors.New("generation no result"), true},
		{"upstream upload failed", errors.New("视频生成失败: upload failed"), true},
		{"transport eof", errors.New("unexpected EOF"), true},
		{"rate limit 429", errors.New("429 rate limited"), true},
		{"rate_limit token", errors.New("rate_limit exceeded"), true},
		{"rate limit words", errors.New("rate limit hit"), true},
		{"server 503", errors.New("503 Service Unavailable"), true},
		{"queue full", errors.New("video_queue_full"), true},
		{"invalid token 401", errors.New("HTTP 401: 无效的令牌"), false},
		{"bad request 400", errors.New("HTTP 400: bad request"), false},
		{"invalid prompt", errors.New("invalid prompt"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientVideoErr(tc.err); got != tc.want {
				t.Errorf("isTransientVideoErr(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
