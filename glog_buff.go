package glog

import (
	"sync"
	"sync/atomic"
	"time"
	//	"unsafe"
)

var (
	poolmgr       *LogPoolMgr
	buffpoolmutex sync.RWMutex
)

type LogPoolMgr struct {
	buffpool  *sync.Pool
	nowPool   *buffArray
	usedPool  *buffArray // 先进先出队列
	tailPool  *buffArray
	nowMutex  sync.RWMutex
	usedMutex sync.RWMutex
	printChan chan int
	stopChan  chan int
}

func LogPoolMgr_GetMe() *LogPoolMgr {
	if poolmgr == nil {
		buffpoolmutex.Lock()
		if poolmgr == nil {
			poolmgr = &LogPoolMgr{
				printChan: make(chan int, 1),
				buffpool: &sync.Pool{
					New: func() interface{} {
						return &buffArray{
							buffList: make([]*buffer, 1024),
							length:   1024,
							start:    0,
							end:      0,
						}
					},
				},
			}
		}
		buffpoolmutex.Unlock()
	}
	return poolmgr
}

func (this *LogPoolMgr) init() {
	this.nowPool = this.getBuffPool()
	go this.flushLoop()
}

func (this *LogPoolMgr) flush() {
	for {
		this.usedMutex.Lock()
		printPool := this.usedPool
		if printPool == nil {
			this.tailPool = nil
			this.usedMutex.Unlock()
			break
		}
		this.usedPool = this.usedPool.next
		this.usedMutex.Unlock()
		for idx, buf := range printPool.buffList {
			if buf == nil { // 说明这个时候还没附上值(可以考虑推迟一段时间之后再打印)
				continue
			}
			if idx >= int(printPool.end) {
				break
			}
			logging.output(buf.s, buf, buf.file, buf.line, buf.alsoToStderr)
		}
		this.releaseBuffPool(printPool)
	}
}

func (this *LogPoolMgr) flushLoop() {
	var (
		timeTicker = time.NewTicker(time.Second * 2)
	)
	for {
		select {
		case <-this.printChan:
			this.flush()
		case <-this.stopChan:
			return
		case <-timeTicker.C:
			this.printNowPool(false)
		}
	}
}

func (this *LogPoolMgr) output(s severity, buf *buffer, file string, line int, alsoToStderr bool) {
	if buf == nil {
		return
	}
	buf.s = s
	buf.file = file
	buf.line = line
	buf.alsoToStderr = alsoToStderr
	nowPool := this.getNowPool()
	if nowPool == nil {
		return
	}
	if !nowPool.addBuff(buf) {
		nowPool = this.printNowPool(false)
		if nowPool == nil {
			return
		}
		nowPool.addBuff(buf)
	}
	return
}

func (this *LogPoolMgr) printNowPool(force bool) (nowPool *buffArray) {
	nowPool = this.getNowPool()
	if nowPool == nil {
		return nil
	}
	if nowPool.empty() { // 没有日志
		return
	}
	if !force && time.Now().Unix()-nowPool.t < 2 && !nowPool.full() { // 2秒或者队列满了则强制打印
		return
	}
	newPool := this.getBuffPool()
	this.nowMutex.Lock()
	nowPool = this.nowPool
	if !force && time.Now().Unix()-nowPool.t < 2 && !nowPool.full() {
		this.nowMutex.Unlock()
		return
	}
	printPool := this.nowPool
	if printPool.empty() { // 没有日志
		this.nowMutex.Unlock()
		return
	}
	nowPool = newPool
	this.nowPool = newPool
	this.nowMutex.Unlock()
	// 利用两个锁可以提高效率，但是在极端情况下可能导致数据乱序(日志暂不考虑这种情况)
	this.usedMutex.Lock()
	defer this.usedMutex.Unlock()
	printPool.next = nil
	if this.usedPool == nil {
		this.usedPool = printPool
		this.tailPool = printPool
	} else { // 将log链表加入待打印队列的尾部
		this.tailPool.next = printPool
		this.tailPool = printPool
	}
	select {
	case this.printChan <- 1:
	default:
	}
	return
}

func (this *LogPoolMgr) getNowPool() *buffArray {
	this.nowMutex.RLock()
	defer this.nowMutex.RUnlock()
	return this.nowPool
	//return (*buffArray)(atomic.LoadPointer((*unsafe.Pointer)(unsafe.Pointer(&this.nowPool))))
}

func (this *LogPoolMgr) getBuffPool() *buffArray {
	p := this.buffpool.Get().(*buffArray)
	p.t = time.Now().Unix()
	return p
}

func (this *LogPoolMgr) releaseBuffPool(pool *buffArray) {
	if pool == nil {
		return
	}
	pool.start = 0
	pool.end = 0
	pool.next = nil
	length := int(pool.length)
	for i := 0; i < length; i++ {
		pool.buffList[i] = nil
	}
	this.buffpool.Put(pool)
}

type buffArray struct {
	buffList []*buffer
	length   uint32
	start    uint32
	end      uint32
	t        int64
	next     *buffArray
}

func (this *buffArray) addBuff(buf *buffer) bool {
	end := atomic.AddUint32(&this.end, 1)
	if end > this.length {
		return false
	}
	this.buffList[int(end-1)] = buf
	return true
}

func (this *buffArray) getBuffList() []*buffer {
	return this.buffList[:int(this.end)]
}

func (this *buffArray) empty() bool {
	return atomic.LoadUint32(&this.end) == 0
}

func (this *buffArray) full() bool {
	return atomic.LoadUint32(&this.end) >= this.length
}
