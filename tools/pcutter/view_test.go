package main

import (
	"flag"
	"image"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"pcutter/core"
)

// testApp 造一个没有解码器的 app：帧表是真的，画面是占位符。
func testApp(n int) *app {
	info := core.Info{Path: "/tmp/demo.mp4", Width: 1920, Height: 1080, FPS: 30, Count: n, CFR: true}
	a := &app{
		info:   info,
		tl:     core.NewTimeline(info),
		pw:     960,
		ph:     540,
		winW:   400,
		winH:   24*30 + statusH,
		done:   make(chan string, 1),
		poke:   make(chan struct{}, 1),
		jpos:   -1,
		cmdIdx: -1,
	}
	return a
}

func newTester(t *testing.T, a *app) *ui.Tester {
	t.Helper()
	tst := ui.NewTester(a.view, a.winW, a.winH)
	tst.SetFocused(true)
	return tst
}

func TestCursorAndScrolloff(t *testing.T) {
	a := testApp(200)
	tst := newTester(t, a)

	for i := 1; i <= 20; i++ {
		tst.Key(0, ui.KeyJ)
		if a.cursor != i {
			t.Fatalf("after %d j: cursor=%d", i, a.cursor)
		}
	}
	tst.Key(0, ui.KeyK)
	if a.cursor != 19 {
		t.Fatalf("k: %d", a.cursor)
	}
	tst.Key(ui.Shift, ui.KeyG)
	if a.cursor != 199 {
		t.Fatalf("G: %d", a.cursor)
	}
	tst.Key(0, ui.KeyG)
	tst.Key(0, ui.KeyG)
	if a.cursor != 0 || a.top != 0 {
		t.Fatalf("gg: cursor=%d top=%d", a.cursor, a.top)
	}

	for a.cursor < 100 {
		tst.Key(0, ui.KeyJ)
	}
	vis := a.visibleRows()
	if got := a.top + vis - 1 - a.cursor; got != scrolloff {
		t.Fatalf("底部 scrolloff = %d, want %d", got, scrolloff)
	}
	for a.cursor > 40 {
		tst.Key(0, ui.KeyK)
	}
	if got := a.cursor - a.top; got != scrolloff {
		t.Fatalf("顶部 scrolloff = %d, want %d", got, scrolloff)
	}
}

// <C-o> / <C-i>：gg 跳走之后能回来、能再跳过去。
func TestJumpList(t *testing.T) {
	a := testApp(500)
	tst := newTester(t, a)

	for i := 0; i < 120; i++ {
		tst.Key(0, ui.KeyJ)
	}
	if a.cursor != 120 {
		t.Fatalf("cursor=%d", a.cursor)
	}
	tst.Key(0, ui.KeyG)
	tst.Key(0, ui.KeyG) // gg -> 0
	if a.cursor != 0 {
		t.Fatalf("gg 之后 cursor=%d", a.cursor)
	}
	tst.Key(ui.Ctrl, ui.KeyO)
	if a.cursor != 120 {
		t.Fatalf("<C-o> 应该回到 120，got %d", a.cursor)
	}
	tst.Key(ui.Ctrl, ui.KeyI)
	if a.cursor != 0 {
		t.Fatalf("<C-i> 应该再跳到 0，got %d", a.cursor)
	}
	// Tab 等价于 <C-i>
	tst.Key(ui.Ctrl, ui.KeyO)
	if a.cursor != 120 {
		t.Fatalf("<C-o> again: %d", a.cursor)
	}
	tst.Key(0, ui.KeyTab)
	if a.cursor != 0 {
		t.Fatalf("<Tab> 应该等于 <C-i>，got %d", a.cursor)
	}
	// 到头了不能越界
	tst.Key(ui.Ctrl, ui.KeyI)
	if a.cursor != 0 {
		t.Fatalf("在最新位置按 <C-i> 不该动，got %d", a.cursor)
	}
	// G 也是一个 jump
	tst.Key(ui.Shift, ui.KeyG)
	if a.cursor != 499 {
		t.Fatalf("G: %d", a.cursor)
	}
	tst.Key(ui.Ctrl, ui.KeyO)
	if a.cursor != 0 {
		t.Fatalf("<C-o> after G: %d", a.cursor)
	}
}

func TestDeleteUndoRedo(t *testing.T) {
	a := testApp(10)
	tst := newTester(t, a)

	tst.Key(0, ui.KeyJ)
	tst.Key(0, ui.KeyJ)
	tst.Key(0, ui.KeyD)
	if a.count() != 9 || a.tl.Has(2) {
		t.Fatalf("d: count=%d alive2=%v", a.count(), a.tl.Has(2))
	}
	if a.cursor != 2 {
		t.Fatalf("光标应该在 2，got %d", a.cursor)
	}
	tst.Key(0, ui.KeyU)
	if a.count() != 10 || !a.tl.Has(2) {
		t.Fatalf("u: count=%d", a.count())
	}
	tst.Key(ui.Ctrl, ui.KeyR)
	if a.count() != 9 || a.tl.Has(2) {
		t.Fatalf("C-r: count=%d", a.count())
	}
}

func TestVisualDelete(t *testing.T) {
	a := testApp(10)
	tst := newTester(t, a)

	tst.Key(0, ui.KeyJ)
	tst.Key(0, ui.KeyJ) // 2
	tst.Key(ui.Shift, ui.KeyV)
	if a.m != modeVisual {
		t.Fatalf("mode=%v", a.m)
	}
	tst.Key(0, ui.KeyJ)
	tst.Key(0, ui.KeyJ) // 到 4
	tst.Key(0, ui.KeyD)
	if a.m != modeNormal {
		t.Fatalf("Visual 里 d 之后应该回 NORMAL，got %v", a.m)
	}
	if a.count() != 7 {
		t.Fatalf("删了 %d 帧，want 3", 10-a.count())
	}
	if a.tl.Has(2) || a.tl.Has(3) || a.tl.Has(4) {
		t.Fatal("2/3/4 应该都没了")
	}
	// 一次撤销全回来
	tst.Key(0, ui.KeyU)
	if a.count() != 10 {
		t.Fatalf("undo 之后 count=%d", a.count())
	}
}

func TestCommandLine(t *testing.T) {
	a := testApp(10)
	tst := newTester(t, a)

	tst.TypeKey(ui.Shift, ui.KeySemicolon, ":")
	if a.m != modeCommand {
		t.Fatalf("mode=%v", a.m)
	}
	if a.cmd != "" {
		t.Fatalf("那个 ':' 应该被吞掉，cmd=%q", a.cmd)
	}
	tst.Type("export out.png")
	if a.cmd != "export out.png" {
		t.Fatalf("cmd=%q", a.cmd)
	}
	tst.Key(0, ui.KeyBackspace)
	if a.cmd != "export out.pn" {
		t.Fatalf("backspace: %q", a.cmd)
	}
	tst.Key(0, ui.KeyEscape)
	if a.m != modeNormal || a.cmd != "" {
		t.Fatalf("esc: mode=%v cmd=%q", a.m, a.cmd)
	}
}

func TestCommandCompletion(t *testing.T) {
	a := testApp(10)
	tst := newTester(t, a)

	tst.TypeKey(ui.Shift, ui.KeySemicolon, ":")
	tst.Type("exp")
	tst.Key(0, ui.KeyTab)
	if a.cmd != "export" {
		t.Fatalf("Tab 补全: %q", a.cmd)
	}
	tst.Key(0, ui.KeyTab)
	if a.cmd != "exportKrita" {
		t.Fatalf("再 Tab: %q", a.cmd)
	}
	tst.Key(0, ui.KeyTab)
	if a.cmd != "exportSeq" {
		t.Fatalf("再 Tab: %q", a.cmd)
	}
	tst.Key(0, ui.KeyTab)
	if a.cmd != "exportTile" {
		t.Fatalf("再 Tab: %q", a.cmd)
	}
	tst.Key(ui.Ctrl, ui.KeyP)
	if a.cmd != "exportSeq" {
		t.Fatalf("C-p 回退: %q", a.cmd)
	}
}

func TestExportTarget(t *testing.T) {
	info := core.Info{Path: "/v/clip.mp4", Width: 1920, Height: 1080, FPS: 30, Count: 90, CFR: true}

	// 默认视频：绝对不覆盖源文件
	out, kind, err := exportTarget(info, nil, kindVideo)
	if err != nil {
		t.Fatal(err)
	}
	if kind != kindVideo || out != filepath.Join("/v", "clip-pcut.mp4") {
		t.Fatalf("默认视频 = %q (%v)", out, kind)
	}

	// .png 参数 -> 序列帧，并补上 %04d
	out, kind, err = exportTarget(info, []string{"/tmp/x.png"}, kindVideo)
	if err != nil {
		t.Fatal(err)
	}
	if kind != kindImages || out != "/tmp/x_%04d.png" {
		t.Fatalf("png = %q (%v)", out, kind)
	}

	// 已经带 %d 就不动
	out, _, _ = exportTarget(info, []string{"/tmp/y_%03d.png"}, kindVideo)
	if out != "/tmp/y_%03d.png" {
		t.Fatalf("带 %%d = %q", out)
	}

	// 不允许写回源文件
	if _, _, err := exportTarget(info, []string{"/v/clip.mp4"}, kindVideo); err == nil {
		t.Fatal("应该拒绝覆盖源文件")
	}

	// exportTile 默认名
	out, kind, err = exportTarget(info, nil, kindTile)
	if err != nil || kind != kindTile || out != filepath.Join("/v", "clip-tile.png") {
		t.Fatalf("tile = %q (%v) %v", out, kind, err)
	}

	// :exportSeq 是 :export XXX.png 的简写：图片扩展名照留，没写就补 .png
	seq := []struct {
		args []string
		want string
	}{
		{nil, filepath.Join("/v", "clip_%04d.png")}, // 不给名字 → 同默认序列名
		{[]string{"/tmp/s"}, "/tmp/s_%04d.png"},
		{[]string{"/tmp/s.jpg"}, "/tmp/s_%04d.jpg"},
		{[]string{"/tmp/s_%03d.webp"}, "/tmp/s_%03d.webp"}, // 已经带 %d 就不动
		{[]string{"out.mp4"}, "out.mp4_%04d.png"},          // 非图片扩展名当名字的一部分
	}
	for _, c := range seq {
		out, kind, err = exportTarget(info, c.args, kindImages)
		if err != nil || kind != kindImages || out != c.want {
			t.Fatalf("exportSeq %v = %q (%v) %v, want %q", c.args, out, kind, err, c.want)
		}
	}

	// :export 给视频扩展名还是视频，不会被 exportSeq 的规则影响
	out, kind, err = exportTarget(info, []string{"/tmp/z.mkv"}, kindVideo)
	if err != nil || kind != kindVideo || out != "/tmp/z.mkv" {
		t.Fatalf("视频 = %q (%v) %v", out, kind, err)
	}

	// exportKrita：默认名带 -krita，没写扩展名就补 .kra
	out, kind, err = exportTarget(info, nil, kindKrita)
	if err != nil || kind != kindKrita || out != filepath.Join("/v", "clip-krita.kra") {
		t.Fatalf("krita 默认名 = %q (%v) %v", out, kind, err)
	}
	for _, c := range []struct{ args, want string }{
		{"/tmp/k", "/tmp/k.kra"},
		{"/tmp/k.kra", "/tmp/k.kra"},
	} {
		out, kind, err = exportTarget(info, []string{c.args}, kindKrita)
		if err != nil || kind != kindKrita || out != c.want {
			t.Fatalf("krita %q = %q (%v) %v, want %q", c.args, out, kind, err, c.want)
		}
	}
	// :export X.kra 也走 Krita 导出
	out, kind, err = exportTarget(info, []string{"/tmp/z.kra"}, kindVideo)
	if err != nil || kind != kindKrita || out != "/tmp/z.kra" {
		t.Fatalf("export .kra = %q (%v) %v", out, kind, err)
	}
}

// 行号列的对齐靠像素断言，不靠「我觉得应该对齐」。
func ink(img *image.RGBA, x0, x1, y0, y1 int) (left, right int, ok bool) {
	count := map[uint32]int{}
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			i := img.PixOffset(x, y)
			k := uint32(img.Pix[i])<<16 | uint32(img.Pix[i+1])<<8 | uint32(img.Pix[i+2])
			count[k]++
		}
	}
	var bg uint32
	best := -1
	for k, c := range count {
		if c > best {
			bg, best = k, c
		}
	}
	left, right, ok = 1<<30, -1, false
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			i := img.PixOffset(x, y)
			k := uint32(img.Pix[i])<<16 | uint32(img.Pix[i+1])<<8 | uint32(img.Pix[i+2])
			if k != bg {
				left = min(left, x)
				right = max(right, x)
				ok = true
			}
		}
	}
	return
}

func TestLineLabel(t *testing.T) {
	cases := []struct {
		i, cursor int
		want      string
	}{
		{0, 0, "1"}, // 当前行：绝对行号，1 起
		{5, 5, "6"},
		{4, 5, "1"}, // 其他行：相对距离
		{7, 5, "2"},
		{0, 5, "5"},
	}
	for _, c := range cases {
		if got := lineLabel(c.i, c.cursor); got != c.want {
			t.Errorf("lineLabel(%d,%d) = %q, want %q", c.i, c.cursor, got, c.want)
		}
	}
}

func TestNumberColumnsAlign(t *testing.T) {
	relX0 := rowPadX
	relX1 := relX0 + relW
	absX0 := relX1 + colGap
	absX1 := absX0 + absW
	a := testApp(1200)
	tst := newTester(t, a)

	checkEdges := func(name string, x0, x1, from, to int, wantRight bool) {
		t.Helper()
		img := tst.Image()
		lo, hi := 1<<30, -1
		for row := from; row <= to; row++ {
			l, r, ok := ink(img, x0, x1, row*rowH, (row+1)*rowH)
			if !ok {
				t.Fatalf("%s: 第 %d 行没有文字", name, row)
			}
			v := l
			if wantRight {
				v = r
			}
			lo, hi = min(lo, v), max(hi, v)
		}
		if hi-lo > 2 {
			t.Fatalf("%s: 边缘跳动 %dpx (%d..%d)，对齐坏了", name, hi-lo, lo, hi)
		}
	}

	// 光标在 100：帧号列同时有 2 位数和 3 位数
	for a.cursor < 100 {
		tst.Key(0, ui.KeyJ)
	}
	tst.Frame()
	checkEdges("frame-column", absX0, absX1, 0, a.visibleRows()-1, false)

	// 回到顶部：非当前行的相对行号同时有 1 位数和 2 位数，右边缘必须齐
	tst.Key(0, ui.KeyG)
	tst.Key(0, ui.KeyG)
	tst.Frame()
	checkEdges("rel-column", relX0, relX1, 1, a.visibleRows()-1, true)
}

func TestParseTime(t *testing.T) {
	cases := map[string]float64{
		"12.5":        12.5,
		"00:00:12.5":  12.5,
		"01:02:03.25": 3723.25,
		"02:30":       150,
	}
	for in, want := range cases {
		got, err := parseTime(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got < want-0.001 || got > want+0.001 {
			t.Errorf("parseTime(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"abc", "1:2:3:4", "-"} {
		if _, err := parseTime(bad); err == nil {
			t.Errorf("parseTime(%q) 应该报错", bad)
		}
	}
}

func TestGotoFrame(t *testing.T) {
	a := testApp(100)
	a.tl.Delete(50)
	a.gotoFrame(60)
	if a.tl.Index(a.cursor) != 60 {
		t.Fatalf("gotoFrame(60) 落在帧 %d", a.tl.Index(a.cursor))
	}
	a.gotoFrame(50) // 被删了，应该落在最近的存活帧
	if idx := a.tl.Index(a.cursor); idx != 51 && idx != 49 {
		t.Fatalf("gotoFrame 到被删的帧，落在 %d", idx)
	}
}

// Go 的 flag 包遇到第一个非 flag 参数就停，但 spec 是 pcutter FILENAME -t TIME。
func TestParseFlagsInterspersed(t *testing.T) {
	cases := []struct {
		args []string
		pos  []string
		at   string
		long string
		size string
	}{
		{[]string{"a.mp4"}, []string{"a.mp4"}, "", "", "1280x800"},
		{[]string{"a.mp4", "-t", "12.5"}, []string{"a.mp4"}, "12.5", "", "1280x800"},
		{[]string{"-t", "3", "a.mp4"}, []string{"a.mp4"}, "3", "", "1280x800"},
		{[]string{"a.mp4", "--time", "00:01:00", "--size", "800x600"}, []string{"a.mp4"}, "", "00:01:00", "800x600"},
		{[]string{"-t=7", "a.mp4"}, []string{"a.mp4"}, "7", "", "1280x800"},
		{[]string{"a.mp4", "b.mp4"}, []string{"a.mp4", "b.mp4"}, "", "", "1280x800"},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		o := setupFlags(fs)
		pos := parseFlags(fs, c.args)
		if len(pos) != len(c.pos) {
			t.Fatalf("%v: pos = %v, want %v", c.args, pos, c.pos)
		}
		for i := range pos {
			if pos[i] != c.pos[i] {
				t.Fatalf("%v: pos = %v, want %v", c.args, pos, c.pos)
			}
		}
		if o.at != c.at {
			t.Errorf("%v: -t = %q, want %q", c.args, o.at, c.at)
		}
		if o.timeLong != c.long {
			t.Errorf("%v: --time = %q, want %q", c.args, o.timeLong, c.long)
		}
		if o.size != c.size {
			t.Errorf("%v: --size = %q, want %q", c.args, o.size, c.size)
		}
	}
}

// 拼图帧数超过建议值（50）要先警告，点「取消」什么都不该发生。
func TestTileWarnsWhenTooManyFrames(t *testing.T) {
	// 60 帧 1080p 原尺寸约 6x10 格，能装下但超过建议的 50 帧
	a := testApp(60)
	tst := newTester(t, a)

	tst.TypeKey(ui.Shift, ui.KeySemicolon, ":")
	tst.Type("exportTile " + filepath.Join(t.TempDir(), "t.png"))
	tst.Key(0, ui.KeyEnter)

	if a.confirm == nil {
		t.Fatal("60 帧拼图应该弹确认框")
	}
	if a.busy != "" {
		t.Fatal("确认之前不该开始导出")
	}
	if err := tst.Click("取消"); err != nil {
		t.Fatalf("点不到「取消」: %v", err)
	}
	if a.confirm != nil {
		t.Fatal("点完应该关掉确认框")
	}
	if a.busy != "" {
		t.Fatal("取消之后不该开始导出")
	}
}

// 帧数不多的时候直接开导，不打扰用户。
func TestTileStartsWithoutAsking(t *testing.T) {
	a := testApp(30)
	tst := newTester(t, a)

	tst.TypeKey(ui.Shift, ui.KeySemicolon, ":")
	// 用绝对路径，免得真跑起来时把文件写到仓库目录里
	tst.Type("exportTile " + filepath.Join(t.TempDir(), "t.png"))
	tst.Key(0, ui.KeyEnter)

	if a.confirm != nil {
		t.Fatal("30 帧不该弹框")
	}
	if a.busy == "" {
		t.Fatal("应该已经开始导出了")
	}
	if a.cancel != nil {
		a.cancel()
	}
}

// :exportSeq 不用写 .png 就能导序列帧，输出名自动带 _%04d。
func TestExportSeqShortcut(t *testing.T) {
	a := testApp(30)
	tst := newTester(t, a)

	tst.TypeKey(ui.Shift, ui.KeySemicolon, ":")
	tst.Type("exportSeq " + filepath.Join(t.TempDir(), "s"))
	tst.Key(0, ui.KeyEnter)

	if a.confirm != nil {
		t.Fatal("序列帧不该弹框")
	}
	if !strings.HasSuffix(a.busy, "s_%04d.png") {
		t.Fatalf("busy = %q（应该导序列帧）", a.busy)
	}
	if a.cancel != nil {
		a.cancel()
	}
}

// :exportKrita 走 Krita 动画导出，没写扩展名就补 .kra。
func TestExportKritaStarts(t *testing.T) {
	a := testApp(30)
	tst := newTester(t, a)

	tst.TypeKey(ui.Shift, ui.KeySemicolon, ":")
	tst.Type("exportKrita " + filepath.Join(t.TempDir(), "k"))
	tst.Key(0, ui.KeyEnter)

	if a.confirm != nil {
		t.Fatal("Krita 导出不该弹框")
	}
	if !strings.HasSuffix(a.busy, "k.kra") {
		t.Fatalf("busy = %q（应该导 .kra）", a.busy)
	}
	if a.cancel != nil {
		a.cancel()
	}
}

// :exportKrita 不给名字 → 源文件旁边生成 <名字>-krita.kra。
func TestExportKritaDefaultName(t *testing.T) {
	a := testApp(30)
	tst := newTester(t, a)

	tst.TypeKey(ui.Shift, ui.KeySemicolon, ":")
	tst.Type("exportKrita")
	tst.Key(0, ui.KeyEnter)

	if !strings.HasSuffix(a.busy, "demo-krita.kra") {
		t.Fatalf("busy = %q", a.busy)
	}
	if a.cancel != nil {
		a.cancel()
	}
}

// 拼图大到超过上限时直接拒绝，连确认都不问。
func TestTileRejectsOversized(t *testing.T) {
	a := testApp(3000)
	tst := newTester(t, a)

	tst.TypeKey(ui.Shift, ui.KeySemicolon, ":")
	tst.Type("exportTile " + filepath.Join(t.TempDir(), "t.png"))
	tst.Key(0, ui.KeyEnter)

	if a.confirm != nil {
		t.Fatal("超上限应该直接报错，不该弹确认框")
	}
	if a.busy != "" {
		t.Fatal("不该开始导出")
	}
	if !strings.Contains(a.status, "上限") {
		t.Fatalf("status = %q（应该提示上限）", a.status)
	}
}

// <C-c> 中止导出需要确认。
func TestAbortNeedsConfirmation(t *testing.T) {
	a := testApp(10)
	tst := newTester(t, a)

	cancelled := false
	a.busy = "导出 x.mp4"
	a.cancel = func() { cancelled = true }

	tst.Key(ui.Ctrl, ui.KeyC)
	if a.confirm == nil {
		t.Fatal("<C-c> 应该弹确认框")
	}
	if cancelled {
		t.Fatal("问之前不能中止")
	}
	// 先点「继续」：不该中止
	if err := tst.Click("继续"); err != nil {
		t.Fatalf("点不到「继续」: %v", err)
	}
	if cancelled {
		t.Fatal("点「继续」之后不该中止")
	}
	// 再来一次，这次点「中止」
	tst.Key(ui.Ctrl, ui.KeyC)
	if err := tst.Click("中止"); err != nil {
		t.Fatalf("点不到「中止」: %v", err)
	}
	if !cancelled {
		t.Fatal("点「中止」之后应该调用 cancel")
	}
	// Esc 关掉确认框，等同放弃中止
	cancelled = false
	a.cancel = func() { cancelled = true }
	tst.Key(ui.Ctrl, ui.KeyC)
	tst.Key(0, ui.KeyEscape)
	if a.confirm != nil || cancelled {
		t.Fatalf("Esc 应该只是关掉确认框（confirm=%v cancelled=%v）", a.confirm, cancelled)
	}
}

// Vim 的数字前缀：30j / 20G / 5d / 3gg / 3u / 2<C-o>
func TestCountPrefix(t *testing.T) {
	type digitKey struct {
		d   int
		key ui.Key
	}
	keys := []digitKey{{0, ui.Key0}, {1, ui.Key1}, {2, ui.Key2}, {3, ui.Key3}, {4, ui.Key4},
		{5, ui.Key5}, {6, ui.Key6}, {7, ui.Key7}, {8, ui.Key8}, {9, ui.Key9}}
	press := func(tst *ui.Tester, n int) {
		for _, r := range strconv.Itoa(n) {
			for _, k := range keys {
				if int(r-'0') == k.d {
					tst.Key(0, k.key)
				}
			}
		}
	}

	t.Run("30j", func(t *testing.T) {
		a := testApp(200)
		tst := newTester(t, a)
		press(tst, 30)
		tst.Key(0, ui.KeyJ)
		if a.cursor != 30 {
			t.Fatalf("30j -> %d", a.cursor)
		}
		if a.pendCount != 0 {
			t.Fatalf("count 用完要清零，got %d", a.pendCount)
		}
	})

	t.Run("k 到底夹住", func(t *testing.T) {
		a := testApp(200)
		tst := newTester(t, a)
		press(tst, 30)
		tst.Key(0, ui.KeyJ)
		press(tst, 999)
		tst.Key(0, ui.KeyK)
		if a.cursor != 0 {
			t.Fatalf("999k -> %d，应该夹在 0", a.cursor)
		}
	})

	t.Run("20G", func(t *testing.T) {
		a := testApp(200)
		tst := newTester(t, a)
		press(tst, 20)
		tst.Key(ui.Shift, ui.KeyG)
		if a.cursor != 19 {
			t.Fatalf("20G -> %d，应该是第 20 行也就是 index 19", a.cursor)
		}
	})

	t.Run("5d", func(t *testing.T) {
		a := testApp(100)
		tst := newTester(t, a)
		tst.Key(0, ui.KeyJ)
		tst.Key(0, ui.KeyJ)
		press(tst, 5)
		tst.Key(0, ui.KeyD)
		if a.count() != 95 {
			t.Fatalf("5d 之后剩 %d 帧，want 95", a.count())
		}
		// 一次撤销全回来
		tst.Key(0, ui.KeyU)
		if a.count() != 100 {
			t.Fatalf("u 之后 %d 帧，want 100", a.count())
		}
	})

	t.Run("3gg", func(t *testing.T) {
		a := testApp(200)
		tst := newTester(t, a)
		tst.Key(0, ui.KeyJ)
		tst.Key(0, ui.KeyJ)
		press(tst, 3)
		tst.Key(0, ui.KeyG)
		tst.Key(0, ui.KeyG)
		if a.cursor != 2 {
			t.Fatalf("3gg -> %d，应该是第 3 行也就是 index 2", a.cursor)
		}
	})

	t.Run("3u", func(t *testing.T) {
		a := testApp(100)
		tst := newTester(t, a)
		for i := 0; i < 3; i++ {
			tst.Key(0, ui.KeyD)
		}
		if a.count() != 97 {
			t.Fatalf("删 3 帧后剩 %d", a.count())
		}
		press(tst, 3)
		tst.Key(0, ui.KeyU)
		if a.count() != 100 {
			t.Fatalf("3u 之后 %d 帧，want 100", a.count())
		}
	})

	t.Run("2C-o", func(t *testing.T) {
		a := testApp(500)
		tst := newTester(t, a)
		press(tst, 10)
		tst.Key(0, ui.KeyJ)        // 10
		tst.Key(ui.Shift, ui.KeyG) // 499
		tst.Key(0, ui.KeyG)
		tst.Key(0, ui.KeyG) // 0
		press(tst, 2)
		tst.Key(ui.Ctrl, ui.KeyO)
		if a.cursor != 10 {
			t.Fatalf("2<C-o> -> %d，应该回到 10", a.cursor)
		}
	})

	t.Run("单独的 0 不算 count", func(t *testing.T) {
		a := testApp(100)
		tst := newTester(t, a)
		tst.Key(0, ui.KeyJ)
		tst.Key(0, ui.Key0)
		if a.pendCount != 0 {
			t.Fatalf("单独按 0 不该开始 count，got %d", a.pendCount)
		}
		tst.Key(0, ui.KeyJ)
		if a.cursor != 2 {
			t.Fatalf("0 之后再 j -> %d，want 2", a.cursor)
		}
	})

	t.Run(":30 跳行", func(t *testing.T) {
		a := testApp(100)
		tst := newTester(t, a)
		tst.TypeKey(ui.Shift, ui.KeySemicolon, ":")
		tst.Type("30")
		tst.Key(0, ui.KeyEnter)
		if a.cursor != 29 {
			t.Fatalf(":30 -> %d", a.cursor)
		}
	})
}

// Visual 里 G / gg 也要能拉选区。
func TestVisualGAndGG(t *testing.T) {
	press := func(tst *ui.Tester, n int) {
		for _, r := range strconv.Itoa(n) {
			var k ui.Key = ui.Key0
			switch r {
			case '1':
				k = ui.Key1
			case '2':
				k = ui.Key2
			case '3':
				k = ui.Key3
			}
			tst.Key(0, k)
		}
	}

	t.Run("G 拉到底", func(t *testing.T) {
		a := testApp(50)
		tst := newTester(t, a)
		tst.Key(0, ui.KeyJ)
		tst.Key(0, ui.KeyJ) // cursor 2
		tst.Key(ui.Shift, ui.KeyV)
		tst.Key(ui.Shift, ui.KeyG)
		if a.cursor != 49 {
			t.Fatalf("Visual 里 G -> %d，want 49", a.cursor)
		}
		if a.m != modeVisual {
			t.Fatalf("G 之后应该还在 Visual，got %v", a.m)
		}
		tst.Key(0, ui.KeyD)
		if a.count() != 2 { // 2..49 全删
			t.Fatalf("Visual G 后 d：剩 %d 帧，want 2", a.count())
		}
	})

	t.Run("gg 拉到头", func(t *testing.T) {
		a := testApp(50)
		tst := newTester(t, a)
		press(tst, 30)
		tst.Key(0, ui.KeyJ) // cursor 30
		tst.Key(ui.Shift, ui.KeyV)
		tst.Key(0, ui.KeyG)
		tst.Key(0, ui.KeyG)
		if a.cursor != 0 {
			t.Fatalf("Visual 里 gg -> %d，want 0", a.cursor)
		}
		tst.Key(0, ui.KeyD)
		if a.count() != 19 { // 0..30 删掉 31 帧
			t.Fatalf("Visual gg 后 d：剩 %d 帧，want 19", a.count())
		}
	})

	t.Run("count + G", func(t *testing.T) {
		a := testApp(100)
		tst := newTester(t, a)
		tst.Key(ui.Shift, ui.KeyV)
		press(tst, 2)
		tst.Key(0, ui.Key0) // 20
		tst.Key(ui.Shift, ui.KeyG)
		if a.cursor != 19 {
			t.Fatalf("Visual 20G -> %d，want 19", a.cursor)
		}
	})

	t.Run("退出 Visual 清掉 count", func(t *testing.T) {
		a := testApp(100)
		tst := newTester(t, a)
		tst.Key(ui.Shift, ui.KeyV)
		press(tst, 5)
		tst.Key(ui.Shift, ui.KeyV) // 退出
		if a.pendCount != 0 {
			t.Fatalf("退出 Visual 后 count 应该清掉，got %d", a.pendCount)
		}
	})
}

// y 复制、p 粘贴出重复帧。
func TestYankPaste(t *testing.T) {
	seq := func(a *app) []int { return append([]int(nil), a.tl.List()...) }

	t.Run("Visual y 复制选区", func(t *testing.T) {
		a := testApp(10)
		tst := newTester(t, a)
		tst.Key(0, ui.KeyJ) // 1
		tst.Key(0, ui.KeyJ) // 2
		tst.Key(ui.Shift, ui.KeyV)
		tst.Key(0, ui.KeyJ) // 3
		tst.Key(0, ui.KeyY)
		if a.m != modeNormal {
			t.Fatalf("y 之后应该回 Normal，got %v", a.m)
		}
		if len(a.clip) != 2 || a.clip[0] != 2 || a.clip[1] != 3 {
			t.Fatalf("clip = %v，want [2 3]", a.clip)
		}
		if a.cursor != 2 {
			t.Fatalf("y 之后光标应该在选区开头 2，got %d", a.cursor)
		}
		if a.count() != 10 {
			t.Fatal("y 不该改变帧数")
		}
	})

	t.Run("p 粘在光标之后", func(t *testing.T) {
		a := testApp(5)
		tst := newTester(t, a)
		tst.Key(0, ui.KeyJ) // 1
		tst.Key(0, ui.KeyY) // clip = [1]
		tst.Key(0, ui.KeyP) // 粘在 1 后面
		if got := seq(a); !sameInts(got, []int{0, 1, 1, 2, 3, 4}) {
			t.Fatalf("序列 = %v", got)
		}
		if a.cursor != 2 {
			t.Fatalf("光标应该落在粘出来的最后一行 2，got %d", a.cursor)
		}
	})

	t.Run("P 粘在光标之前", func(t *testing.T) {
		a := testApp(5)
		tst := newTester(t, a)
		tst.Key(0, ui.KeyJ) // 1
		tst.Key(0, ui.KeyY)
		tst.Key(ui.Shift, ui.KeyP)
		if got := seq(a); !sameInts(got, []int{0, 1, 1, 2, 3, 4}) {
			t.Fatalf("序列 = %v", got)
		}
		if a.cursor != 1 {
			t.Fatalf("P 之后光标应该停在第一行副本 1，got %d", a.cursor)
		}
	})

	t.Run("3p 粘三份", func(t *testing.T) {
		a := testApp(4)
		tst := newTester(t, a)
		tst.Key(0, ui.KeyY) // clip = [0]
		for _, k := range []ui.Key{ui.Key3} {
			tst.Key(0, k)
		}
		tst.Key(0, ui.KeyP)
		if got := seq(a); !sameInts(got, []int{0, 0, 0, 0, 1, 2, 3}) {
			t.Fatalf("序列 = %v", got)
		}
		if a.count() != 7 {
			t.Fatalf("Count = %d，want 7", a.count())
		}
	})

	t.Run("撤销粘贴", func(t *testing.T) {
		a := testApp(4)
		tst := newTester(t, a)
		tst.Key(ui.Shift, ui.KeyV) // 选中整行
		tst.Key(0, ui.KeyY)
		tst.Key(0, ui.KeyP)
		if a.count() != 5 {
			t.Fatalf("粘贴后 Count=%d", a.count())
		}
		tst.Key(0, ui.KeyU)
		if got := seq(a); !sameInts(got, []int{0, 1, 2, 3}) {
			t.Fatalf("撤销后 = %v", got)
		}
	})

	t.Run("空剪贴板", func(t *testing.T) {
		a := testApp(4)
		tst := newTester(t, a)
		tst.Key(0, ui.KeyP)
		if a.count() != 4 {
			t.Fatal("空剪贴板不该改动序列")
		}
		if a.status == "" {
			t.Fatal("应该提示还没有复制任何帧")
		}
	})
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
