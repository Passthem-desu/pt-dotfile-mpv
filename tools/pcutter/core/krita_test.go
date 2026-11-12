package core

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"hash/crc32"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ---- 测试辅助 ----

func unzipBytes(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("不是合法 zip: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
	}
	return out
}

func readKRA(t *testing.T, path string) map[string][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return unzipBytes(t, data)
}

// xmlAttrs 扫出某种元素的所有属性，用来读 maindoc / keyframes。
func xmlAttrs(t *testing.T, data []byte, element string) []map[string]string {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []map[string]string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if strings.Contains(err.Error(), "DOCTYPE") {
				continue
			}
			t.Fatalf("解析 %s: %v", element, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != element {
			continue
		}
		m := map[string]string{}
		for _, a := range se.Attr {
			m[a.Name.Local] = a.Value
		}
		out = append(out, m)
	}
	return out
}

// layerTiles 解析一个关键帧的画面文件，返回块坐标 -> 块数据。
func layerTiles(t *testing.T, data []byte) (map[[2]int][]byte, string) {
	t.Helper()
	br := bufio.NewReader(bytes.NewReader(data))
	header := ""
	for i := 0; i < 5; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("读第 %d 行头失败: %v", i+1, err)
		}
		header += line
	}
	// 头里 DATA <n> 的 n 就是块数
	var n int
	for _, line := range strings.Split(strings.TrimSpace(header), "\n") {
		if v, ok := strings.CutPrefix(line, "DATA "); ok {
			n, _ = strconv.Atoi(v)
		}
	}
	tiles := map[[2]int][]byte{}
	for i := 0; i < n; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("读第 %d 个块头失败: %v", i+1, err)
		}
		parts := strings.Split(strings.TrimSpace(line), ",")
		if len(parts) != 4 || parts[2] != "LZF" {
			t.Fatalf("块头 %q 不是 x,y,LZF,size", line)
		}
		x, _ := strconv.Atoi(parts[0])
		y, _ := strconv.Atoi(parts[1])
		size, _ := strconv.Atoi(parts[3])
		payload := make([]byte, size)
		if _, err := io.ReadFull(br, payload); err != nil {
			t.Fatalf("读块数据失败: %v", err)
		}
		if payload[0] != 0 {
			t.Fatalf("块 %d,%d 的标志位是 %d，want 0（未压缩）", x, y, payload[0])
		}
		tiles[[2]int{x, y}] = payload[1:]
	}
	return tiles, header
}

// testDoc 造一个每帧都是纯色的最小文档。
func solidFrame(w, h int, c [4]uint8, n int) []byte {
	buf := make([]byte, w*h*4*n)
	for i := 0; i < w*h*n; i++ {
		copy(buf[i*4:], c[:])
	}
	return buf
}

// ---- 时间轴映射 ----

func TestKritaTimes(t *testing.T) {
	tl := newTL(15)
	// 只保留 3,6,9,12；从后往前删，行号才不会被前面的删除挪掉
	for _, i := range []int{14, 13, 11, 10, 8, 7, 5, 4, 2, 1, 0} {
		if _, ok := tl.Delete(i); !ok {
			t.Fatalf("删不掉第 %d 行", i)
		}
	}
	got := KritaTimes(tl)
	want := []int{3, 6, 9, 12}
	if len(got) != len(want) {
		t.Fatalf("关键帧 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("关键帧 = %v, want %v", got, want)
		}
	}
}

// 粘贴出来的重复帧在 Krita 的时间轴上只有一个位置（帧号就是源帧号）。
func TestKritaTimesDedupesAndSorts(t *testing.T) {
	tl := newTL(8)
	tl.Insert(0, []int{5, 5}) // 序列变成 5,5,0,1,...——重复且乱序
	if got := KritaTimes(tl); len(got) != 8 || got[0] != 0 || got[7] != 7 {
		t.Fatalf("关键帧 = %v", got)
	}
	tl2 := newTL(4)
	tl2.DeleteRange(1, 3)   // 只留 0,3
	tl2.Insert(0, []int{3}) // 序列变成 3,0,3
	if got := KritaTimes(tl2); len(got) != 2 || got[0] != 0 || got[1] != 3 {
		t.Fatalf("关键帧 = %v", got)
	}
}

// ---- XML ----

func TestKritaMaindoc(t *testing.T) {
	d := kritaDoc{
		Width: 1920, Height: 1080, FPS: 29.97,
		From: 0, To: 14, CurrentTime: 3,
		Layer: `a&b"c`,
		Keys:  []int{3, 6},
		Names: []string{"layer1", "layer1.f1"},
	}
	xmlData := d.maindocXML()
	s := string(xmlData)
	if !strings.Contains(s, "<!DOCTYPE DOC") {
		t.Fatalf("maindoc 少了 DOCTYPE DOC，Krita 会拒绝:\n%s", s)
	}
	if !strings.Contains(s, `syntaxVersion="2"`) {
		t.Fatalf("syntaxVersion 应该是 2:\n%s", s)
	}

	images := xmlAttrs(t, xmlData, "IMAGE")
	if len(images) != 1 {
		t.Fatalf("IMAGE 元素 = %d 个", len(images))
	}
	im := images[0]
	if im["mime"] != "application/x-kra" || im["colorspacename"] != "RGBA" ||
		im["name"] != kritaImageName || im["width"] != "1920" || im["height"] != "1080" {
		t.Fatalf("IMAGE 属性不对: %v", im)
	}

	layers := xmlAttrs(t, xmlData, "layer")
	if len(layers) != 1 {
		t.Fatalf("layer 元素 = %d 个", len(layers))
	}
	l := layers[0]
	if l["nodetype"] != "paintlayer" || l["filename"] != "layer1" ||
		l["keyframes"] != "layer1.keyframes.xml" || l["compositeop"] != "normal" ||
		l["opacity"] != "255" || l["visible"] != "1" {
		t.Fatalf("layer 属性不对: %v", l)
	}
	if l["name"] != `a&b"c` { // 解析回来应该是原文，转义在写的时候做
		t.Fatalf("图层名 = %q", l["name"])
	}
	if !strings.Contains(string(xmlData), `name="a&amp;b&#34;c"`) {
		t.Fatalf("图层名没转义:\n%s", xmlData)
	}

	fr := xmlAttrs(t, xmlData, "framerate")
	if len(fr) != 1 || fr[0]["value"] != "29.97" {
		t.Fatalf("framerate = %v", fr)
	}
	rng := xmlAttrs(t, xmlData, "range")
	if len(rng) != 1 || rng[0]["from"] != "0" || rng[0]["to"] != "14" {
		t.Fatalf("range = %v", rng)
	}
	cur := xmlAttrs(t, xmlData, "currentTime")
	if len(cur) != 1 || cur[0]["value"] != "3" {
		t.Fatalf("currentTime = %v", cur)
	}
}

func TestKritaKeyframes(t *testing.T) {
	d := kritaDoc{Keys: []int{3, 6, 9}, Names: []string{"layer1", "layer1.f1", "layer1.f2"}}
	data := d.keyframesXML()
	if !strings.Contains(string(data), "<!DOCTYPE keyframes") {
		t.Fatal("keyframes 少了 DOCTYPE")
	}
	kfs := xmlAttrs(t, data, "keyframe")
	if len(kfs) != 3 {
		t.Fatalf("关键帧 = %d 个", len(kfs))
	}
	for i, want := range []struct {
		time  string
		frame string
	}{{"3", "layer1"}, {"6", "layer1.f1"}, {"9", "layer1.f2"}} {
		if kfs[i]["time"] != want.time || kfs[i]["frame"] != want.frame {
			t.Fatalf("第 %d 个关键帧 = %v, want time=%s frame=%s", i, kfs[i], want.time, want.frame)
		}
	}
	// 通道名必须是 content，否则 Krita 找不到光栅关键帧通道
	if ch := xmlAttrs(t, data, "channel"); len(ch) != 1 || ch[0]["name"] != "content" {
		t.Fatalf("channel = %v", ch)
	}
}

// ---- 图层数据 ----

// 像素在 .kra 里是 BGRA，不是 RGBA；全透明的块不写。
func TestWriteKritaLayerTiles(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 130, 70)) // 3×2 块，只有两块有内容
	img.SetRGBA(0, 0, color.RGBA{1, 2, 3, 255})
	img.SetRGBA(129, 69, color.RGBA{4, 5, 6, 255})
	var b bytes.Buffer
	if err := writeKritaLayer(&b, img); err != nil {
		t.Fatal(err)
	}
	tiles, header := layerTiles(t, b.Bytes())
	for _, want := range []string{"VERSION 2\n", "TILEWIDTH 64\n", "TILEHEIGHT 64\n", "PIXELSIZE 4\n", "DATA 2\n"} {
		if !strings.Contains(header, want) {
			t.Fatalf("头里少了 %q:\n%s", want, header)
		}
	}
	if len(tiles) != 2 {
		t.Fatalf("块数 = %d，want 2（只有两块有像素）", len(tiles))
	}
	t00, ok := tiles[[2]int{0, 0}]
	if !ok {
		t.Fatal("缺 0,0 块")
	}
	if got := t00[0:4]; !bytes.Equal(got, []byte{3, 2, 1, 255}) {
		t.Fatalf("0,0 的四个字节 = %v，want [3 2 1 255]（BGRA）", got)
	}
	t64, ok := tiles[[2]int{128, 64}]
	if !ok {
		t.Fatal("缺 128,64 块")
	}
	// 129,69 在块内 (1,5)：行 5、列 1
	off := (5*kritaTileSize + 1) * 4
	if got := t64[off : off+4]; !bytes.Equal(got, []byte{6, 5, 4, 255}) {
		t.Fatalf("129,69 的四个字节 = %v，want [6 5 4 255]", got)
	}
}

func TestWriteKritaLayerEmpty(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	var b bytes.Buffer
	if err := writeKritaLayer(&b, img); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "VERSION 2\nTILEWIDTH 64\nTILEHEIGHT 64\nPIXELSIZE 4\nDATA 0\n" {
		t.Fatalf("全透明的帧应该只有 5 行头，得到 %q", got)
	}
}

// ---- 整个文件 ----

func TestWriteKritaZip(t *testing.T) {
	d := kritaDoc{
		Width: 8, Height: 8, FPS: 24, From: 0, To: 9, CurrentTime: 3,
		Layer: "clip",
		Keys:  []int{3, 6},
		Names: []string{"layer1", "layer1.f1"},
	}
	frames := append(solidFrame(8, 8, [4]uint8{255, 0, 0, 255}, 1), solidFrame(8, 8, [4]uint8{0, 0, 0, 0}, 1)...)
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	if err := writeKritaZip(zw, d, bytes.NewReader(frames)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	files := unzipBytes(t, b.Bytes())

	if got := string(files["mimetype"]); got != "application/x-krita" {
		t.Fatalf("mimetype = %q", got)
	}
	for _, name := range []string{
		"maindoc.xml", "preview.png",
		kritaImageName + "/layers/layer1",
		kritaImageName + "/layers/layer1.f1",
		kritaImageName + "/layers/layer1.keyframes.xml",
	} {
		if _, ok := files[name]; !ok {
			t.Fatalf("缺 %s，有 %v", name, keysOf(files))
		}
	}
	// zip 的第一项必须是 mimetype：不压缩、不能带 data descriptor，
	// 否则一些认 MIME 的工具（file / 文件管理器）读不出来。
	zr, _ := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if zr.File[0].Name != "mimetype" || zr.File[0].Method != zip.Store {
		t.Fatalf("第一项 = %s (method %d)", zr.File[0].Name, zr.File[0].Method)
	}
	if want := crc32.ChecksumIEEE([]byte(kritaMime)); zr.File[0].CRC32 != want {
		t.Fatalf("mimetype 的 CRC = %#x, want %#x", zr.File[0].CRC32, want)
	}
	raw := b.Bytes()
	if flags := binary.LittleEndian.Uint16(raw[6:8]); flags&0x08 != 0 {
		t.Fatalf("mimetype 带了 data descriptor（flag = %#x）", flags)
	}
	if ver := binary.LittleEndian.Uint16(raw[4:6]); ver != 20 {
		t.Fatalf("version needed = %d, want 20", ver)
	}

	// 第一帧是纯红，第二帧全透明
	if _, header := layerTiles(t, files[kritaImageName+"/layers/layer1"]); !strings.Contains(header, "DATA 1\n") {
		t.Fatalf("第一帧应该有 1 块:\n%s", header)
	}
	if _, header := layerTiles(t, files[kritaImageName+"/layers/layer1.f1"]); !strings.Contains(header, "DATA 0\n") {
		t.Fatalf("第二帧全透明，应该是 DATA 0:\n%s", header)
	}
}

// ffmpeg 少给几帧时（VFR 估算偏差），不要留下指向不存在画面的关键帧。
func TestWriteKritaZipTrimsToFrames(t *testing.T) {
	d := kritaDoc{
		Width: 8, Height: 8, FPS: 24, From: 0, To: 9,
		Layer: "clip",
		Keys:  []int{3, 6, 9},
		Names: []string{"layer1", "layer1.f1", "layer1.f2"},
	}
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	if err := writeKritaZip(zw, d, bytes.NewReader(solidFrame(8, 8, [4]uint8{9, 9, 9, 255}, 2))); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	files := unzipBytes(t, b.Bytes())
	kfs := xmlAttrs(t, files["maindoc.xml"], "range")
	if len(kfs) != 1 || kfs[0]["to"] != "9" {
		t.Fatalf("range 用的是源帧数，和实际写进去的帧数无关: %v", kfs)
	}
	if got := xmlAttrs(t, files[kritaImageName+"/layers/layer1.keyframes.xml"], "keyframe"); len(got) != 2 {
		t.Fatalf("关键帧 = %v, want 2 个", got)
	}
	if _, ok := files[kritaImageName+"/layers/layer1.f2"]; ok {
		t.Fatal("第 3 帧没解出来，不该有它的画面文件")
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---- 端到端（要 ffmpeg） ----

func TestExportKritaEndToEnd(t *testing.T) {
	src := makeVideo(t, "v.mp4", "testsrc2=size=160x120:rate=30:duration=1") // 30 帧
	info, err := Probe(src)
	if err != nil {
		t.Fatal(err)
	}
	keep := map[int]bool{3: true, 6: true, 9: true, 12: true}
	tl := NewTimeline(info)
	for i := info.Count - 1; i >= 0; i-- {
		if !keep[i] {
			tl.Delete(i)
		}
	}
	out := filepath.Join(t.TempDir(), "clip.kra")
	if err := ExportKrita(context.Background(), info, tl, out); err != nil {
		t.Fatal(err)
	}
	files := readKRA(t, out)

	im := xmlAttrs(t, files["maindoc.xml"], "IMAGE")[0]
	if im["width"] != "160" || im["height"] != "120" || im["name"] != kritaImageName {
		t.Fatalf("IMAGE = %v", im)
	}
	// 播放区间是整个源视频，不是保留的帧数
	rng := xmlAttrs(t, files["maindoc.xml"], "range")[0]
	if rng["from"] != "0" || rng["to"] != strconv.Itoa(info.Count-1) {
		t.Fatalf("range = %v, want 0..%d", rng, info.Count-1)
	}
	if fr := xmlAttrs(t, files["maindoc.xml"], "framerate")[0]; fr["value"] != "30" {
		t.Fatalf("framerate = %v", fr)
	}
	kfs := xmlAttrs(t, files[kritaImageName+"/layers/layer1.keyframes.xml"], "keyframe")
	wantTimes := []string{"3", "6", "9", "12"}
	if len(kfs) != len(wantTimes) {
		t.Fatalf("关键帧 = %v", kfs)
	}
	for i, want := range wantTimes {
		if kfs[i]["time"] != want {
			t.Fatalf("第 %d 个关键帧时间 = %s, want %s", i, kfs[i]["time"], want)
		}
		if _, ok := files[kritaImageName+"/layers/"+kfs[i]["frame"]]; !ok {
			t.Fatalf("关键帧 %d 的画面文件 %s 不在包里", i, kfs[i]["frame"])
		}
	}
}
