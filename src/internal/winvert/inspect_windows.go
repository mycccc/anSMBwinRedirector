//go:build windows

package winvert

import (
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/imgk/divert-go"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"ansmbwinhelper/internal/diag"
)

// InspectTimeout 是 REFLECT 层事件的收集窗口（该层会补发"打开之前已发生"的事件）。
const InspectTimeout = 400 * time.Millisecond

// Inspector 实现 controller.Inspector：**只读**的 WinDivert 使用情况检查。
//
// ⛔ v1 没有卸载能力（decisions/007 **L6**）：本文件**不得**出现
// 停止/删除服务或卸载驱动的调用 —— 这里只做查询与报告。
type Inspector struct{}

// Inspect 采集 WinDivert 诊断信息（decisions/009 §10）。
//
// ⚠️ 会先执行 Prepare()：只读检查同样需要装载 WinDivert **用户态 DLL**。
// 这**不会**安装或加载驱动 —— 所有 Open 都带 WINDIVERT_FLAG_NO_INSTALL
// （官方说明：该 flag "is useful for querying the WinDivert state using a
// WINDIVERT_LAYER_REFLECT handle"）。
func (Inspector) Inspect() diag.WinDivertInfo {
	info := diag.WinDivertInfo{Available: true}
	info.WinDivertDir = DescribeWinDivertFiles()
	if err := Prepare(); err != nil {
		// 校验/装载失败 ⇒ 无法查询，但**这不是卸载理由**（L6）。
		info.Err = err.Error()
		return info
	}
	ok, _ := VerifyWinDivertFiles()
	info.RuntimeFilesOK = ok

	collectService(&info)
	if v, err := DriverVersion(); err == nil {
		info.DriverVersion = v
	}
	consumers, err := observableConsumers(InspectTimeout)
	if err != nil {
		info.Err = err.Error()
	}
	info.Consumers = consumers
	return info
}

// collectService 查询 WinDivert 服务是否存在、ImagePath 是否属于本程序。
func collectService(info *diag.WinDivertInfo) {
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	defer m.Disconnect()

	s, err := m.OpenService("WinDivert")
	if err != nil {
		return // 服务不存在（驱动未安装）
	}
	defer s.Close()
	info.ServiceExists = true

	if cfg, err := s.Config(); err == nil {
		info.ServiceImagePath = cfg.BinaryPathName
		info.ImagePathIsOurs = imagePathIsOurs(cfg.BinaryPathName)
	}
	if st, err := s.Query(); err == nil {
		info.DriverState = stateName(st.State)
	}
}

// imagePathIsOurs 判定服务 ImagePath 是否指向**本程序自己的 WinDivert 文件目录**
// （= 可执行文件所在目录；decisions/007 §4.7.3 的检查①，位置口径见 decisions/010 §D）。
func imagePathIsOurs(p string) bool {
	dir, err := WinDivertDir()
	if err != nil || p == "" {
		return false
	}
	return strings.Contains(strings.ToLower(p), strings.ToLower(dir))
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "Stopped"
	case svc.StartPending:
		return "StartPending"
	case svc.StopPending:
		return "StopPending"
	case svc.Running:
		return "Running"
	default:
		return "Unknown"
	}
}

// observableConsumers 用公开的 WINDIVERT_LAYER_REFLECT 层枚举**可观察**的使用者。
//
// ⚠️ 关键限制（decisions/009 §10.1 / 007 §4.7.1）：该层**看不到其他 REFLECT 层 handle**。
// 因此"未发现可观察使用者"**不等于**"系统中不存在其他使用者"——
// 上层必须在诊断输出中固定声明这一点，且**不得**据此执行任何卸载。
//
// 实现方式：打开 REFLECT handle（会补发既有 handle 的事件）→ 收集一个短暂窗口 →
// 关闭 handle 并用超时兜底等待收集协程退出。
func observableConsumers(timeout time.Duration) ([]diag.Consumer, error) {
	// ⚠️ 必须带 FlagNoInstall：只读检查**不得**安装驱动
	// （decisions/007 §4.8：启动/检查阶段不加载驱动）。
	h, err := divert.Open("true", divert.LayerReflect, divert.PriorityDefault,
		divert.FlagSniff|divert.FlagRecvOnly|divert.FlagNoInstall)
	if err != nil {
		if errors.Is(err, divert.ErrServiceDoseNotExist) {
			// 驱动未安装 ⇒ 自然没有使用者，这不是错误。
			return nil, nil
		}
		return nil, MapError(err, "打开 REFLECT 层 handle 失败")
	}

	var (
		mu  sync.Mutex
		out []diag.Consumer
	)
	buf := make([]byte, 65535)
	var addr divert.Address
	done := make(chan struct{})

	go func() {
		defer close(done)
		self := uint32(os.Getpid())
		for {
			if _, err := h.Recv(buf, &addr); err != nil {
				return
			}
			r := addr.Reflect()
			// addr 会被下一次 Recv 覆盖 ⇒ 立即复制需要的字段。
			c := diag.Consumer{PID: r.ProcessID, Layer: r.Layer().String(), IsOurs: r.ProcessID == self}
			mu.Lock()
			out = append(out, c)
			mu.Unlock()
		}
	}()

	time.Sleep(timeout)
	_ = h.Shutdown(divert.ShutdownBoth)
	_ = h.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
	}

	mu.Lock()
	defer mu.Unlock()
	return append([]diag.Consumer(nil), out...), nil
}
