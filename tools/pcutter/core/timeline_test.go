package core

import "testing"

func newTL(n int) *Timeline {
	return NewTimeline(Info{Count: n, FPS: 30, Width: 100, Height: 100})
}

// seqOf 把序列压成容易读的一段字符串，方便断言。
func seqOf(t *Timeline) []int { return append([]int(nil), t.List()...) }

func sameSeq(a, b []int) bool {
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

func TestTimelineStartsAsIdentity(t *testing.T) {
	tl := newTL(5)
	if got := seqOf(tl); !sameSeq(got, []int{0, 1, 2, 3, 4}) {
		t.Fatalf("初始序列 = %v", got)
	}
	if tl.Count() != 5 || tl.Total() != 5 {
		t.Fatalf("Count=%d Total=%d", tl.Count(), tl.Total())
	}
	if !tl.Monotonic() {
		t.Fatal("初始序列应该是严格递增的")
	}
}

func TestTimelineDelete(t *testing.T) {
	tl := newTL(10)
	if _, ok := tl.Delete(2); !ok {
		t.Fatal("Delete(2) 失败")
	}
	if got := seqOf(tl); !sameSeq(got, []int{0, 1, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("删完序列 = %v", got)
	}
	if tl.Has(2) {
		t.Fatal("帧 2 应该没了")
	}
	if got := tl.Index(2); got != 3 {
		t.Fatalf("Index(2) = %d，want 3", got)
	}
	if got := tl.Index(9); got != -1 {
		t.Fatalf("Index(9) = %d，want -1", got)
	}
}

func TestTimelineDeleteRangeUndoRedo(t *testing.T) {
	tl := newTL(10)
	if n := tl.DeleteRange(3, 5); n != 2 {
		t.Fatalf("删了 %d 行，want 2", n)
	}
	if got := seqOf(tl); !sameSeq(got, []int{0, 1, 2, 5, 6, 7, 8, 9}) {
		t.Fatalf("序列 = %v", got)
	}
	tl.Undo()
	if got := seqOf(tl); !sameSeq(got, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("undo 之后 = %v", got)
	}
	tl.Redo()
	if got := seqOf(tl); !sameSeq(got, []int{0, 1, 2, 5, 6, 7, 8, 9}) {
		t.Fatalf("redo 之后 = %v", got)
	}
	// 新的编辑要清空 redo
	tl.Undo()
	tl.Delete(0)
	if tl.CanRedo() {
		t.Fatal("新编辑之后 redo 应该清空")
	}
	tl.DeleteRange(-5, 100) // 越界要安全
	if tl.Count() != 0 {
		t.Fatalf("全删之后 Count=%d", tl.Count())
	}
}

// 粘贴：同一个原始帧可以出现多次，这是「重复帧」的基础。
func TestTimelineInsertCopies(t *testing.T) {
	tl := newTL(6)
	// 在 index 2（原始帧 2）之后插入帧 2 的两份副本
	if n := tl.Insert(3, []int{2, 2}); n != 2 {
		t.Fatalf("插入了 %d 行", n)
	}
	want := []int{0, 1, 2, 2, 2, 3, 4, 5}
	if got := seqOf(tl); !sameSeq(got, want) {
		t.Fatalf("粘贴之后 = %v，want %v", got, want)
	}
	if tl.Monotonic() {
		t.Fatal("有重复帧时不该是严格递增的")
	}
	tl.Undo()
	if got := seqOf(tl); !sameSeq(got, []int{0, 1, 2, 3, 4, 5}) {
		t.Fatalf("undo 之后 = %v", got)
	}
	tl.Redo()
	if got := seqOf(tl); !sameSeq(got, want) {
		t.Fatalf("redo 之后 = %v，want %v", got, want)
	}
}

// 插入之后还能继续删，撤销顺序也要对。
func TestTimelineMixedEdits(t *testing.T) {
	tl := newTL(5)
	tl.DeleteRange(1, 2)   // 0 2 3 4
	tl.Insert(1, []int{0}) // 0 0 2 3 4
	tl.DeleteRange(0, 1)   // 0 2 3 4
	if got := seqOf(tl); !sameSeq(got, []int{0, 2, 3, 4}) {
		t.Fatalf("序列 = %v", got)
	}
	tl.Undo()
	if got := seqOf(tl); !sameSeq(got, []int{0, 0, 2, 3, 4}) {
		t.Fatalf("undo1 = %v", got)
	}
	tl.Undo()
	if got := seqOf(tl); !sameSeq(got, []int{0, 2, 3, 4}) {
		t.Fatalf("undo2 = %v", got)
	}
	tl.Undo()
	if got := seqOf(tl); !sameSeq(got, []int{0, 1, 2, 3, 4}) {
		t.Fatalf("undo3 = %v", got)
	}
	if tl.CanUndo() {
		t.Fatal("撤销栈应该空了")
	}
}

func TestTimelineMissingRanges(t *testing.T) {
	tl := newTL(20)
	tl.DeleteRange(5, 8) // 原始帧 5,6,7
	tl.Delete(9)         // 现在第 9 行是原始帧 12
	got := tl.MissingRanges()
	if len(got) != 2 || got[0] != (span{5, 8}) || got[1] != (span{12, 13}) {
		t.Fatalf("MissingRanges = %+v", got)
	}
}

func TestTimelineRuns(t *testing.T) {
	tl := newTL(8)
	tl.Delete(6)           // 0..5,7
	tl.Insert(3, []int{2}) // 0,1,2,2,3,4,5,7
	got := tl.Runs()
	// 0,1,2 | 2,3,4,5 | 7 —— 第二段从重复的 2 开始，往后就是连续的了
	want := []span{{0, 3}, {2, 6}, {7, 8}}
	if len(got) != len(want) {
		t.Fatalf("Runs = %+v，want %+v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("Runs = %+v，want %+v", got, want)
		}
	}
}

func TestSelectFilter(t *testing.T) {
	tl := newTL(20)
	if got := SelectFilter(tl); got != "" {
		t.Fatalf("没删帧应该返回空，got %q", got)
	}
	tl.DeleteRange(5, 9) // 原始帧 5,6,7,8
	tl.Delete(8)         // 第 8 行：原始帧 12
	got := SelectFilter(tl)
	want := "select='not(between(n\\,5\\,8)+eq(n\\,12))'"
	if got != want {
		t.Fatalf("SelectFilter =\n%q\nwant\n%q", got, want)
	}
}

// 有重复帧时必须用 trim+concat，select 表达不了。
func TestConcatFilterForDuplicates(t *testing.T) {
	tl := newTL(4)
	tl.Insert(4, []int{3}) // 0 1 2 3 3
	f, err := ConcatFilter(tl, 30, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "[0:v]trim=start_frame=0:end_frame=4,setpts=PTS-STARTPTS[v0];" +
		"[0:v]trim=start_frame=3:end_frame=4,setpts=PTS-STARTPTS[v1];" +
		"[v0][v1]concat=n=2:v=1:a=0[cat];[cat]setpts=N/(30*TB)[out]"
	if f != want {
		t.Fatalf("ConcatFilter =\n%s\nwant\n%s", f, want)
	}
}

func TestTimelinePositionOf(t *testing.T) {
	tl := newTL(100)
	tl.Delete(50)
	if got := tl.PositionOf(60); got != 59 {
		t.Fatalf("PositionOf(60) = %d，want 59", got)
	}
	if got := tl.PositionOf(50); got != 49 { // 50 被删了，落到它前面那一帧
		t.Fatalf("PositionOf(50) = %d，want 49", got)
	}
	if got := tl.PositionOf(0); got != 0 {
		t.Fatalf("PositionOf(0) = %d", got)
	}
}
