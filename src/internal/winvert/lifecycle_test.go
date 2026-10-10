package winvert

import (
	"errors"
	"testing"
	"time"
)

// --- waitServiceGone：**轮询 + 超时**（decisions/011 T2）-------------------

func TestWaitServiceGoneReturnsWhenServiceDisappears(t *testing.T) {
	calls := 0
	query := func() (ServiceInfo, error) {
		calls++
		if calls < 3 {
			return ServiceInfo{Exists: true, State: "STOPPED"}, nil
		}
		return ServiceInfo{}, nil
	}
	if err := waitServiceGone(query, time.Second, time.Millisecond); err != nil {
		t.Fatalf("应等到服务消失: %v", err)
	}
	if calls < 3 {
		t.Errorf("必须是**轮询**（实测调用 %d 次）", calls)
	}
}

func TestWaitServiceGoneTimesOut(t *testing.T) {
	query := func() (ServiceInfo, error) {
		return ServiceInfo{Exists: true, State: "STOP_PENDING"}, nil
	}
	err := waitServiceGone(query, 30*time.Millisecond, 5*time.Millisecond)
	if err == nil {
		t.Fatal("服务始终存在 ⇒ 必须超时报错（不得 sleep-then-assume）")
	}
}

func TestWaitServiceGonePropagatesQueryError(t *testing.T) {
	sentinel := errors.New("SCM 不可用")
	qerr := waitServiceGone(func() (ServiceInfo, error) { return ServiceInfo{}, sentinel },
		time.Second, time.Millisecond)
	if qerr == nil || !errors.Is(qerr, sentinel) {
		t.Fatalf("查询失败必须原样上报，得到 %v", qerr)
	}
}

func TestWaitServiceGoneReturnsImmediatelyWhenAbsent(t *testing.T) {
	calls := 0
	if err := waitServiceGone(func() (ServiceInfo, error) {
		calls++
		return ServiceInfo{}, nil
	}, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("已不存在时应只查询一次，得到 %d", calls)
	}
}

// --- ImagePath 判定（decisions/011 T-c）------------------------------------

func TestImagePathMatches(t *testing.T) {
	dir := `C:\ansmb-rc`
	cases := map[string]bool{
		`\??\C:\ansmb-rc\WinDivert64.sys`:              true, // SCM 的常见形式
		`C:\ansmb-rc\WinDivert64.sys`:                  true,
		`\??\c:\ANSMB-RC\windivert64.SYS`:              true, // 大小写不敏感
		`\??\C:\ansmb-rc\WinDivert64.sys `:             true,
		`\??\C:\Windows\Temp\ansmb-p0\WinDivert64.sys`: false, // 旧路径 ⇒ 必须判否
		`\??\C:\ansmb-rc2\WinDivert64.sys`:             false,
		``:                                             false,
	}
	for in, want := range cases {
		if got := imagePathMatches(in, dir); got != want {
			t.Errorf("imagePathMatches(%q) = %v, 期望 %v", in, got, want)
		}
	}
}

func TestNormalizeImagePath(t *testing.T) {
	cases := map[string]string{
		`\??\C:\ansmb-rc\WinDivert64.sys`: "c:\\ansmb-rc\\windivert64.sys",
		`  C:\X\y.sys  `:                  "c:\\x\\y.sys",
		`C:/X/y.sys`:                      "c:\\x\\y.sys", // 分隔符统一
	}
	for in, want := range cases {
		if got := normalizeImagePath(in); got != want {
			t.Errorf("normalizeImagePath(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// --- removeServiceAndWait：**stop 不够，必须显式 delete**（真机缺陷回归）------

func TestRemoveServiceDeletesEvenWhenStopDoesNothing(t *testing.T) {
	// 回归用例（2026-10-08 真机测试发现的缺陷）：
	// 一个"遗留但从未启动"的服务，`stop` 是空操作、注册项仍然存在；
	// 只靠轮询永远等不到 1060 ⇒ **必须显式 delete**。
	gone := false
	var order []string
	query := func() (ServiceInfo, error) {
		if gone {
			return ServiceInfo{}, nil
		}
		return ServiceInfo{Exists: true, State: "STOPPED"}, nil
	}
	err := removeServiceAndWait(
		func() error { order = append(order, "stop"); return nil }, // 空操作，服务仍在
		func() error { order = append(order, "delete"); gone = true; return nil },
		query, time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("应通过 delete 达成目的: %v", err)
	}
	if len(order) != 2 || order[0] != "stop" || order[1] != "delete" {
		t.Fatalf("顺序必须是 stop → delete，得到 %v", order)
	}
}

func TestRemoveServiceTimesOutWhenStillPresent(t *testing.T) {
	err := removeServiceAndWait(
		func() error { return nil },
		func() error { return nil }, // 删不掉
		func() (ServiceInfo, error) { return ServiceInfo{Exists: true, State: "STOPPED"}, nil },
		30*time.Millisecond, 5*time.Millisecond)
	if err == nil {
		t.Fatal("服务始终存在 ⇒ 必须超时报错")
	}
}

func TestRemoveServicePropagatesStopAndDeleteErrors(t *testing.T) {
	sentinel := errors.New("SCM 拒绝")
	noop := func() (ServiceInfo, error) { return ServiceInfo{}, nil }
	if err := removeServiceAndWait(
		func() error { return sentinel }, nil, noop, time.Second, time.Millisecond); !errors.Is(err, sentinel) {
		t.Errorf("stop 失败应上报，得到 %v", err)
	}
	if err := removeServiceAndWait(
		nil, func() error { return sentinel }, noop, time.Second, time.Millisecond); !errors.Is(err, sentinel) {
		t.Errorf("delete 失败应上报，得到 %v", err)
	}
}

// --- waitFileUnlocked（decisions/011 §2.4 的"确认 .sys 已解锁"）------------

func TestWaitFileUnlockedPollsUntilOpenable(t *testing.T) {
	calls := 0
	err := waitFileUnlocked("/tmp/x.sys", time.Second, time.Millisecond, func(string) bool {
		calls++
		return calls >= 3
	})
	if err != nil {
		t.Fatalf("应在解锁后返回: %v", err)
	}
	if calls < 3 {
		t.Errorf("必须是轮询，得到 %d 次", calls)
	}
}

func TestWaitFileUnlockedTimesOut(t *testing.T) {
	err := waitFileUnlocked("/tmp/x.sys", 20*time.Millisecond, 5*time.Millisecond, func(string) bool { return false })
	if err == nil {
		t.Fatal("始终被占用 ⇒ 必须超时报错")
	}
}
