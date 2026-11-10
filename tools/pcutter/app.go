package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"pcutter/core"
)

type mode int

const (
	modeNormal mode = iota
	modeVisual
	modeCommand
)

func (m mode) String() string {
	switch m {
	case modeVisual:
		return "VISUAL"
	case modeCommand:
		return "COMMAND"
	}
	return "NORMAL"
}

const (
	rowH      = 22
	statusH   = 24
	listW     = 260
	scrolloff = 8

	// 帧列表的三列：行号 / 帧号 / 时间。列之间留 gap，
	// 否则「相对 7」和「帧号 0」会视觉上黏成「70」。
	rowPadX = 6
	relW    = 52
	absW    = 64
	colGap  = 10
)

type app struct {
	info   core.Info
	tl     *core.Timeline
	loader frameSource

	cursor int
	top    int

	m           mode
	cmd         string
	gsince      time.Time
	swallowText bool // 吞掉打开命令行那个 ':' 的 InputText

	jumps        []int
	jpos         int
	visualAnchor int
	pendCount    int   // Vim 的数字前缀（30j / 20G / 5d）
	clip         []int // y 复制下来的原始帧号
	cmdIdx       int
	compBase     string // 本轮补全使用的输入前缀，避免补全后候选集收窄
	comp         []string

	bm    *ui.Bitmap
	shown *image.RGBA

	pw, ph int // 预览解码尺寸
	height int
	winW   int
	winH   int

	svc      ui.Services
	status   string
	busy     string
	cancel   context.CancelFunc
	confirm  *confirm
	benchImg *image.RGBA
	done     chan string

	errTitle string
	errMsg   string

	// 验证用
	bench    int
	warmup   int
	frames   int
	phase    atomic.Int32
	quitOnce atomic.Bool
	startAt  time.Time
	poke     chan struct{}
	recFile  *os.File
	recFirst bool
	viewDur  []time.Duration
	starts   []time.Time
	exitAt   *time.Timer
}

func newApp(info core.Info, rec string, exitAfter float64, size string) *app {
	pw, ph := core.PreviewSize(info.Width, info.Height)
	winW, winH := parseSize(size, 1280, 800)
	a := &app{
		info:    info,
		tl:      core.NewTimeline(info),
		pw:      pw,
		ph:      ph,
		winW:    winW,
		winH:    winH,
		done:    make(chan string, 1),
		poke:    make(chan struct{}, 1),
		startAt: time.Now(),
		jpos:    -1,
	}
	a.openRec(rec, exitAfter)
	return a
}

func newBenchApp(bench, warmup int, rec, size, bm string, exitAfter float64) *app {
	w, h := parseSize(size, 1280, 800)
	info := core.Info{Path: "bench", Width: 1280, Height: 720, FPS: 30, Count: 1200, CFR: true}
	pw, ph := parseSize(bm, 640, 360)
	a := &app{
		info:    info,
		tl:      core.NewTimeline(info),
		pw:      min(pw, 1920),
		ph:      min(ph, 1080),
		winW:    w,
		winH:    h,
		bench:   bench,
		warmup:  warmup,
		poke:    make(chan struct{}, 1),
		startAt: time.Now(),
		jpos:    -1,
	}
	a.openRec(rec, exitAfter)
	return a
}

func parseSize(s string, dw, dh int) (int, int) {
	var w, h int
	if n, err := fmt.Sscanf(s, "%dx%d", &w, &h); n == 2 && err == nil && w > 0 && h > 0 {
		return w, h
	}
	return dw, dh
}

func (a *app) openRec(rec string, exitAfter float64) {
	if rec != "" {
		if f, err := os.Create(rec); err == nil {
			a.recFile = f
		}
	}
	if exitAfter > 0 {
		a.exitAt = time.AfterFunc(time.Duration(exitAfter*float64(time.Second)), func() {
			if a.recFile != nil {
				a.recFile.Sync()
			}
			mygo.App.Quit()
		})
	}
}

func (a *app) rec(format string, args ...any) {
	if a.recFile == nil {
		return
	}
	fmt.Fprintf(a.recFile, format+"\n", args...)
}

// ---- 模型操作 ----

func (a *app) count() int { return a.tl.Count() }

// gotoFrame 跳到原始帧号 idx（尽量贴近），不干扰 jumplist 无关的调用。
func (a *app) gotoFrame(idx int) {
	a.cursor = a.tl.PositionOf(idx)
	a.clamp()
}

func (a *app) clamp() {
	n := a.count()
	if n == 0 {
		a.cursor, a.top = 0, 0
		return
	}
	a.cursor = min(max(a.cursor, 0), n-1)
	a.scroll()
}

func (a *app) visibleRows() int {
	h := a.height
	if h == 0 {
		h = a.winH
	}
	if h == 0 {
		return 30
	}
	return max(1, (h-statusH)/rowH)
}

// scroll 保证光标上下各留 scrolloff 行，除非已经到顶/到底（Vim 的 scrolloff）。
func (a *app) scroll() {
	n := a.count()
	vis := a.visibleRows()
	if vis <= 2*scrolloff {
		a.top = a.cursor - vis/2
	} else {
		if a.cursor-a.top < scrolloff {
			a.top = a.cursor - scrolloff
		}
		if a.cursor-a.top > vis-1-scrolloff {
			a.top = a.cursor - (vis - 1 - scrolloff)
		}
	}
	a.top = min(max(a.top, 0), max(0, n-vis))
}

// jump 记录跳转（gg / G / -t），<C-o> <C-i> 在跳转历史里来回。
func (a *app) jump(to int) {
	n := a.count()
	if n == 0 {
		return
	}
	to = min(max(to, 0), n-1)
	if a.jpos < 0 || a.jpos >= len(a.jumps) || a.jumps[a.jpos] != a.cursor {
		if a.jpos < len(a.jumps)-1 {
			a.jumps = a.jumps[:a.jpos+1]
		}
		a.jumps = append(a.jumps, a.cursor)
		a.jpos = len(a.jumps) - 1
	} else if a.jpos < len(a.jumps)-1 {
		a.jumps = a.jumps[:a.jpos+1]
	}
	a.jumps = append(a.jumps, to)
	a.jpos = len(a.jumps) - 1
	if len(a.jumps) > 200 {
		a.jumps = a.jumps[len(a.jumps)-200:]
		a.jpos = len(a.jumps) - 1
	}
	a.cursor = to
	a.clamp()
}

func (a *app) jumpBack() {
	if a.jpos <= 0 {
		return
	}
	a.jpos--
	a.cursor = a.jumps[a.jpos]
	a.clamp()
}

func (a *app) jumpForward() {
	if a.jpos < 0 || a.jpos+1 >= len(a.jumps) {
		return
	}
	a.jpos++
	a.cursor = a.jumps[a.jpos]
	a.clamp()
}

func (a *app) moveCursor(d int) {
	n := a.count()
	if n == 0 {
		return
	}
	a.cursor = min(max(a.cursor+d, 0), n-1)
	a.scroll()
}

// ---- 视图 ----

// confirm 是一个待确认的弹窗；onPick 收到被点按钮的下标。
type confirm struct {
	open    bool
	title   string
	msg     string
	buttons []string
	onPick  func(int)
}

func (a *app) ask(title, msg string, buttons []string, onPick func(int)) {
	a.confirm = &confirm{open: true, title: title, msg: msg, buttons: buttons, onPick: onPick}
}

func (a *app) view(c *ui.Context) {
	t0 := time.Now()
	t := c.Theme()
	a.svc = c.Services()
	if _, h := c.Size(); h > 0 {
		a.height = int(h)
	}
	if a.recFile != nil && !a.recFirst {
		a.recFirst = true
		a.rec(`{"ev":"first-frame","ms":%d}`, time.Since(a.startAt).Milliseconds())
	}
	select {
	case msg := <-a.done:
		a.status = msg
		a.busy = ""
		a.cancel = nil
		a.rec(`{"ev":"status","msg":%q}`, msg)
	default:
	}

	if a.errMsg != "" {
		a.viewError(c, t)
		return
	}

	if a.bench > 0 {
		a.benchFrame(t0)
	}

	root := ui.Column(c).Fill().Background(t.Background).Focusable().AutoFocus()
	// 弹窗关掉之后 MyGo 不一定把焦点还给根元素，没有焦点就自己抢回来，
	// 否则后续按键全丢。弹窗开着的时候不要抢。
	if a.confirm == nil && !root.FocusWithin() {
		root.Focus()
	}
	root.HandleInput(a.handle)
	root.Children(func() {
		ui.Row(c).Grow(1).Children(func() {
			a.list(c, t)
			a.preview(c, t)
		})
		if a.m == modeCommand {
			ui.Row(c).Height(statusH).Padding(0, 8).AlignItems(ui.Center).Background(t.Surface).Children(func() {
				ui.Text(c, ":"+a.cmd+"\u258f").FontSize(13)
			})
		}
		a.statusLine(c, t)
	})

	if a.confirm != nil && a.confirm.open {
		k := a.confirm
		pick := ui.AlertDialog(c, &k.open, k.title, k.msg, k.buttons...)
		if pick >= 0 {
			a.confirm = nil
			if k.onPick != nil {
				k.onPick(pick)
			}
		} else if !k.open {
			a.confirm = nil
		}
	}

	if a.bench > 0 {
		if n := len(a.viewDur); n > 0 && a.viewDur[n-1] == 0 {
			a.viewDur[n-1] = time.Since(t0)
		}
		select {
		case a.poke <- struct{}{}:
		default:
		}
	}
}

func (a *app) viewError(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Padding(24).Gap(12).Children(func() {
		ui.Text(c, a.errTitle).FontSize(18).Bold()
		ui.Text(c, a.errMsg).FontSize(13).TextColor(t.TextMuted)
		if ui.PrimaryButton(c, "关闭").Clicked() {
			mygo.App.Quit()
		}
	})
}

func (a *app) list(c *ui.Context, t *ui.Theme) {
	rows := a.tl.List()
	vis := a.visibleRows()
	ui.Column(c).Width(listW).Clip().Children(func() {
		for i := a.top; i < a.top+vis && i < len(rows); i++ {
			a.row(c, t, rows[i], i)
		}
	})
}

func (a *app) row(c *ui.Context, t *ui.Theme, idx, i int) {
	selected := i == a.cursor
	inVisual := a.m == modeVisual && i >= min(a.cursor, a.visualAnchor) && i <= max(a.cursor, a.visualAnchor)
	fg, bg := t.Text, ui.Color{}
	switch {
	case selected:
		fg, bg = t.AccentText, t.Accent
	case inVisual:
		bg = t.Selection
	}
	r := ui.Row(c).Height(rowH).Padding(0, rowPadX).Gap(colGap).Background(bg).AlignItems(ui.Center)
	r.Children(func() {
		// NeoVim 那种行号：当前行是实的绝对行号（左对齐），
		// 其他行是淡的相对行号（右对齐）。
		lineNum, lineAlign, lineColor := lineLabel(i, a.cursor), ui.Start, fg
		if i != a.cursor {
			lineAlign, lineColor = ui.End, t.TextMuted
		}
		ui.Text(c, lineNum).FontSize(12).Width(relW).TextAlign(lineAlign).TextColor(lineColor)
		ui.Text(c, fmt.Sprint(idx)).FontSize(12).Width(absW).TextAlign(ui.Start).TextColor(fg)
		// 选中行的时间戳不能再用 muted，压在强调色上几乎看不见
		timeColor := t.TextMuted
		if selected {
			timeColor = t.AccentText
		}
		ui.Text(c, formatTime(a.info.FrameTime(idx))).FontSize(12).TextColor(timeColor)
	})
}

func (a *app) preview(c *ui.Context, t *ui.Theme) {
	ui.Box(c).Grow(1).Clip().Padding(8).Children(func() {
		if a.bench > 0 {
			a.benchBitmap()
			ui.Image(c, a.bm)
			return
		}
		if a.loader == nil {
			ui.Text(c, "没有预览").FontSize(12).TextColor(t.TextMuted)
			return
		}
		idx := a.tl.Index(a.cursor)
		img := a.loader.Frame(idx)
		if img == nil {
			a.shown = nil // 下次拿到一定重新上传
			ui.Text(c, "解码中…").FontSize(12).TextColor(t.TextMuted)
			return
		}
		if a.bm == nil {
			a.bm = ui.NewBitmap(img)
		} else if a.shown != img {
			a.bm.Update(img)
		}
		a.shown = img
		ui.Image(c, a.bm).Fit(ui.Contain).Fill()
	})
}

// benchBitmap 造一帧假画面并上传，量的是 上传+重建+上屏 的完整成本。
func (a *app) benchBitmap() {
	if a.bm == nil {
		a.benchImg = image.NewRGBA(image.Rect(0, 0, a.pw, a.ph))
		a.bm = ui.NewBitmap(a.benchImg)
	}
	fillFrame(a.benchImg, int(a.phase.Load()))
	a.bm.Update(a.benchImg)
}

func fillFrame(img *image.RGBA, phase int) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	band := (phase * 7) % h
	for y := 0; y < h; y++ {
		var cr, cg, cb uint8
		if y >= band && y < band+40 {
			cr, cg, cb = 250, 180, 40
		} else {
			cr, cg, cb = uint8(y*255/h), 60, uint8(255-y*255/h)
		}
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for x := 0; x < w; x++ {
			row[x*4+0] = cr
			row[x*4+1] = cg
			row[x*4+2] = cb
			row[x*4+3] = 255
		}
	}
}

func (a *app) benchFrame(t0 time.Time) {
	a.frames++
	if a.frames >= a.warmup {
		a.viewDur = append(a.viewDur, 0)
		a.starts = append(a.starts, t0)
	}
	if a.frames >= a.warmup+a.bench && a.quitOnce.CompareAndSwap(false, true) {
		go func() {
			time.Sleep(200 * time.Millisecond)
			a.printBench()
			mygo.App.Quit()
		}()
	}
}

func (a *app) printBench() {
	if len(a.starts) < 2 {
		return
	}
	gaps := make([]time.Duration, 0, len(a.starts)-1)
	for i := 1; i < len(a.starts); i++ {
		gaps = append(gaps, a.starts[i].Sub(a.starts[i-1]))
	}
	durs := a.viewDur
	var sum time.Duration
	for _, g := range gaps {
		sum += g
	}
	avg := sum / time.Duration(len(gaps))
	pct := func(v []time.Duration, p float64) time.Duration {
		if len(v) == 0 {
			return 0
		}
		c := append([]time.Duration(nil), v...)
		sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
		return c[min(len(c)-1, int(float64(len(c))*p))]
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	slow := 0
	for _, g := range gaps {
		if g > avg*3/2 {
			slow++
		}
	}
	line := fmt.Sprintf("BENCH frames=%d avg_fps=%.1f gap_p50=%.2f gap_p95=%.2f gap_max=%.2f view_p50=%.2f view_p95=%.2f view_max=%.2f slow=%d",
		len(gaps)+1, float64(len(gaps))/sum.Seconds(),
		ms(pct(gaps, 0.5)), ms(pct(gaps, 0.95)), ms(pct(gaps, 1)),
		ms(pct(durs, 0.5)), ms(pct(durs, 0.95)), ms(pct(durs, 1)), slow)
	fmt.Println(line)
	a.rec(`{"ev":"bench","line":%q}`, line)
}

func (a *app) statusLine(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Height(statusH).Padding(0, 8).Gap(16).AlignItems(ui.Center).Background(t.Surface).Children(func() {
		ui.Text(c, a.m.String()).FontSize(12).FontWeight(600)
		ui.Text(c, filepath.Base(a.info.Path)).FontSize(12)
		ui.Text(c, fmt.Sprintf("[%d/%d]", a.cursor+1, a.count())).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, formatTime(a.tl.Time(a.cursor).Seconds())).FontSize(12).TextColor(t.TextMuted)
		if a.info.Note != "" {
			ui.Text(c, a.info.Note).FontSize(11).TextColor(t.TextMuted)
		}
		if a.busy != "" {
			ui.Text(c, "⏳ "+a.busy).FontSize(12)
		}
		if a.status != "" {
			ui.Text(c, a.status).FontSize(12).TextColor(t.TextMuted)
		}
		ui.Box(c).Grow(1)
		if a.pendCount > 0 {
			ui.Text(c, strconv.Itoa(a.pendCount)).FontSize(12).TextColor(t.Accent)
		}
	})
}

// lineLabel 是 NeoVim 的行号：当前行给绝对行号（1 起），其他行给相对距离。
func lineLabel(i, cursor int) string {
	if i == cursor {
		return strconv.Itoa(i + 1)
	}
	return strconv.Itoa(abs(i - cursor))
}

func formatTime(sec float64) string {
	ms := int(sec*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

var _ = strings.Fields
