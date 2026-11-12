package core

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Krita 动画文件（.kra）导出。
//
// .kra 就是个 ZIP，Krita 5.x 的布局是（条目顺序无所谓）：
//
//	mimetype                              application/x-krita（必须第一项、不压缩）
//	pcutter/layers/layer1                 第 1 个关键帧的画面
//	pcutter/layers/layer1.f1              第 2..N 个关键帧的画面
//	maindoc.xml                           文档、图层、动画元数据
//	pcutter/layers/layer1.keyframes.xml   关键帧表
//	preview.png                           缩略图（可选，Krita 不读）
//
// 图层里的画面是 64×64 分块，每块可以原样存（flag 0）也可以 LZF 压缩
// （flag 1）。这里只写原样数据，省掉一个压缩器；全透明的块不写——Krita
// 的分块表本来也只存和默认像素不同的块。像素内存序是 BGRA，不是 RGBA。

const (
	kritaTileSize = 64
	// kritaImageName 是 ZIP 里的顶层目录名（Krita 用文档标题）。固定成 ASCII，
	// 免得图层路径还要处理特殊字符。
	kritaImageName  = "pcutter"
	kritaPreviewMax = 256
	kritaMime       = "application/x-krita"
)

// KritaTimes 返回 Krita 时间轴上要有关键帧的帧号，也就是保留下来的
// 源视频帧号（升序去重）。Krita 的时间轴按源帧号对齐，删掉的时间段
// 在 Krita 里表现为「保持上一帧」。
//
// 注意：时间轴是帧号，所以粘贴出来的重复帧在 Krita 里没有位置——
// 同一个源帧号只有一个关键帧。
func KritaTimes(t *Timeline) []int {
	seen := make([]bool, t.Total())
	for _, v := range t.List() {
		if v >= 0 && v < len(seen) {
			seen[v] = true
		}
	}
	out := make([]int, 0, len(seen))
	for i, ok := range seen {
		if ok {
			out = append(out, i)
		}
	}
	return out
}

// kritaDoc 是一个要写的 .kra 文档。
type kritaDoc struct {
	Width, Height int
	FPS           float64
	From, To      int // 播放区间（含两端），单位是帧
	CurrentTime   int // 打开时停在哪一帧
	Layer         string
	Keys          []int    // 关键帧帧号，升序
	Names         []string // 每个关键帧的画面文件名，和 Keys 一一对应
}

func (d kritaDoc) maindocXML() []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE DOC PUBLIC '-//KDE//DTD krita 2.0//EN' 'http://www.calligra.org/DTD/krita-2.0.dtd'>` + "\n")
	b.WriteString(`<DOC xmlns="http://www.calligra.org/DTD/krita" syntaxVersion="2" editor="Krita">` + "\n")
	fmt.Fprintf(&b, ` <IMAGE width="%d" height="%d" mime="application/x-kra" name="%s" colorspacename="RGBA" description="" x-res="100" y-res="100">`+"\n",
		d.Width, d.Height, kritaImageName)
	b.WriteString("  <layers>\n")
	fmt.Fprintf(&b, `   <layer name="%s" nodetype="paintlayer" filename="layer1" x="0" y="0" opacity="255" visible="1" compositeop="normal" locked="0" collapsed="0" colorspacename="RGBA" channelflags="" keyframes="layer1.keyframes.xml"/>`+"\n",
		escapeXML(d.Layer))
	b.WriteString("  </layers>\n")
	b.WriteString("  <animation>\n")
	fmt.Fprintf(&b, `   <framerate type="value" value="%s"/>`+"\n", strconv.FormatFloat(d.FPS, 'g', -1, 64))
	fmt.Fprintf(&b, `   <range from="%d" type="timerange" to="%d"/>`+"\n", d.From, d.To)
	fmt.Fprintf(&b, `   <currentTime type="value" value="%d"/>`+"\n", d.CurrentTime)
	b.WriteString("  </animation>\n")
	b.WriteString(" </IMAGE>\n</DOC>\n")
	return b.Bytes()
}

func (d kritaDoc) keyframesXML() []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE keyframes PUBLIC '-//KDE//DTD krita-keyframes 1.0//EN' 'http://www.calligra.org/DTD/krita-keyframes-1.0.dtd'>` + "\n")
	b.WriteString(`<keyframes xmlns="http://www.calligra.org/DTD/krita-keyframes">` + "\n")
	b.WriteString(" <channel name=\"content\">\n")
	for i, key := range d.Keys {
		fmt.Fprintf(&b, `  <keyframe frame="%s" time="%d">`+"\n", escapeXML(d.Names[i]), key)
		b.WriteString("   <offset x=\"0\" y=\"0\" type=\"point\"/>\n")
		b.WriteString("  </keyframe>\n")
	}
	b.WriteString(" </channel>\n</keyframes>\n")
	return b.Bytes()
}

func escapeXML(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ExportKrita 把输出序列写成一个 Krita 动画文件：一个 paint layer，动画
// 长度等于源视频帧数，保留下来的每一帧是一个关键帧（帧号就是源帧号）。
func ExportKrita(ctx context.Context, info Info, t *Timeline, out string) error {
	keys := KritaTimes(t)
	if len(keys) == 0 {
		return fmt.Errorf("没有帧可导出")
	}
	if info.Width <= 0 || info.Height <= 0 {
		return fmt.Errorf("不知道视频尺寸")
	}
	d := kritaDoc{
		Width:       info.Width,
		Height:      info.Height,
		FPS:         info.FPS,
		From:        0,
		To:          max(0, info.Count-1),
		CurrentTime: keys[0],
		Layer:       kritaLayerName(info.Path),
		Keys:        keys,
		Names:       make([]string, len(keys)),
	}
	for i := range keys {
		// 和 Krita 自己保存时一样：第一个关键帧沿用图层文件名，后面加 .f<序号>
		if i == 0 {
			d.Names[i] = "layer1"
		} else {
			d.Names[i] = "layer1.f" + strconv.Itoa(i)
		}
	}

	args := []string{"-hide_banner", "-loglevel", "error", "-i", info.Path}
	if f := SelectFilter(t); f != "" {
		args = append(args, "-vf", f)
	}
	// rawvideo 默认按 CFR 补帧，passthrough 才能让删掉的帧真的不出现。
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

	f, err := os.Create(out)
	if err != nil {
		_ = pipe.Close()
		_ = cmd.Wait()
		return err
	}
	zw := zip.NewWriter(f)
	werr := writeKritaZip(zw, d, pipe)
	_, _ = io.Copy(io.Discard, pipe) // 源帧数比探测值多时，把尾帧读干净，免得 ffmpeg 撞 EPIPE
	cerr := zw.Close()
	_ = pipe.Close() // 先关管道，Wait 才不会挂住
	waitErr := cmd.Wait()
	_ = f.Close()
	if werr != nil || cerr != nil || waitErr != nil {
		// 半个 .kra 是废文件，删掉别留着让人误以为导出成功
		_ = os.Remove(out)
		switch {
		case werr != nil:
			return werr
		case cerr != nil:
			return cerr
		}
	}
	if waitErr != nil {
		return ffmpegFail(ctx, stderr.Bytes())
	}
	if ctx.Err() != nil {
		_ = os.Remove(out)
		return ctx.Err()
	}
	return nil
}

// writeKritaZip 写整个 .kra，画面从 frames 里按帧号升序读（rawvideo rgba）。
//
// 先写图层再写 XML：万一 ffmpeg 解出的帧比探测值少（VFR 估算偏差），
// 就以实际写进去的帧为准，不会留下指向不存在画面的关键帧。
func writeKritaZip(zw *zip.Writer, d kritaDoc, frames io.Reader) error {
	add := func(name string, data []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}

	// mimetype 必须是第一个条目、不压缩，而且不能带 data descriptor
	// （CreateRaw 会直接把长度写在本地头里，跟 Krita 自己写的一样）。
	// CreateRaw 不压缩也不自己算 CRC，得手动填（否则严格一点的解包器会报错）
	n := uint64(len(kritaMime))
	mw, err := zw.CreateRaw(&zip.FileHeader{
		Name: "mimetype", Method: zip.Store, ReaderVersion: 20, // 和 Krita 自己写的一致
		CRC32:            crc32.ChecksumIEEE([]byte(kritaMime)),
		CompressedSize64: n, UncompressedSize64: n,
	})
	if err != nil {
		return err
	}
	if _, err := mw.Write([]byte(kritaMime)); err != nil {
		return err
	}

	stride := d.Width * 4
	buf := make([]byte, stride*d.Height)
	var preview []byte
	written := 0
	for i, name := range d.Names {
		if _, err := io.ReadFull(frames, buf); err != nil {
			if i == 0 {
				return fmt.Errorf("ffmpeg 没解出画面（%v）", err)
			}
			break
		}
		img := &image.RGBA{Pix: buf, Stride: stride, Rect: image.Rect(0, 0, d.Width, d.Height)}
		w, err := zw.Create(kritaImageName + "/layers/" + name)
		if err != nil {
			return err
		}
		if err := writeKritaLayer(w, img); err != nil {
			return err
		}
		if i == 0 {
			preview = kritaPreview(img)
		}
		written++
	}
	d.Keys = d.Keys[:written]
	d.Names = d.Names[:written]
	d.CurrentTime = d.Keys[0]

	if err := add("maindoc.xml", d.maindocXML()); err != nil {
		return err
	}
	if err := add(kritaImageName+"/layers/layer1.keyframes.xml", d.keyframesXML()); err != nil {
		return err
	}
	if preview != nil {
		return add("preview.png", preview)
	}
	return nil
}

// writeKritaLayer 写一个关键帧的画面。
func writeKritaLayer(w io.Writer, img *image.RGBA) error {
	b := img.Bounds()
	cols := (b.Dx() + kritaTileSize - 1) / kritaTileSize
	rows := (b.Dy() + kritaTileSize - 1) / kritaTileSize

	var tiles int
	for ty := 0; ty < rows; ty++ {
		for tx := 0; tx < cols; tx++ {
			if kritaTileHasInk(img, tx*kritaTileSize, ty*kritaTileSize) {
				tiles++
			}
		}
	}
	if _, err := fmt.Fprintf(w, "VERSION 2\nTILEWIDTH %d\nTILEHEIGHT %d\nPIXELSIZE 4\nDATA %d\n",
		kritaTileSize, kritaTileSize, tiles); err != nil {
		return err
	}

	buf := make([]byte, kritaTileSize*kritaTileSize*4)
	for ty := 0; ty < rows; ty++ {
		for tx := 0; tx < cols; tx++ {
			x, y := tx*kritaTileSize, ty*kritaTileSize
			if !kritaTileHasInk(img, x, y) {
				continue // 和默认像素（全透明）一样，不用存
			}
			kritaFillTile(img, buf, x, y)
			// 结尾的 "LZF" 是固定字串（Krita 的块头就这么写），
			// 载荷第一个字节 0 表示这块没压缩。
			if _, err := fmt.Fprintf(w, "%d,%d,LZF,%d\n", x, y, len(buf)+1); err != nil {
				return err
			}
			if _, err := w.Write([]byte{0}); err != nil {
				return err
			}
			if _, err := w.Write(buf); err != nil {
				return err
			}
		}
	}
	return nil
}

// kritaTileHasInk 报告这个块里有没有不透明的像素。
func kritaTileHasInk(img *image.RGBA, x, y int) bool {
	b := img.Bounds()
	for row := 0; row < kritaTileSize; row++ {
		py := y + row
		if py >= b.Max.Y {
			break
		}
		for col := 0; col < kritaTileSize; col++ {
			px := x + col
			if px >= b.Max.X {
				break
			}
			if img.Pix[img.PixOffset(px, py)+3] != 0 {
				return true
			}
		}
	}
	return false
}

// kritaFillTile 把一块画面写进 buf（BGRA，块外补 0）。
func kritaFillTile(img *image.RGBA, buf []byte, x, y int) {
	clear(buf)
	b := img.Bounds()
	for row := 0; row < kritaTileSize; row++ {
		py := y + row
		if py >= b.Max.Y {
			break
		}
		dst := row * kritaTileSize * 4
		for col := 0; col < kritaTileSize; col++ {
			px := x + col
			if px >= b.Max.X {
				break
			}
			s := img.PixOffset(px, py)
			buf[dst] = img.Pix[s+2] // B
			buf[dst+1] = img.Pix[s+1]
			buf[dst+2] = img.Pix[s] // R
			buf[dst+3] = img.Pix[s+3]
			dst += 4
		}
	}
}

// kritaPreview 把一帧缩成不超过 256 的长边当缩略图。
// Krita 不读它，但文件管理器和打开对话框会拿它当图标。
func kritaPreview(img *image.RGBA) []byte {
	b := img.Bounds()
	step := max(1, (max(b.Dx(), b.Dy())+kritaPreviewMax-1)/kritaPreviewMax)
	w, h := (b.Dx()+step-1)/step, (b.Dy()+step-1)/step
	small := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			small.SetRGBA(x, y, img.RGBAAt(b.Min.X+min(x*step, b.Dx()-1), b.Min.Y+min(y*step, b.Dy()-1)))
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, small); err != nil {
		return nil
	}
	return out.Bytes()
}

// kritaLayerName 用文件名当图层名，方便在一堆导出里认出来。
func kritaLayerName(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if base == "" {
		return "frames"
	}
	return base
}
