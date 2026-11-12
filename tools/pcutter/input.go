package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"pcutter/core"
)

func keyName(k ui.Key) string {
	switch k {
	case ui.KeyJ:
		return "j"
	case ui.KeyK:
		return "k"
	case ui.KeyD:
		return "d"
	case ui.KeyU:
		return "u"
	case ui.KeyG:
		return "g"
	case ui.KeyV:
		return "v"
	case ui.KeyR:
		return "r"
	case ui.KeyO:
		return "o"
	case ui.KeyI:
		return "i"
	case ui.KeySemicolon:
		return "semicolon"
	case ui.KeyEnter:
		return "enter"
	case ui.KeyEscape:
		return "escape"
	case ui.KeyTab:
		return "tab"
	case ui.KeyBackspace:
		return "backspace"
	}
	return fmt.Sprintf("key%d", int(k))
}

func modName(m ui.Modifiers) string {
	var b []string
	if m&ui.Shift != 0 {
		b = append(b, "shift")
	}
	if m&ui.Ctrl != 0 {
		b = append(b, "ctrl")
	}
	if m&ui.Alt != 0 {
		b = append(b, "alt")
	}
	if len(b) == 0 {
		return "none"
	}
	return strings.Join(b, "+")
}

func (a *app) handle(ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputKeyDown:
		a.rec(`{"ev":"keydown","key":"%s","mods":"%s","text":%q,"repeat":%v}`,
			keyName(ev.Key), modName(ev.Mods), ev.Text, ev.Repeat)
	case ui.InputText:
		a.rec(`{"ev":"inputtext","text":%q}`, ev.Text)
	}

	switch ev.Kind {
	case ui.InputKeyDown:
		return a.keyDown(ev)
	case ui.InputText:
		if a.swallowText {
			// MyGo 会为可打印键额外送一个 InputText，进命令行时把 ':' 吞掉
			a.swallowText = false
			return true
		}
		if a.m == modeCommand {
			a.cmd += ev.Text
			a.resetCompletion()
			return true
		}
	}
	return false
}

// digitOf 把数字键变成 0-9，其他键返回 -1。
func digitOf(k ui.Key) int {
	switch k {
	case ui.Key0:
		return 0
	case ui.Key1:
		return 1
	case ui.Key2:
		return 2
	case ui.Key3:
		return 3
	case ui.Key4:
		return 4
	case ui.Key5:
		return 5
	case ui.Key6:
		return 6
	case ui.Key7:
		return 7
	case ui.Key8:
		return 8
	case ui.Key9:
		return 9
	}
	return -1
}

// motion 处理纯粹的移动键（j k gg G，带 count），正常模式和 Visual 模式共用。
// 处理了返回 true；和 Vim 一样，Visual 里 G / gg 会把选区一直拉到目标行。
func (a *app) motion(ev ui.InputEvent) bool {
	// g 是前缀键：3gg 的 3 要留到第二个 g 才用
	if ev.Key == ui.KeyG && ev.Mods&ui.Shift == 0 {
		if time.Since(a.gsince) < 500*time.Millisecond {
			a.jump(a.takeCount() - 1)
			a.gsince = time.Time{}
		} else {
			a.gsince = time.Now()
		}
		return true
	}
	hadCount := a.pendCount > 0
	switch {
	case ev.Key == ui.KeyJ:
		a.moveCursor(a.takeCount())
	case ev.Key == ui.KeyK:
		a.moveCursor(-a.takeCount())
	case ev.Key == ui.KeyG && ev.Mods&ui.Shift != 0:
		if hadCount {
			a.jump(a.takeCount() - 1) // 20G 是第 20 行
		} else {
			a.jump(a.count())
		}
	default:
		return false
	}
	return true
}

// pushCount 追加一位数字前缀。
func (a *app) pushCount(d int) {
	a.pendCount = min(a.pendCount*10+d, 999999)
}

// takeCount 取出数字前缀并清零；没有就给 1。
func (a *app) takeCount() int {
	n := a.pendCount
	a.pendCount = 0
	if n == 0 {
		return 1
	}
	return n
}

func (a *app) keyDown(ev ui.InputEvent) bool {
	if a.m == modeCommand {
		switch ev.Key {
		case ui.KeyEnter:
			line := a.cmd
			a.cmd, a.m = "", modeNormal
			a.rec(`{"ev":"exec","cmd":%q}`, line)
			a.exec(line)
		case ui.KeyEscape:
			a.cmd, a.m = "", modeNormal
		case ui.KeyBackspace:
			if r := []rune(a.cmd); len(r) > 0 {
				a.cmd = string(r[:len(r)-1])
				a.resetCompletion()
			}
		case ui.KeyTab:
			a.complete(1)
		case ui.KeyN:
			if ev.Mods&ui.Ctrl != 0 {
				a.complete(1)
			}
		case ui.KeyP:
			if ev.Mods&ui.Ctrl != 0 {
				a.complete(-1)
			}
		}
		a.recState()
		return true
	}

	if a.m == modeVisual {
		if d := digitOf(ev.Key); d >= 0 {
			a.pushCount(d)
			a.recState()
			return true
		}
		if a.motion(ev) {
			a.recState()
			return true
		}
		switch {
		case ev.Key == ui.KeyV && ev.Mods&ui.Shift != 0, ev.Key == ui.KeyEscape:
			a.m = modeNormal
			a.pendCount = 0
		case ev.Key == ui.KeyD:
			a.deleteVisual()
		case ev.Key == ui.KeyY:
			a.yankVisual()
		}
		a.recState()
		return true
	}

	// 数字前缀；单独一个 0 没有「行首」可去，和 Vim 一样忽略
	if d := digitOf(ev.Key); d >= 0 {
		if a.pendCount > 0 || d != 0 {
			a.pushCount(d)
		}
		a.recState()
		return true
	}

	if ev.Mods&ui.Ctrl != 0 {
		n := a.takeCount()
		switch ev.Key {
		case ui.KeyC:
			a.abortBusy()
		case ui.KeyO:
			for i := 0; i < n; i++ {
				a.jumpBack()
			}
		case ui.KeyI, ui.KeyTab:
			for i := 0; i < n; i++ {
				a.jumpForward()
			}
		case ui.KeyR:
			for i := 0; i < n; i++ {
				a.tl.Redo()
			}
			a.clamp()
		}
		a.recState()
		return true
	}

	if a.motion(ev) {
		a.recState()
		return true
	}

	n := a.takeCount()
	switch {
	case ev.Key == ui.KeyD:
		a.deleteFrames(a.cursor, n)
	case ev.Key == ui.KeyY:
		a.yank(n)
	case ev.Key == ui.KeyP && ev.Mods&ui.Shift != 0:
		a.paste(true, n)
	case ev.Key == ui.KeyP:
		a.paste(false, n)
	case ev.Key == ui.KeyU:
		for i := 0; i < n; i++ {
			a.tl.Undo()
		}
		a.clamp()
	case ev.Key == ui.KeyV && ev.Mods&ui.Shift != 0:
		a.m = modeVisual
		a.visualAnchor = a.cursor
	case ev.Key == ui.KeySemicolon:
		a.m, a.cmd = modeCommand, ""
		a.resetCompletion()
		a.swallowText = true
	case ev.Key == ui.KeyTab:
		a.jumpForward()
	}
	a.recState()
	return true
}

func (a *app) recState() {
	if a.recFile == nil {
		return
	}
	a.rec(`{"ev":"state","cursor":%d,"frames":%d,"mode":"%s","cmd":%q,"undone":%d,"redone":%d,"top":%d,"jpos":%d}`,
		a.cursor, a.count(), a.m.String(), a.cmd, boolInt(a.tl.CanUndo()), boolInt(a.tl.CanRedo()), a.top, a.jpos)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// yank 复制从光标开始的 n 行。
func (a *app) yank(n int) {
	list := a.tl.List()
	if len(list) == 0 {
		return
	}
	from := min(max(a.cursor, 0), len(list)-1)
	to := min(from+max(n, 1), len(list))
	a.clip = append(a.clip[:0], list[from:to]...)
	a.status = fmt.Sprintf("已复制 %d 帧", len(a.clip))
}

// yankVisual 复制选区（含两端），然后回到 Normal。
func (a *app) yankVisual() {
	lo, hi := min(a.cursor, a.visualAnchor), max(a.cursor, a.visualAnchor)
	list := a.tl.List()
	if len(list) == 0 {
		return
	}
	lo, hi = max(lo, 0), min(hi, len(list)-1)
	a.clip = append(a.clip[:0], list[lo:hi+1]...)
	a.cursor = lo
	a.m = modeNormal
	a.pendCount = 0
	a.status = fmt.Sprintf("已复制 %d 帧", len(a.clip))
}

// paste 把复制下来的帧插进去（粘出来的行还是活着的，所以能重复帧）。
// before 为真时插在光标行之前（Vim 的 P），否则插在之后（p）。
func (a *app) paste(before bool, times int) {
	if len(a.clip) == 0 {
		a.status = "还没有复制任何帧（V 选中后按 y）"
		return
	}
	times = max(times, 1)
	frames := make([]int, 0, len(a.clip)*times)
	for i := 0; i < times; i++ {
		frames = append(frames, a.clip...)
	}
	at := a.cursor + 1
	if before {
		at = a.cursor
	}
	if a.tl.Insert(at, frames) == 0 {
		return
	}
	if before {
		a.cursor = at
	} else {
		a.cursor = at + len(frames) - 1
	}
	a.status = fmt.Sprintf("已粘贴 %d 帧", len(frames))
	a.clamp()
}

// deleteFrames 删掉从第 i 个保留帧开始的 n 帧（Vim 的 5dd）。
func (a *app) deleteFrames(i, n int) {
	if a.tl.DeleteRange(i, i+n) > 0 {
		a.clamp()
	}
}

func (a *app) deleteVisual() {
	a.pendCount = 0 // 选区上的 d 不吃 count
	lo, hi := min(a.cursor, a.visualAnchor), max(a.cursor, a.visualAnchor)
	if a.tl.DeleteRange(lo, hi+1) > 0 {
		a.cursor = lo
		a.clamp()
	}
	a.m = modeNormal
}

// ---- 命令行 ----

var commands = []string{"export", "exportSeq", "exportTile", "q"}

func (a *app) resetCompletion() {
	a.cmdIdx = -1
	a.compBase = ""
	a.comp = a.comp[:0]
}

// complete 在命令名的候选里循环（<Tab> / <C-n> 往后，<C-p> 往前）。
// 候选集按「本轮开始补全时的前缀」算，所以补全成 exportTile 之后还能按 C-p 回到 export。
func (a *app) complete(dir int) {
	_, rest := a.cmd, ""
	if i := strings.IndexByte(a.cmd, ' '); i >= 0 {
		rest = a.cmd[i:]
	}
	if a.compBase == "" {
		word := a.cmd[:len(a.cmd)-len(rest)]
		a.compBase = word
		for _, c := range commands {
			if strings.HasPrefix(c, word) {
				a.comp = append(a.comp, c)
			}
		}
	}
	if len(a.comp) == 0 {
		return
	}
	a.cmdIdx += dir
	if a.cmdIdx < 0 {
		a.cmdIdx = len(a.comp) - 1
	}
	a.cmdIdx %= len(a.comp)
	a.cmd = a.comp[a.cmdIdx] + rest
}

func (a *app) exec(line string) {
	f := strings.Fields(line)
	if len(f) == 0 {
		return
	}
	// :30 跳到第 30 行（和 Vim 的「:行号」一致）
	if n, err := strconv.Atoi(f[0]); err == nil && len(f) == 1 {
		a.jump(n - 1)
		return
	}
	switch f[0] {
	case "export":
		a.startExport(f[1:], kindVideo)
	case "exportSeq", "exportseq":
		a.startExport(f[1:], kindImages)
	case "exportTile", "exporttile":
		a.startExport(f[1:], kindTile)
	case "q", "quit":
		mygo.App.Quit()
	default:
		a.status = "未知命令：" + f[0]
	}
}

type outKind int

const (
	kindVideo outKind = iota
	kindImages
	kindTile
)

// exportTarget 决定导出到哪、什么格式。绝不写回源文件。
func exportTarget(info core.Info, args []string, want outKind) (string, outKind, error) {
	dir := filepath.Dir(info.Path)
	base := strings.TrimSuffix(filepath.Base(info.Path), filepath.Ext(info.Path))

	out := ""
	if len(args) > 0 {
		out = args[0]
	}
	// :export 只在参数带图片扩展名时转成序列帧；:exportSeq 总是序列帧
	if want == kindVideo && isImageExt(out) {
		want = kindImages
	}
	switch want {
	case kindTile:
		if out == "" {
			out = filepath.Join(dir, base+"-tile.png")
		}
	case kindImages:
		if out == "" {
			out = filepath.Join(dir, base)
		}
		out = sequencePath(out)
	default:
		want = kindVideo
		if out == "" {
			out = filepath.Join(dir, base+"-pcut.mp4")
		}
	}
	if src, err := filepath.Abs(info.Path); err == nil {
		if dst, err := filepath.Abs(out); err == nil && src == dst {
			return "", want, fmt.Errorf("不能覆盖源文件，换个名字")
		}
	}
	return out, want, nil
}

// isImageExt 判断是不是序列帧能用的图片扩展名。
func isImageExt(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}

// sequencePath 把路径补成序列帧模板：没有图片扩展名就补 .png，
// 还没有 %d 就在扩展名前插 _%04d。
func sequencePath(out string) string {
	ext := filepath.Ext(out)
	if !isImageExt(out) {
		ext = ".png"
		out += ext
	}
	if strings.Contains(out, "%") {
		return out
	}
	return strings.TrimSuffix(out, ext) + "_%04d" + ext
}

const (
	// videoCRF 是重编码的质量参数，18 基本看不出损失。
	videoCRF = 18
)

func (a *app) startExport(args []string, want outKind) {
	if a.busy != "" {
		a.status = "还有导出任务在跑（<C-c> 可以中止）"
		return
	}
	if a.tl.Count() == 0 {
		a.status = "没有帧可以导出"
		return
	}

	// 精灵图帧数太多先问一句：平时拼图基本不超过 50 帧。
	if want == kindTile {
		// 第二个参数是每格宽度；不给就是原尺寸（1:1，不缩放）
		tileW := 0
		if len(args) > 1 {
			if v, err := strconv.Atoi(args[1]); err == nil && v > 0 {
				tileW = v
			}
			args = args[:1]
		}
		a.confirmTile(args, tileW)
		return
	}
	a.exportNow(args, want, 0)
}

func (a *app) confirmTile(args []string, tileW int) {
	n := a.tl.Count()
	w, h := core.TileSize(a.info, n, tileW)
	if max(w, h) > core.MaxTileEdge {
		a.status = fmt.Sprintf("%d 帧会拼成 %d×%d，超过 %d 上限；先删掉一些帧，或导出序列帧",
			n, w, h, core.MaxTileEdge)
		return
	}
	if n <= core.TileWarnFrames {
		a.exportNow(args, kindTile, tileW)
		return
	}
	a.ask("帧数有点多",
		fmt.Sprintf("%d 帧会拼成 %d×%d（约 %dMB）的精灵图，确定要导出吗？", n, w, h, w*h*4>>20),
		[]string{"取消", "继续"},
		func(pick int) {
			if pick == 1 {
				a.exportNow(args, kindTile, tileW)
			}
		})
}

func (a *app) exportNow(args []string, want outKind, tileW int) {
	out, kind, err := exportTarget(a.info, args, want)
	if err != nil {
		a.status = err.Error()
		return
	}
	snap := a.tl.Clone() // 导出期间用户可能继续删帧，先拍个快照
	info := a.info
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.busy = "导出 " + filepath.Base(out)
	a.status = ""
	svc := a.svc
	go func() {
		var err error
		switch kind {
		case kindImages:
			err = core.ExportImages(ctx, info, snap, out)
		case kindTile:
			err = core.ExportTile(ctx, info, snap, out, tileW)
		default:
			err = core.ExportVideo(ctx, info, snap, out, videoCRF)
		}
		switch {
		case ctx.Err() != nil:
			a.done <- "已中止导出"
		case err != nil:
			a.done <- "导出失败：" + err.Error()
		default:
			a.done <- "已导出 " + filepath.Base(out)
		}
		svc.Invalidate()
	}()
}

// abortBusy 问一句再中止正在跑的导出。
func (a *app) abortBusy() {
	if a.busy == "" || a.cancel == nil {
		return
	}
	a.ask("中止导出？", "正在"+a.busy+"，确定要中止吗？", []string{"继续", "中止"}, func(pick int) {
		if pick == 1 && a.cancel != nil {
			a.cancel()
		}
	})
}

var _ = os.Getenv
