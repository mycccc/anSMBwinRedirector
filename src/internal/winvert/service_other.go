//go:build !windows

package winvert

// 非 Windows 平台没有 WinDivert 服务，因此生命周期是**无操作**：
//   - queryService 报告"不存在" ⇒ BeforeOpen/AfterStop 直接通过；
//   - AfterOpen 在非 Windows 不会被调用（Factory.New 直接返回错误，见 runtime/factory_other.go）。
//
// 这样 runtime 层的单元测试可以在 macOS/Linux 上跑，而不必为生命周期再造一套替身。

func queryService() (ServiceInfo, error) { return ServiceInfo{}, nil }

func stopService() error { return nil }

func deleteService() error { return nil }

func fileOpenable(string) bool { return true }
