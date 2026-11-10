package core

import (
	"container/list"
	"image"
	"sync"
	"sync/atomic"
)

const (
	// BlockSize 是一次解码的帧数。实测抽 60 帧 640×360 要 311ms、54MB；
	// 24 帧一块约 124ms、50MB，256MB 预算能放 5 块（当前 ± 1 只要 3 块）。
	BlockSize = 24
	// CacheBudget 是帧缓存的内存上限（spec 里定的 256MB）。
	CacheBudget = 256 << 20
)

// Block 是一块连续解码出来的帧。
type Block struct {
	First  int
	Frames []*image.RGBA
	bytes  int
}

// Frame 返回该块里第 idx 个原始帧的画面，不在这块里返回 nil。
func (b *Block) Frame(idx int) *image.RGBA {
	if b == nil {
		return nil
	}
	off := idx - b.First
	if off < 0 || off >= len(b.Frames) {
		return nil
	}
	return b.Frames[off]
}

// Cache 是按字节预算淘汰的 LRU。
type Cache struct {
	mu      sync.Mutex
	budget  int
	used    int
	ll      *list.List // front 是最近用过的
	entries map[int]*list.Element
}

type cacheEntry struct {
	key int
	blk *Block
}

func NewCache(budget int) *Cache {
	if budget <= 0 {
		budget = CacheBudget
	}
	return &Cache{budget: budget, ll: list.New(), entries: map[int]*list.Element{}}
}

// Get 取一块，命中时提升为最近使用。
func (c *Cache) Get(key int) *Block {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil
	}
	c.ll.MoveToFront(el)
	return el.Value.(*cacheEntry).blk
}

// Has 只看在不在，不动 LRU。
func (c *Cache) Has(key int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[key]
	return ok
}

// Put 放一块进去，超预算就把最久没用的挤出去。
func (c *Cache) Put(key int, blk *Block) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.used -= el.Value.(*cacheEntry).blk.bytes
		c.ll.Remove(el)
		delete(c.entries, key)
	}
	if blk.bytes > c.budget {
		return // 单块就超预算，不缓存，免得把别的都挤掉
	}
	el := c.ll.PushFront(&cacheEntry{key: key, blk: blk})
	c.entries[key] = el
	c.used += blk.bytes
	for c.used > c.budget {
		last := c.ll.Back()
		if last == nil {
			break
		}
		e := last.Value.(*cacheEntry)
		c.ll.Remove(last)
		delete(c.entries, e.key)
		c.used -= e.blk.bytes
	}
}

// Stats 返回当前占用与预算，给状态行用。
func (c *Cache) Stats() (used, budget, blocks int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used, c.budget, len(c.entries)
}

// Loader 在后台线程解码块，视图永远不阻塞：命中就画，没命中就返回 nil 让视图画占位符。
type Loader struct {
	info  Info
	w, h  int
	cache *Cache

	req     chan int
	mu      sync.Mutex
	pending map[int]bool

	want atomic.Int64 // 当前想要的块，用来丢弃过期的预取

	onReady func()
	quit    chan struct{}
	closed  bool
}

func NewLoader(info Info, w, h int, budget int, onReady func()) *Loader {
	l := &Loader{
		info:    info,
		w:       w,
		h:       h,
		cache:   NewCache(budget),
		req:     make(chan int, 8),
		quit:    make(chan struct{}),
		pending: map[int]bool{},
		onReady: onReady,
	}
	go l.work()
	return l
}

func (l *Loader) Cache() *Cache { return l.cache }

func (l *Loader) blocks() int { return (l.info.Count + BlockSize - 1) / BlockSize }

// Frame 返回原始帧号的画面；不在缓存里就在后台排一个解码任务并返回 nil。
func (l *Loader) Frame(idx int) *image.RGBA {
	if idx < 0 || idx >= l.info.Count {
		return nil
	}
	key := idx / BlockSize
	if blk := l.cache.Get(key); blk != nil {
		return blk.Frame(idx)
	}
	l.Request(key)
	return nil
}

// Request 请求某一块，并把相邻两块排进预取队列（spec 的要求）。
func (l *Loader) Request(key int) {
	l.want.Store(int64(key))
	for _, k := range []int{key, key + 1, key - 1} {
		l.enqueue(k)
	}
}

func (l *Loader) enqueue(key int) {
	if key < 0 || key >= l.blocks() || l.cache.Has(key) {
		return
	}
	l.mu.Lock()
	if l.closed || l.pending[key] {
		l.mu.Unlock()
		return
	}
	l.pending[key] = true
	l.mu.Unlock()
	select {
	case l.req <- key:
	default:
		// 队列满了，撤销占位，下次滚动时再排
		l.mu.Lock()
		delete(l.pending, key)
		l.mu.Unlock()
	}
}

func (l *Loader) work() {
	for {
		var key int
		select {
		case <-l.quit:
			return
		case key = <-l.req:
		}
		// 用户已经跳远了，这块钱白花，直接跳过
		if d := abs(int(l.want.Load()) - key); d > 1 {
			l.mu.Lock()
			delete(l.pending, key)
			l.mu.Unlock()
			continue
		}
		frames, err := Extract(l.info, key*BlockSize, BlockSize, l.w, l.h)
		l.mu.Lock()
		delete(l.pending, key)
		closed := l.closed
		l.mu.Unlock()
		if closed {
			return
		}
		if err != nil || len(frames) == 0 {
			continue
		}
		b := &Block{First: key * BlockSize, Frames: frames, bytes: len(frames) * l.w * l.h * 4}
		l.cache.Put(key, b)
		if l.onReady != nil {
			l.onReady()
		}
	}
}

// Close 停掉解码线程。
func (l *Loader) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.mu.Unlock()
	close(l.quit)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
