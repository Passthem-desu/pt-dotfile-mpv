package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"pcutter/core"
)

type options struct {
	at, timeLong  string
	bench, warmup int
	rec           string
	exitAfter     float64
	size, bm      string
}

func setupFlags(fs *flag.FlagSet) *options {
	o := &options{}
	fs.StringVar(&o.at, "t", "", "启动时跳到的时间点，如 12.5 或 00:00:12.5")
	fs.StringVar(&o.timeLong, "time", "", "同 -t")
	fs.IntVar(&o.bench, "bench", 0, "渲染 N 帧后退出（验证用）")
	fs.IntVar(&o.warmup, "warmup", 40, "验证预热帧数")
	fs.StringVar(&o.rec, "rec", "", "把事件记录成 JSONL（验证用）")
	fs.Float64Var(&o.exitAfter, "exit-after", 0, "N 秒后自动退出（验证用）")
	fs.StringVar(&o.size, "size", "1280x800", "窗口 WxH")
	fs.StringVar(&o.bm, "bm", "640x360", "验证模式下的预览位图尺寸")
	return o
}

func main() {
	fs := flag.NewFlagSet("pcutter", flag.ContinueOnError)
	o := setupFlags(fs)
	pos := parseFlags(fs, os.Args[1:])

	if o.bench > 0 && len(pos) == 0 {
		// 验证用：不打开真实视频也能跑
		runWindow(newBenchApp(o.bench, o.warmup, o.rec, o.size, o.bm, o.exitAfter))
		return
	}
	if len(pos) != 1 {
		runError("用法", "pcutter FILENAME [-t TIME]")
		return
	}

	start := o.at
	if start == "" {
		start = o.timeLong
	}
	path := pos[0]
	fail := startupCheck(path)
	if fail != nil {
		runError("打不开这个文件", fail.Error())
		return
	}

	info, err := core.Probe(path)
	if err != nil {
		runError("读不出视频信息", err.Error())
		return
	}

	app := newApp(info, o.rec, o.exitAfter, o.size)
	if start != "" {
		sec, err := parseTime(start)
		if err != nil {
			runError("时间参数不对", err.Error())
			return
		}
		app.gotoFrame(info.FrameAt(sec))
	}
	argsJSON, _ := json.Marshal(os.Args[1:]) // 合法的 JSON，方便脚本排查
	app.rec(`{"ev":"open","path":%q,"frame":%d,"total":%d,"args":%s}`, path, app.cursor, app.tl.Count(), argsJSON)
	runWindow(app)
}

// parseFlags 允许 flag 出现在文件名前后：
// Go 的 flag 包遇到第一个非 flag 参数就停，而 spec 要求 pcutter FILENAME -t 12。
func parseFlags(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' || a == "--" {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil && !isBoolFlag(f) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	_ = fs.Parse(flags)
	return pos
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

func startupCheck(path string) error {
	if err := core.CheckFFmpeg(); err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("文件不存在: %s", path)
	}
	if st.IsDir() {
		return errors.New("这是一个目录，不是视频")
	}
	return nil
}

func parseTime(s string) (float64, error) {
	if strings.Contains(s, ":") {
		var h, m int
		var sec float64
		parts := strings.Split(s, ":")
		if len(parts) > 3 {
			return 0, fmt.Errorf("%q 不是时间", s)
		}
		// hh:mm:ss(.fff) 或 mm:ss(.fff)
		num := len(parts)
		if num == 3 {
			if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil {
				return 0, fmt.Errorf("%q 不是时间", s)
			}
		}
		if _, err := fmt.Sscanf(parts[num-2], "%d", &m); err != nil {
			return 0, fmt.Errorf("%q 不是时间", s)
		}
		if _, err := fmt.Sscanf(parts[num-1], "%f", &sec); err != nil {
			return 0, fmt.Errorf("%q 不是时间", s)
		}
		return float64(h*3600+m*60) + sec, nil
	}
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil {
		return 0, fmt.Errorf("%q 不是时间", s)
	}
	return v, nil
}

// runWindow 起一个窗口显示 app，或错误页。
func runWindow(a *app) {
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:     a.title(),
			Width:     a.winW,
			Height:    a.winH,
			MinWidth:  640,
			MinHeight: 320,
			Content:   ui.View(a.view),
		})
		a.start()
	})
	if err := mygo.App.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runError 弹一个窗口报错（启动检查失败时用，不依赖任何框架能力）。
func runError(title, msg string) {
	e := &app{winW: 560, winH: 220, errTitle: title, errMsg: msg}
	runWindow(e)
}

// frameSource 让测试可以塞假数据，不用真的解码。
type frameSource interface {
	Frame(idx int) *image.RGBA
	Cache() *core.Cache
}

func (a *app) title() string {
	if a.errMsg != "" {
		return "pcutter"
	}
	return "pcutter — " + filepath.Base(a.info.Path)
}

func (a *app) start() {
	if a.info.Count > 0 && a.bench == 0 {
		a.loader = core.NewLoader(a.info, a.pw, a.ph, core.CacheBudget, func() { a.svc.Invalidate() })
	}
	if a.bench > 0 {
		go a.pump()
	}
}

// pump 在验证模式下不停请求下一帧，量吞吐上限。
func (a *app) pump() {
	for range a.poke {
		a.svc.Invalidate()
	}
}

var _ = atomic.Int32{}
var _ = time.Now
