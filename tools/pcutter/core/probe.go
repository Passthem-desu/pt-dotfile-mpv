// Package core 是 PCutter 的后端：探测视频、维护帧表、按块解码、LRU 缓存、导出。
// 它不依赖任何 UI 框架，可以用纯 go test 覆盖。
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// Info 是一个视频文件的基本信息。
type Info struct {
	Path     string
	Width    int
	Height   int
	FPS      float64 // r_frame_rate：用于把帧号换算成时间戳
	Duration float64 // 秒
	Count    int     // 总帧数
	// CFR 为真表示恒定帧率，帧号 i 的时间戳就是 i/FPS，帧表可以 O(1) 生成。
	// 为假时需要逐帧探测（很慢），v1 仍然按 FPS 近似。
	CFR  bool
	Note string // 例如「VFR 已按平均帧率近似」
}

// CheckFFmpeg 检查 PATH 里有没有可用的 ffmpeg 与 ffprobe。
func CheckFFmpeg() error {
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("PATH 里找不到 %s", name)
		}
	}
	return nil
}

type probeStream struct {
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	RFrameRate   string `json:"r_frame_rate"`
	AvgFrameRate string `json:"avg_frame_rate"`
	NbFrames     string `json:"nb_frames"`
	Duration     string `json:"duration"`
}

type probeOutput struct {
	Streams []probeStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// Probe 探测一个视频文件。它只调一次 ffprobe（毫秒级），
// 绝不逐帧列表——实测 900 帧要 3.7 秒，30 分钟的片子要 220 秒。
func Probe(path string) (Info, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate,avg_frame_rate,nb_frames,duration:format=duration",
		"-of", "json", path)
	hideWindow(cmd)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Info{}, fmt.Errorf("ffprobe 失败: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	var p probeOutput
	if err := json.Unmarshal(out, &p); err != nil {
		return Info{}, err
	}
	if len(p.Streams) == 0 {
		return Info{}, errors.New("文件里没有视频流")
	}
	s := p.Streams[0]
	fps := ratio(s.RFrameRate)
	avg := ratio(s.AvgFrameRate)
	dur := atof(s.Duration)
	if dur == 0 {
		dur = atof(p.Format.Duration)
	}
	if fps <= 0 || s.Width <= 0 || s.Height <= 0 {
		return Info{}, errors.New("读不出分辨率或帧率")
	}

	info := Info{Path: path, Width: s.Width, Height: s.Height, FPS: fps, Duration: dur}

	// CFR 判定：容器给了准确的 nb_frames，且平均帧率等于名义帧率。
	cfr := avg > 0 && math.Abs(avg-fps) < 1e-6
	if n, err := strconv.Atoi(s.NbFrames); err == nil && n > 0 && cfr {
		info.Count = n
	} else {
		info.Count = int(math.Round(dur * avg))
		if cfr {
			info.Note = "容器没给 nb_frames，按时长×帧率估算"
		}
	}
	info.CFR = cfr
	if !cfr && avg > 0 {
		info.Note = fmt.Sprintf("VFR 视频（名义 %.3f / 平均 %.3f fps），已按平均帧率近似", fps, avg)
		info.FPS = avg
	}
	if info.Count <= 0 {
		return Info{}, errors.New("算出来的帧数是 0")
	}
	return info, nil
}

// FrameTime 返回帧号 i 的时间戳（秒）。
func (in Info) FrameTime(i int) float64 {
	if in.FPS <= 0 {
		return 0
	}
	return float64(i) / in.FPS
}

// FrameAt 返回时间戳 t 秒最接近的帧号。
func (in Info) FrameAt(t float64) int {
	if in.FPS <= 0 {
		return 0
	}
	i := int(math.Round(t * in.FPS))
	return min(max(i, 0), in.Count-1)
}

func ratio(s string) float64 {
	if s == "" || s == "0/0" {
		return 0
	}
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		return atof(s)
	}
	n, d := atof(num), atof(den)
	if d == 0 {
		return 0
	}
	return n / d
}

func atof(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}
