package core

import "time"

// Timeline 是**输出序列**：每一行是一个原始帧号，同一个帧号可以出现多次
// （粘贴出来的副本），也可以完全消失（删除）。这样删除、复制、重排都能表达。
//
// 撤销/重做记录的是「在 at 处删掉了 removed、插入了 inserted」，
// 撤销就是反过来做一遍，不需要存整份快照。
type Timeline struct {
	info Info
	seq  []int

	undo []edit
	redo []edit
}

// edit 是一次编辑：在 At 处把 Removed 换成了 Inserted。
type edit struct {
	At       int
	Removed  []int
	Inserted []int
}

// span 是半开区间 [From, To)，单位是原始帧号。
type span struct{ From, To int }

// NewTimeline 建一个「原样输出」的时间线。
func NewTimeline(info Info) *Timeline {
	t := &Timeline{info: info, seq: make([]int, info.Count)}
	for i := range t.seq {
		t.seq[i] = i
	}
	return t
}

func (t *Timeline) Info() Info { return t.info }

// Total 是源文件的原始帧数。
func (t *Timeline) Total() int { return t.info.Count }

// Count 是输出序列的长度。
func (t *Timeline) Count() int { return len(t.seq) }

// List 返回输出序列（原始帧号），只读，别改。
func (t *Timeline) List() []int { return t.seq }

// Index 返回第 i 行的原始帧号，越界返回 -1。
func (t *Timeline) Index(i int) int {
	if i < 0 || i >= len(t.seq) {
		return -1
	}
	return t.seq[i]
}

// Time 返回第 i 行的时间戳（源文件里的位置）。
func (t *Timeline) Time(i int) time.Duration {
	idx := t.Index(i)
	if idx < 0 {
		return 0
	}
	return time.Duration(t.info.FrameTime(idx) * float64(time.Second))
}

// Has 报告原始帧号 idx 是否还出现在序列里。
func (t *Timeline) Has(idx int) bool {
	for _, v := range t.seq {
		if v == idx {
			return true
		}
	}
	return false
}

// PositionOf 返回原始帧号 idx 在序列里的位置（-t 启动时用来定位）。
func (t *Timeline) PositionOf(idx int) int {
	if len(t.seq) == 0 {
		return 0
	}
	pos := 0
	for pos < len(t.seq) && t.seq[pos] < idx {
		pos++
	}
	if pos >= len(t.seq) {
		return len(t.seq) - 1
	}
	if t.seq[pos] == idx {
		return pos
	}
	return max(0, pos-1)
}

// Delete 删掉第 i 行，返回被删的原始帧号。
func (t *Timeline) Delete(i int) (int, bool) {
	if i < 0 || i >= len(t.seq) {
		return 0, false
	}
	idx := t.seq[i]
	t.DeleteRange(i, i+1)
	return idx, true
}

// DeleteRange 删掉 [from, to) 这些行，返回删掉的行数。
func (t *Timeline) DeleteRange(from, to int) int {
	from, to = clampRange(from, to, len(t.seq))
	if from >= to {
		return 0
	}
	removed := cloneInts(t.seq[from:to])
	t.seq = append(t.seq[:from], t.seq[to:]...)
	t.pushEdit(edit{At: from, Removed: removed})
	return to - from
}

// Insert 在位置 at 插入若干份 frames（粘贴/复制帧）。
func (t *Timeline) Insert(at int, frames []int) int {
	if len(frames) == 0 {
		return 0
	}
	at = min(max(at, 0), len(t.seq))
	tail := append([]int(nil), t.seq[at:]...)
	t.seq = append(t.seq[:at], frames...)
	t.seq = append(t.seq, tail...)
	t.pushEdit(edit{At: at, Inserted: cloneInts(frames)})
	return len(frames)
}

func (t *Timeline) pushEdit(e edit) {
	t.undo = append(t.undo, e)
	t.redo = t.redo[:0]
}

// Undo 撤销上一次编辑。
func (t *Timeline) Undo() bool {
	if len(t.undo) == 0 {
		return false
	}
	e := t.undo[len(t.undo)-1]
	t.undo = t.undo[:len(t.undo)-1]
	t.apply(e.At, e.Inserted, e.Removed) // 反过来
	t.redo = append(t.redo, e)
	return true
}

// Redo 重做。
func (t *Timeline) Redo() bool {
	if len(t.redo) == 0 {
		return false
	}
	e := t.redo[len(t.redo)-1]
	t.redo = t.redo[:len(t.redo)-1]
	t.apply(e.At, e.Removed, e.Inserted)
	t.undo = append(t.undo, e)
	return true
}

// apply 在 at 处把 remove 这一段换成 insert。
func (t *Timeline) apply(at int, remove, insert []int) {
	from, to := clampRange(at, at+len(remove), len(t.seq))
	tail := append([]int(nil), t.seq[to:]...)
	t.seq = append(t.seq[:from], insert...)
	t.seq = append(t.seq, tail...)
}

func (t *Timeline) CanUndo() bool { return len(t.undo) > 0 }
func (t *Timeline) CanRedo() bool { return len(t.redo) > 0 }

// Clone 复制一份（导出时拍快照，Timeline 不是线程安全的）。
func (t *Timeline) Clone() *Timeline {
	c := &Timeline{info: t.info, seq: cloneInts(t.seq)}
	return c
}

// MissingRanges 返回源文件里「完全没出现」的帧区间，用来拼 select 表达式。
func (t *Timeline) MissingRanges() []span {
	seen := make([]bool, t.info.Count)
	for _, v := range t.seq {
		if v >= 0 && v < len(seen) {
			seen[v] = true
		}
	}
	var out []span
	for i := 0; i < len(seen); i++ {
		if seen[i] {
			continue
		}
		j := i
		for j < len(seen) && !seen[j] {
			j++
		}
		out = append(out, span{i, j})
		i = j - 1
	}
	return out
}

// Runs 把输出序列压成尽量少的「连续递增」片段。
// 没有重复帧时，片段数就是 保留内容被删除区间切成的段数。
func (t *Timeline) Runs() []span {
	var out []span
	for i := 0; i < len(t.seq); {
		j := i
		for j+1 < len(t.seq) && t.seq[j+1] == t.seq[j]+1 {
			j++
		}
		out = append(out, span{t.seq[i], t.seq[j] + 1})
		i = j + 1
	}
	return out
}

// Monotonic 报告序列是否严格递增（没有复制出来的重复帧）——
// 是的话导出可以走快的 select 路线。
func (t *Timeline) Monotonic() bool {
	for i := 1; i < len(t.seq); i++ {
		if t.seq[i] <= t.seq[i-1] {
			return false
		}
	}
	return true
}

func clampRange(from, to, n int) (int, int) {
	if from < 0 {
		from = 0
	}
	if to > n {
		to = n
	}
	if to < from {
		to = from
	}
	return from, to
}

func cloneInts(s []int) []int {
	out := make([]int, len(s))
	copy(out, s)
	return out
}
