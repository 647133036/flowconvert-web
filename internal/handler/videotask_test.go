package handler

import (
	"testing"
	"time"
)

// 已结束任务：TTL 从完成时刻起算，过期后被回收
func TestVideoJobGCExpiredAfterDone(t *testing.T) {
	s := NewVideoJobStore(0) // TTL=0：结束后立即过期
	j := s.Create()
	s.SetComplete(j.ID, "/api/download/x.mp4")
	time.Sleep(5 * time.Millisecond)
	s.gc()
	if s.Get(j.ID) != nil {
		t.Fatal("已完成且过期的任务应被回收")
	}
}

// 运行中任务：不按创建时间 TTL 回收（AI 视频生成可能超过 30 分钟）
func TestVideoJobGCKeepsRunning(t *testing.T) {
	s := NewVideoJobStore(0)
	j := s.Create()
	time.Sleep(5 * time.Millisecond)
	s.gc()
	if s.Get(j.ID) == nil {
		t.Fatal("运行中任务不应被 TTL 回收")
	}
}

// 完成后的任务在 TTL 内仍然可查（用户晚回来也能看到结果）
func TestVideoJobCompletedVisibleWithinTTL(t *testing.T) {
	s := NewVideoJobStore(2 * time.Hour)
	j := s.Create()
	s.SetComplete(j.ID, "/api/download/x.mp4")
	s.gc()
	got := s.Get(j.ID)
	if got == nil || got.Status != "completed" || got.DownloadURL == "" {
		t.Fatal("TTL 内已完成任务应可查询")
	}
}
