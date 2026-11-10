package core

import (
	"context"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func countFiles(t *testing.T, dir, glob string) int {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, glob))
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}

// 导出视频后，用 ffprobe 数真正解出来的帧数（VFR 下 nb_frames 不可信）。
func countDecodedFrames(t *testing.T, path string) int {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-count_frames", "-show_entries", "stream=nb_read_frames",
		"-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe count: %v", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	return n
}

func TestExportImagesWritesKeptFrames(t *testing.T) {
	src := makeVideo(t, "v.mp4", "testsrc2=size=160x120:rate=30:duration=1") // 30 帧
	info, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	tl := NewTimeline(info)
	tl.DeleteRange(5, 15) // 删 10 帧
	if tl.Count() != 20 {
		t.Fatalf("count = %d", tl.Count())
	}

	dir := t.TempDir()
	if err := ExportImages(context.Background(), info, tl, filepath.Join(dir, "f_%04d.png")); err != nil {
		t.Fatal(err)
	}
	if got := countFiles(t, dir, "f_*.png"); got != 20 {
		t.Fatalf("导出了 %d 个 PNG，want 20", got)
	}
	if got := countFiles(t, dir, "f_0001.png"); got != 1 {
		t.Fatal("编号应该从 1 开始")
	}
}

func TestExportVideoRespectsDeletions(t *testing.T) {
	src := makeVideo(t, "v.mp4", "testsrc2=size=160x120:rate=30:duration=1") // 30 帧
	info, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	tl := NewTimeline(info)
	tl.DeleteRange(0, 5)
	tl.DeleteRange(10, 12)

	out := filepath.Join(t.TempDir(), "out.mp4")
	if err := ExportVideo(context.Background(), info, tl, out, 28); err != nil {
		t.Fatal(err)
	}
	want := 30 - 7
	if got := countDecodedFrames(t, out); got != want {
		t.Fatalf("导出视频有 %d 帧，want %d", got, want)
	}
}

func TestExportVideoWithoutDeletions(t *testing.T) {
	src := makeVideo(t, "v.mp4", "testsrc2=size=160x120:rate=30:duration=0.5") // 15 帧
	info, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.mp4")
	if err := ExportVideo(context.Background(), info, NewTimeline(info), out, 28); err != nil {
		t.Fatal(err)
	}
	if got := countDecodedFrames(t, out); got != 15 {
		t.Fatalf("got %d frames, want 15", got)
	}
}

func TestExportTile(t *testing.T) {
	// 用 45 帧、640x360 的片子：这种「删中间几帧」的素材会让 rawvideo 的
	// 默认 CFR 输出补帧（45 帧而不是 43），正好是这个回归测试要抓的。
	src := makeVideo(t, "v.mp4", "testsrc2=size=640x360:rate=30:duration=1.5")
	info, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	tl := NewTimeline(info)
	tl.DeleteRange(5, 7) // 剩 43 帧

	out := filepath.Join(t.TempDir(), "tile.png")
	if err := ExportTile(context.Background(), info, tl, out, 80); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	tw, th := 80, 44 // 640x360 缩到宽 80
	cols, rows := TileGrid(43, tw, th)
	b := img.Bounds()
	if b.Dx() != cols*tw || b.Dy() != rows*th {
		t.Fatalf("拼贴图 %dx%d，want %dx%d", b.Dx(), b.Dy(), cols*tw, rows*th)
	}

	// 相邻格子必须不一样。rawvideo 输出默认按 CFR 补帧的话，
	// select 删出来的空洞会被重复帧填满，格子就会两两相同。
	rgba := image.NewRGBA(b)
	draw.Draw(rgba, b, img, b.Min, draw.Src)
	cell := func(i int) []byte {
		r := image.Rect((i%cols)*tw, (i/cols)*th, (i%cols+1)*tw, (i/cols+1)*th)
		out := make([]byte, 0, tw*th*4)
		for y := r.Min.Y; y < r.Max.Y; y++ {
			out = append(out, rgba.Pix[rgba.PixOffset(r.Min.X, y):rgba.PixOffset(r.Max.X, y)]...)
		}
		return out
	}
	for i := 0; i+1 < 43; i++ {
		if string(cell(i)) == string(cell(i+1)) {
			t.Fatalf("第 %d 格和第 %d 格一模一样，说明帧没前进（CFR 补帧？）", i, i+1)
		}
	}
}

func TestExportTileRejectsHugeSheet(t *testing.T) {
	info := Info{Path: "x", Width: 1920, Height: 1080, FPS: 30, Count: 20000, CFR: true}
	tl := NewTimeline(info)
	err := ExportTile(context.Background(), info, tl, filepath.Join(t.TempDir(), "t.png"), 200)
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("应该拒绝过大的拼贴图，got %v", err)
	}
}

// 中止导出：ctx 取消后 ffmpeg 要尽快被杀掉，并且返回的是 context.Canceled。
func TestExportCancelled(t *testing.T) {
	src := makeVideo(t, "v.mp4", "testsrc2=size=640x480:rate=30:duration=2")
	info, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 一开始就取消
	dir := t.TempDir()
	err = ExportImages(ctx, info, NewTimeline(info), filepath.Join(dir, "x_%04d.png"))
	if err == nil {
		t.Fatal("取消之后应该报错")
	}
	if got := countFiles(t, dir, "x_*.png"); got != 0 {
		t.Fatalf("取消之后不该产出文件，got %d", got)
	}
}

func TestTileSize(t *testing.T) {
	info := Info{Width: 1920, Height: 1080}

	// 默认是原尺寸：每格 1920x1080，一格不多一格不少
	w, h := TileSize(info, 9, 0)
	if w%1920 != 0 || h%1080 != 0 {
		t.Fatalf("9 帧原尺寸 = %dx%d，每格应该是 1920x1080", w, h)
	}
	if n := (w / 1920) * (h / 1080); n < 9 {
		t.Fatalf("%dx%d 只装得下 %d 格", w, h, n)
	}
	if float64(max(w, h))/float64(min(w, h)) > 1.7 {
		t.Fatalf("%dx%d 不够方正", w, h)
	}

	// 正方形视频，9 帧就该是 3x3
	sq := Info{Width: 1080, Height: 1080}
	if w, h := TileSize(sq, 9, 0); w != 3240 || h != 3240 {
		t.Fatalf("9 帧方形原尺寸 = %dx%d，want 3240x3240", w, h)
	}

	// 给了宽度就按比例缩
	w, h = TileSize(info, 9, 480)
	if w%480 != 0 {
		t.Fatalf("给宽度时每格应该正好 480，got 总宽 %d", w)
	}
	th := 480 * 1080 / 1920
	if h%th != 0 {
		t.Fatalf("总高 %d 不是每格高 %d 的倍数", h, th)
	}

	// 50 帧 1080p 原尺寸也要放得下（约 124MP）
	w, h = TileSize(info, 50, 0)
	if w*h > MaxTilePixels {
		t.Fatalf("50 帧原尺寸 = %dx%d (%d MP) 超了上限", w, h, w*h>>20)
	}
}

func TestTileDimsIsOneToOne(t *testing.T) {
	info := Info{Width: 1920, Height: 1080}
	if w, h := tileDims(info, 0); w != 1920 || h != 1080 {
		t.Fatalf("默认应该是原尺寸，got %dx%d", w, h)
	}
	// 比源还宽也不放大
	if w, h := tileDims(info, 4000); w != 1920 || h != 1080 {
		t.Fatalf("不该放大，got %dx%d", w, h)
	}
}

// 用户要的：精灵图每格就是原始分辨率，1:1 不缩放。
func TestExportTileKeepsSourceResolution(t *testing.T) {
	src := makeVideo(t, "sq.mp4", "testsrc2=size=480x480:rate=30:duration=0.3") // 9 帧
	info, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	if info.Count != 9 {
		t.Fatalf("fixture 是 %d 帧，want 9", info.Count)
	}
	out := filepath.Join(t.TempDir(), "tile.png")
	if err := ExportTile(context.Background(), info, NewTimeline(info), out, 0); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	// 9 帧 480x480 -> 3x3 -> 1440x1440，每格正好 480
	if img.Width != 1440 || img.Height != 1440 {
		t.Fatalf("精灵图 %dx%d，want 1440x1440（每格 480x480 原尺寸）", img.Width, img.Height)
	}
}

// 重复帧要真的出现在输出里：4 帧的片子把第 4 帧复制一份 -> 5 帧，
// 而且倒数两张图必须一模一样。
func TestExportImagesWithDuplicatedFrame(t *testing.T) {
	src := makeVideo(t, "v.mp4", "testsrc2=size=160x120:rate=30:duration=0.2") // 6 帧
	info, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	tl := NewTimeline(info)
	tl.Insert(tl.Count(), []int{5, 5}) // 末尾再放两遍第 5 帧
	if tl.Count() != 8 {
		t.Fatalf("Count = %d", tl.Count())
	}

	dir := t.TempDir()
	if err := ExportImages(context.Background(), info, tl, filepath.Join(dir, "f_%04d.png")); err != nil {
		t.Fatal(err)
	}
	if got := countFiles(t, dir, "f_*.png"); got != 8 {
		t.Fatalf("导出了 %d 个 PNG，want 8", got)
	}
	// 最后三张应该完全相同（同一帧复制出来的）
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a, b, c := read("f_0006.png"), read("f_0007.png"), read("f_0008.png")
	if string(a) != string(b) || string(b) != string(c) {
		t.Fatal("复制出来的帧应该和原帧逐像素一致")
	}
	// 但第 5 张（第 5 帧）不该和它们一样
	if string(read("f_0005.png")) == string(a) {
		t.Fatal("第 5 和第 6 张不该相同")
	}
}

// 有重复帧时导出视频也要按序列长度来。
func TestExportVideoWithDuplicatedFrame(t *testing.T) {
	src := makeVideo(t, "v.mp4", "testsrc2=size=160x120:rate=30:duration=0.5") // 15 帧
	info, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	tl := NewTimeline(info)
	tl.DeleteRange(0, 5)         // 10 帧
	tl.Insert(5, []int{5, 5, 5}) // +3 -> 13 帧
	if tl.Count() != 13 {
		t.Fatalf("Count = %d", tl.Count())
	}
	out := filepath.Join(t.TempDir(), "out.mp4")
	if err := ExportVideo(context.Background(), info, tl, out, 28); err != nil {
		t.Fatal(err)
	}
	if got := countDecodedFrames(t, out); got != 13 {
		t.Fatalf("导出视频 %d 帧，want 13", got)
	}
}

// 拼贴图也必须能处理重复帧：每格都得有画面，不能留透明格子。
func TestExportTileWithDuplicatedFrames(t *testing.T) {
	src := makeVideo(t, "sq.mp4", "testsrc2=size=160x160:rate=30:duration=0.3") // 9 帧
	info, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	tl := NewTimeline(info)
	// 678678678 那种：把三帧重复三遍 -> 9 行
	tl.DeleteRange(0, 3)
	tl.DeleteRange(3, 6)                  // 只剩 6,7,8 三帧
	tl.Insert(3, []int{6, 7, 8, 6, 7, 8}) // 6,7,8,6,7,8,6,7,8
	if tl.Count() != 9 {
		t.Fatalf("Count = %d", tl.Count())
	}
	if tl.Monotonic() {
		t.Fatal("这个序列有重复帧")
	}

	out := filepath.Join(t.TempDir(), "tile.png")
	if err := ExportTile(context.Background(), info, tl, out, 0); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	cols, rows := TileGrid(9, 160, 160)
	if b.Dx() != cols*160 || b.Dy() != rows*160 {
		t.Fatalf("拼贴图 %dx%d，want %dx%d", b.Dx(), b.Dy(), cols*160, rows*160)
	}
	// 每一个格子的中心都必须是不透明的 —— 之前 select 只会填前 3 格，其余全透明
	for i := 0; i < 9; i++ {
		cx := (i%cols)*160 + 80
		cy := (i/cols)*160 + 80
		if _, _, _, a := img.At(cx, cy).RGBA(); a == 0 {
			t.Fatalf("第 %d 格（%d,%d）是透明的", i, cx, cy)
		}
	}
}
