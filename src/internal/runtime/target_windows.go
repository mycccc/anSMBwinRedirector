//go:build windows

package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"ansmbwinhelper/internal/model"
	"ansmbwinhelper/internal/winvert"
)

// forceCloseAfter 是"Shutdown 唤不醒空闲 Recv"时的强制关闭等待上限。
//
// 依据：P0.1 实测 —— 空闲 handle 的 WinDivertShutdown 无法中止挂起的 Recv，
// 必须 Close；detour 亦有同款 forceCloseAfter = 1s。
const forceCloseAfter = 1 * time.Second

// goroutineExitWait 是强制关闭后等待循环 goroutine 退出的上限。
const goroutineExitWait = 3 * time.Second

// winRuntime 是一个 Target 的 WinDivert runtime（每 Target 两个 handle）。
type winRuntime struct {
	target model.Target
	// noop 表示 AndroidPort == WindowsMatchPort（decisions/005 §6.1）：
	// 此时该 runtime 打开 handles 但**不改变任何字节**（等同观察者）。
	noop bool

	out *winvert.Session
	in  *winvert.Session
	ft  *model.FlowTable

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once

	mu  sync.Mutex
	err error

	outRewritten  atomic.Uint64
	inRewritten   atomic.Uint64
	passThrough   atomic.Uint64
	noMatch       atomic.Uint64
	notApplicable atomic.Uint64
	recvErr       atomic.Uint64
	sendErr       atomic.Uint64
}

// Start 建立 handles 并启动循环。
//
// ⚠️ L11：IN handle 打开失败必须**回滚**已打开的 OUT handle，不留半开 runtime。
func (r *winRuntime) Start() error {
	if err := winvert.Prepare(); err != nil {
		return err
	}
	fwdFilter, revFilter := model.Filters(r.target)

	out, err := winvert.OpenSession(fwdFilter, winvert.DefaultBufSize)
	if err != nil {
		return err
	}
	in, err := winvert.OpenSession(revFilter, winvert.DefaultBufSize)
	if err != nil {
		_ = out.Close() // L11 回滚
		return err
	}
	// decisions/011 T-c：安装后必须确认 SCM 里的 ImagePath 指向**本程序目录**的 .sys。
	// ⛔ 失败必须显式失败（不得继续使用来源不明的驱动），并回滚已打开的 handle（L11）。
	if err := winvert.AfterOpen(); err != nil {
		_ = in.Close()
		_ = out.Close()
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.out, r.in = out, in
	r.ft = model.NewFlowTable(0)
	// 目标为 no-op 时无需维护映射表（不改字节 ⇒ 没有"需要还原"的流）。
	r.noop = r.target.AndroidPort == model.WindowsMatchPort

	r.wg.Add(2)
	go r.loop(ctx, out, model.Outbound)
	go r.loop(ctx, in, model.Inbound)
	return nil
}

// Stop 停止并等待"确认关闭"（L4），可重复调用。
//
// 顺序：cancel → Shutdown 两个 handle → 等待 ≤1s → 强制 Close → 等待 goroutine 退出。
func (r *winRuntime) Stop() {
	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.out != nil {
			_ = r.out.Shutdown()
		}
		if r.in != nil {
			_ = r.in.Shutdown()
		}
		// 先给一个自然退出的机会（P0.1 实测：空闲 handle 常需要强制 Close）。
		if !waitGroup(&r.wg, forceCloseAfter) {
			if r.out != nil {
				_ = r.out.Close()
			}
			if r.in != nil {
				_ = r.in.Close()
			}
			waitGroup(&r.wg, goroutineExitWait)
		} else {
			// 循环已退出，仍要关闭 handle。
			if r.out != nil {
				_ = r.out.Close()
			}
			if r.in != nil {
				_ = r.in.Close()
			}
		}
		// L4：只有到这里，"已停止"才成立。
	})
}

func waitGroup(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// Err 返回导致该 runtime 退出的内部错误（正常停止时为 nil）。
func (r *winRuntime) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *winRuntime) setErr(err error) {
	r.mu.Lock()
	if r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
}

// Counters 返回分层归因计数。
func (r *winRuntime) Counters() Counters {
	return Counters{
		OutRewritten:  r.outRewritten.Load(),
		InRewritten:   r.inRewritten.Load(),
		PassThrough:   r.passThrough.Load(),
		NoMatch:       r.noMatch.Load(),
		NotApplicable: r.notApplicable.Load(),
		RecvErr:       r.recvErr.Load(),
		SendErr:       r.sendErr.Load(),
	}
}

func (r *winRuntime) loop(ctx context.Context, s *winvert.Session, dir model.Direction) {
	defer r.wg.Done()
	for {
		pkt, err := s.Recv()
		if err != nil {
			if ctx.Err() != nil || winvert.IsShutdownErr(err) {
				return // 停机路径：正常表现，不上报为用户错误
			}
			r.recvErr.Add(1)
			r.setErr(winvert.MapError(err, "运行时接收失败"))
			return
		}
		r.handle(pkt, s, dir)
	}
}

// handle 处理单个包：改写或**原样放行**。
func (r *winRuntime) handle(pkt []byte, s *winvert.Session, dir model.Direction) {
	p, ok := model.ParseTCPv4(pkt)
	if !ok {
		r.notApplicable.Add(1)
		r.send(s, pkt)
		return
	}
	key, ok := r.keyFor(p, dir)
	if !ok {
		r.noMatch.Add(1)
		r.send(s, pkt)
		return
	}
	// 回收表项（RST 立即回收；双向 FIN 回收）。
	if p.RST {
		r.ft.Observe(key, dir, true, false)
	} else if p.FIN {
		r.ft.Observe(key, dir, false, true)
	}

	switch dir {
	case model.Outbound:
		if r.noop {
			r.passThrough.Add(1)
			break
		}
		if _, ok := model.RewritePort(pkt, dir, r.target.AndroidPort); ok {
			r.ft.Record(key)
			r.outRewritten.Add(1)
		} else {
			r.notApplicable.Add(1)
		}
	case model.Inbound:
		// **未命中 ⇒ 原样放行**：这是"直连 AndroidIP:<AndroidPort> 不被误伤"的机制。
		if !r.ft.Lookup(key) {
			r.passThrough.Add(1)
			break
		}
		if _, ok := model.RewritePort(pkt, dir, model.WindowsMatchPort); ok {
			r.inRewritten.Add(1)
		} else {
			r.notApplicable.Add(1)
		}
	}
	r.send(s, pkt)
}

func (r *winRuntime) send(s *winvert.Session, pkt []byte) {
	// Send 内部会重算校验和（处理 checksum offload 下捕获到的无效校验和）。
	if err := s.Send(pkt); err != nil {
		if winvert.IsShutdownErr(err) {
			return
		}
		r.sendErr.Add(1)
	}
}

// keyFor 依据方向提取 **(WindowsSrcIP, WindowsSrcPort)** 并校验该包确实属于本 Target。
//
//	OUT：取改写**前**报文的 ip.SrcAddr + tcp.SrcPort
//	IN ：取入站报文的     ip.DstAddr + tcp.DstPort
func (r *winRuntime) keyFor(p model.TCPv4, dir model.Direction) (model.FlowKey, bool) {
	switch dir {
	case model.Outbound:
		if p.DstIP != r.target.AndroidIP || p.DstPort != model.WindowsMatchPort {
			return model.FlowKey{}, false
		}
		return model.NewFlowKey(p.SrcIP.As4(), p.SrcPort), true
	default:
		if p.SrcIP != r.target.AndroidIP || p.SrcPort != r.target.AndroidPort {
			return model.FlowKey{}, false
		}
		return model.NewFlowKey(p.DstIP.As4(), p.DstPort), true
	}
}
