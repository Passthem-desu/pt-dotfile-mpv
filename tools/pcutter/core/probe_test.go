package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeVideo 生成测试用视频：sources 交给 lavfi。
func makeVideo(t *testing.T, name, src string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", src,
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-y", p)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make video: %v: %s", err, out)
	}
	return p
}

func TestProbeCFR(t *testing.T) {
	p := makeVideo(t, "a.mp4", "testsrc2=size=320x240:rate=30:duration=2")
	info, err := Probe(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Count != 60 {
		t.Errorf("Count = %d, want 60", info.Count)
	}
	if !info.CFR {
		t.Errorf("CFR = false, want true (%+v)", info)
	}
	if info.Width != 320 || info.Height != 240 {
		t.Errorf("size = %dx%d", info.Width, info.Height)
	}
	if info.FPS != 30 {
		t.Errorf("fps = %v", info.FPS)
	}
	if got := info.FrameTime(30); got < 0.99 || got > 1.01 {
		t.Errorf("FrameTime(30) = %v, want 1.0", got)
	}
	if got := info.FrameAt(1.0); got != 30 {
		t.Errorf("FrameAt(1.0) = %d, want 30", got)
	}
}

func TestProbeRejectsNonVideo(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.mp4")
	if err := os.WriteFile(p, []byte("not a video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(p); err == nil {
		t.Fatal("Probe 应该报错")
	}
}

func TestExtractSizeAndCount(t *testing.T) {
	p := makeVideo(t, "b.mp4", "testsrc2=size=320x240:rate=30:duration=2")
	info, err := Probe(p)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := Extract(info, 10, 5, 160, 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 5 {
		t.Fatalf("got %d frames, want 5", len(frames))
	}
	if frames[0].Rect.Dx() != 160 || frames[0].Rect.Dy() != 120 {
		t.Fatalf("size %v", frames[0].Rect)
	}
	// 不同的帧应该有不同的像素（不然就是解码卡住了）
	same := true
	for i := 1; i < len(frames); i++ {
		if string(frames[0].Pix) != string(frames[i].Pix) {
			same = false
			break
		}
	}
	if same {
		t.Fatal("5 帧像素完全一样，解码没前进")
	}
}

func TestExtractStopsAtEnd(t *testing.T) {
	p := makeVideo(t, "c.mp4", "testsrc2=size=160x120:rate=30:duration=0.5")
	info, err := Probe(p)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := Extract(info, 0, 100, 80, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 15 {
		t.Fatalf("got %d frames, want 15", len(frames))
	}
}

func TestPreviewSize(t *testing.T) {
	cases := []struct{ w, h, wantW, wantH int }{
		{1920, 1080, 960, 540},
		{1080, 1920, 540, 960},
		{640, 360, 640, 360}, // 本来就小，不放大
		{1001, 501, 960, 480},
	}
	for _, c := range cases {
		gotW, gotH := PreviewSize(c.w, c.h)
		if gotW != c.wantW || gotH != c.wantH {
			t.Errorf("PreviewSize(%d,%d) = %d,%d want %d,%d", c.w, c.h, gotW, gotH, c.wantW, c.wantH)
		}
	}
}
