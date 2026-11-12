package core

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// SelectFilter 把「删掉了哪些帧」翻译成 ffmpeg 的 select 表达式。
// 只对「没有重复帧」的序列成立（重复帧 select 表达不了）。
func SelectFilter(t *Timeline) string {
	del := t.MissingRanges()
	if len(del) == 0 {
		return ""
	}
	terms := make([]string, 0, len(del))
	for _, s := range del {
		if s.To == s.From+1 {
			terms = append(terms, fmt.Sprintf("eq(n\\,%d)", s.From))
		} else {
			terms = append(terms, fmt.Sprintf("between(n\\,%d\\,%d)", s.From, s.To-1))
		}
	}
	return "select='not(" + strings.Join(terms, "+") + ")'"
}

// MaxRuns 是 trim+concat 路线的片段数上限，太碎就拒绝，
// 免得拼出一个几千条的 filtergraph。
const MaxRuns = 1000

// ConcatFilter 把输出序列翻译成 trim+concat 的 filter_complex，
// 这样重复帧也能导出（select 只能删，不能复制）。
//
// 最后一步 setpts=N/(fps*TB) 是必须的：单帧片段经过 concat 后时长是 0，
// 几段的时间戳会撞在一起，编码器按 CFR 一合并就少帧（实测 13 帧变 12 帧）。
// 按帧序号重排时间戳之后，输出就是一条干净的 CFR 序列。
func ConcatFilter(t *Timeline, fps float64, scale string) (string, error) {
	runs := t.Runs()
	if len(runs) == 0 {
		return "", fmt.Errorf("没有帧可导出")
	}
	if len(runs) > MaxRuns {
		return "", fmt.Errorf("输出被切成了 %d 段（重复/删除太碎），超过 %d 段上限", len(runs), MaxRuns)
	}
	var parts []string
	var labels strings.Builder
	for i, r := range runs {
		parts = append(parts, fmt.Sprintf("[0:v]trim=start_frame=%d:end_frame=%d,setpts=PTS-STARTPTS[v%d]", r.From, r.To, i))
		fmt.Fprintf(&labels, "[v%d]", i)
	}
	parts = append(parts, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[cat]", labels.String(), len(runs)))
	post := "[cat]"
	if scale != "" {
		post += scale + ","
	}
	parts = append(parts, fmt.Sprintf("%ssetpts=N/(%g*TB)[out]", post, fps))
	return strings.Join(parts, ";"), nil
}

func runFFmpeg(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return ffmpegFail(ctx, stderr.Bytes())
	}
	return nil
}

// ffmpegFail 把 ffmpeg 的 stderr 尾巴变成一条错误。取消时优先报取消。
func ffmpegFail(ctx context.Context, stderr []byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	msg := bytes.TrimSpace(stderr)
	if len(msg) > 800 {
		msg = msg[len(msg)-800:]
	}
	return fmt.Errorf("ffmpeg 失败: %s", msg)
}

// ExportVideo 把输出序列重编码成一个新视频（不封装音频）。
func ExportVideo(ctx context.Context, info Info, t *Timeline, out string, crf int) error {
	fargs, err := sequenceFilterArgs(t, 0, 0)
	if err != nil {
		return err
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", info.Path}
	args = append(args, fargs...)
	if t.Monotonic() {
		args = append(args, "-fps_mode", "vfr")
	}
	args = append(args,
		"-an",
		"-c:v", "libx264", "-crf", strconv.Itoa(crf), "-preset", "medium",
		"-pix_fmt", "yuv420p",
		out,
	)
	return runFFmpeg(ctx, args)
}

// ExportImages 导出 PNG 序列：pattern 里必须带 %d（如 /tmp/out_%04d.png）。
func ExportImages(ctx context.Context, info Info, t *Timeline, pattern string) error {
	if !strings.Contains(pattern, "%") {
		return fmt.Errorf("序列帧输出名要带 %%d 编号，比如 %q", pattern+"/x_%04d.png")
	}
	fargs, err := sequenceFilterArgs(t, 0, 0)
	if err != nil {
		return err
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", info.Path}
	args = append(args, fargs...)
	if t.Monotonic() {
		args = append(args, "-fps_mode", "vfr")
	}
	args = append(args, "-an", pattern)
	return runFFmpeg(ctx, args)
}

// sequenceFilterArgs 返回把源文件变成「输出序列」的滤镜参数，
// scaleW/H > 0 时顺带缩放（拼贴图用）。
func sequenceFilterArgs(t *Timeline, scaleW, scaleH int) ([]string, error) {
	if t.Count() == 0 {
		return nil, fmt.Errorf("没有帧可导出")
	}
	scale := ""
	if scaleW > 0 && scaleH > 0 && (scaleW != t.Info().Width || scaleH != t.Info().Height) {
		scale = fmt.Sprintf("scale=%d:%d", scaleW, scaleH)
	}
	if t.Monotonic() {
		var parts []string
		if f := SelectFilter(t); f != "" {
			parts = append(parts, f)
		}
		if scale != "" {
			parts = append(parts, scale)
		}
		if len(parts) == 0 {
			return nil, nil
		}
		return []string{"-vf", strings.Join(parts, ",")}, nil
	}
	f, err := ConcatFilter(t, t.Info().FPS, scale)
	if err != nil {
		return nil, err
	}
	return []string{"-filter_complex", f, "-map", "[out]"}, nil
}

// 拼贴图的尺寸上限，超过就拒绝，免得吃光内存。
// 50 帧 1080p 原尺寸大约是 11520×10800（124MP，约 500MB），要放得下。
const (
	MaxTileEdge   = 16384
	MaxTilePixels = 160 << 20 // 160MP，约 640MB 的 RGBA
)

// TileWarnFrames 是拼贴图的建议帧数上限：超了就提醒一下再决定。
// 平时拼图基本是给 Blender 用，一般不超过 50 帧。
const TileWarnFrames = 50

// TileGrid 算「尽量方正」的网格：让 cols*tileW ≈ rows*tileH。
func TileGrid(n, tileW, tileH int) (cols, rows int) {
	if n <= 0 || tileW <= 0 || tileH <= 0 {
		return 0, 0
	}
	best := math.MaxInt // 注意别用 0 当哨兵：刚好方正时分差就是 0
	start := int(math.Sqrt(float64(n) * float64(tileH) / float64(tileW)))
	for c := max(1, start-4); c <= start+4; c++ {
		r := (n + c - 1) / c
		d := abs(c*tileW - r*tileH) // 宽高越接近越方正
		if d < best {
			best, cols, rows = d, c, r
		}
	}
	return cols, rows
}

// tileDims 算出每一格的尺寸。tileW <= 0（或比源还宽）就是原尺寸——
// 这时候完全不做缩放，1:1 像素导出。
func tileDims(info Info, tileW int) (w, h int) {
	if tileW <= 0 || tileW >= info.Width {
		return info.Width, info.Height
	}
	h = int(math.Round(float64(info.Height) * float64(tileW) / float64(info.Width)))
	return max(2, tileW&^1), max(2, h&^1)
}

// TileSize 算出拼贴图的像素尺寸，用来在导出前告诉用户会有多大。
func TileSize(info Info, n, tileW int) (w, h int) {
	if n <= 0 {
		return 0, 0
	}
	tw, th := tileDims(info, tileW)
	cols, rows := TileGrid(n, tw, th)
	return cols * tw, rows * th
}

// ExportTile 把保留的帧拼成一张尽量方正的拼贴图。
func ExportTile(ctx context.Context, info Info, t *Timeline, out string, tileW int) error {
	n := t.Count()
	if n == 0 {
		return fmt.Errorf("没有帧可导出")
	}
	tw, th := tileDims(info, tileW)
	cols, rows := TileGrid(n, tw, th)
	if cols*tw > MaxTileEdge || rows*th > MaxTileEdge || cols*tw*rows*th > MaxTilePixels {
		return fmt.Errorf("%d 帧的拼贴图会是 %d×%d（约 %d MB），超过上限 %d×%d / %d 百万像素；"+
			"先删掉一些帧，或者给个每格宽度（:exportTile out.png 960）",
			n, cols*tw, rows*th, cols*tw*rows*th*4>>20, MaxTileEdge, MaxTileEdge, MaxTilePixels>>20)
	}

	sheet := image.NewRGBA(image.Rect(0, 0, cols*tw, rows*th))

	// 和视频/序列帧导出走同一套滤镜：有重复帧时必须 trim+concat，
	// 否则 select 只会给出「唯一的那些帧」，复制出来的格子全是透明的。
	fargs, err := sequenceFilterArgs(t, tw, th)
	if err != nil {
		return err
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-i", info.Path}
	args = append(args, fargs...)
	// rawvideo 输出默认按 CFR 补帧，会把 select 删出来的空洞用重复帧填满。
	args = append(args, "-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", "rgba", "-")
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	buf := make([]byte, tw*th*4)
	for i := 0; i < n; i++ {
		if _, err := io.ReadFull(pipe, buf); err != nil {
			break
		}
		cell := image.Rect((i%cols)*tw, (i/cols)*th, (i%cols+1)*tw, (i/cols+1)*th)
		draw.Draw(sheet, cell, &image.RGBA{Pix: buf, Stride: tw * 4, Rect: image.Rect(0, 0, tw, th)}, image.Point{}, draw.Src)
	}
	_ = pipe.Close() // 同上：先关管道，Wait 才不会挂住
	_ = cmd.Wait()
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, sheet)
}
