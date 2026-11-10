package core

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"os/exec"
	"strconv"
)

// PreviewMaxEdge 是预览解码时的长边上限。
// 实测：1080p 位图逐帧上传 2.2ms、4K 要 8.7ms，而 960 宽只要 0.5ms 量级，
// 内存上 960×540 一帧 2.1MB，24 帧一块刚好塞得进 256MB 预算里的 5 块。
const PreviewMaxEdge = 960

// PreviewSize 把视频尺寸按 PreviewMaxEdge 缩到预览尺寸，并取偶数（yuv420 友好）。
func PreviewSize(w, h int) (int, int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	if w > h && w > PreviewMaxEdge {
		h = h * PreviewMaxEdge / w
		w = PreviewMaxEdge
	} else if h >= w && h > PreviewMaxEdge {
		w = w * PreviewMaxEdge / h
		h = PreviewMaxEdge
	}
	return w &^ 1, h &^ 1
}

// Extract 用一次 ffmpeg 调用取出从 startFrame 开始的 n 帧，缩放到 w×h 的 RGBA。
// 这是「一次缓存一整个 Block」的实现：进程启动 + seek 的固定开销只付一次
// （实测单帧 seek 250ms，60 帧一块 311ms）。
func Extract(info Info, startFrame, n, w, h int) ([]*image.RGBA, error) {
	if n <= 0 {
		return nil, nil
	}
	frameBytes := w * h * 4
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-ss", strconv.FormatFloat(info.FrameTime(startFrame), 'f', 3, 64),
		"-i", info.Path,
		"-frames:v", strconv.Itoa(n),
		// 管道输出默认按 CFR 补帧；passthrough 保证一个输入帧就是一个输出帧
		"-fps_mode", "passthrough",
		"-vf", fmt.Sprintf("scale=%d:%d", w, h),
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-",
	}
	cmd := exec.Command("ffmpeg", args...)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	frames := make([]*image.RGBA, 0, n)
	buf := make([]byte, frameBytes)
	for len(frames) < n {
		if _, err := io.ReadFull(out, buf); err != nil {
			break // io.EOF：视频到此为止
		}
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		copy(img.Pix, buf)
		frames = append(frames, img)
	}
	// 先关管道再 Wait：万一 ffmpeg 还有帧要写（比如把帧补满了），
	// 不关的话它会阻塞在写上，Wait 永远不返回。
	_ = out.Close()
	_ = cmd.Wait()
	if len(frames) == 0 {
		return nil, fmt.Errorf("ffmpeg 没有返回任何帧: %s", bytes.TrimSpace(stderr.Bytes()))
	}
	return frames, nil
}
