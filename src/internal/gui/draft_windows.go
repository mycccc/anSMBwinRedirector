//go:build windows

package gui

import (
	"net/netip"

	"ansmbwinhelper/internal/config"
	"ansmbwinhelper/internal/model"
)

// draft 是 GUI 本地的编辑模型（decisions/008 **G12**）。
//
// ⛔ 它**不是** Runtime 状态，因此：
//   - 不得覆盖 Snapshot；
//   - 不得修改 ActiveRuntime；
//   - 不得直接读写配置文件（一切持久化只能经 `Controller.Apply`）；
//   - 不得用于推断运行态 / 可达性。
//
// 它只回答一件事：**"用户此刻想保存成什么"**。
type draft struct {
	// persisted 是磁盘基线（与 Snapshot.Persisted 对齐），用于计算 HasUnsaved。
	persisted []model.Target
	// targets 是草稿列表（不含已标记删除的项）。
	targets []model.Target
	// deleted 是"已从草稿移除、但仍存在于磁盘"的 ID ⇒ 渲染为"待删除"。
	deleted map[string]bool
}

func newDraft() *draft {
	return &draft{deleted: map[string]bool{}}
}

// reset 用磁盘内容重建草稿（丢弃未保存编辑）。
func (d *draft) reset(persisted []model.Target) {
	d.persisted = append([]model.Target(nil), persisted...)
	d.targets = append([]model.Target(nil), persisted...)
	d.deleted = map[string]bool{}
}

// list 返回草稿列表的副本。
func (d *draft) list() []model.Target {
	return append([]model.Target(nil), d.targets...)
}

func (d *draft) byID(id string) (model.Target, bool) {
	for _, t := range d.targets {
		if t.ID == id {
			return t, true
		}
	}
	return model.Target{}, false
}

func (d *draft) isDeleted(id string) bool { return d.deleted[id] }

// hasUnsaved 返回草稿是否与磁盘不同。
func (d *draft) hasUnsaved() bool {
	if len(d.deleted) > 0 {
		return true
	}
	if len(d.targets) != len(d.persisted) {
		return true
	}
	for i := range d.targets {
		if d.targets[i] != d.persisted[i] {
			return true
		}
	}
	return false
}

// isUnsavedTarget 报告某个 Target 自身是否与磁盘不同（不含"被删除"）。
func (d *draft) isUnsavedTarget(id string) bool {
	t, ok := d.byID(id)
	if !ok {
		return false
	}
	for _, p := range d.persisted {
		if p.ID == id {
			return p != t
		}
	}
	return true // 新增
}

// pendingCount 返回"未保存的改动项数"（新增 + 修改 + 待删除），用于托盘菜单文案。
func (d *draft) pendingCount() int {
	n := len(d.deleted)
	for i := range d.targets {
		if d.isUnsavedTarget(d.targets[i].ID) {
			n++
		}
	}
	return n
}

// isPersisted 报告该 ID 是否存在于磁盘配置。
func (d *draft) isPersisted(id string) bool {
	for _, p := range d.persisted {
		if p.ID == id {
			return true
		}
	}
	return false
}

// add 追加一个新 Target（ID 在 GUI 侧生成，与 config.NewID 同源）。
func (d *draft) add(ip netip.Addr, port uint16) error {
	id, err := config.NewID()
	if err != nil {
		return err
	}
	d.targets = append(d.targets, model.Target{ID: id, AndroidIP: ip, AndroidPort: port})
	// 新 ID 不可能同时处于待删除集合中。
	delete(d.deleted, id)
	return nil
}

// update 修改一个 Target 的 IP / 端口（**ID 不变** —— 006 §6.1：ID 是身份）。
func (d *draft) update(id string, ip netip.Addr, port uint16) bool {
	for i := range d.targets {
		if d.targets[i].ID == id {
			d.targets[i].AndroidIP = ip
			d.targets[i].AndroidPort = port
			return true
		}
	}
	// 若它当前是"待删除"，则先撤销删除再修改。
	if d.deleted[id] {
		for _, p := range d.persisted {
			if p.ID == id {
				delete(d.deleted, id)
				d.targets = append(d.targets, model.Target{ID: id, AndroidIP: ip, AndroidPort: port})
				return true
			}
		}
	}
	return false
}

// remove 把 Target 从草稿移除。
//
// 若它本来就只存在于草稿（新增未保存）⇒ 直接丢弃；否则标记为"待删除"（008 §8）。
func (d *draft) remove(id string) {
	for i := range d.targets {
		if d.targets[i].ID == id {
			d.targets = append(d.targets[:i], d.targets[i+1:]...)
			break
		}
	}
	if d.isPersisted(id) {
		d.deleted[id] = true
	} else {
		delete(d.deleted, id)
	}
}

// undoRemove 撤销"待删除"，把它放回草稿。
func (d *draft) undoRemove(id string) {
	if !d.deleted[id] {
		return
	}
	// 找回它在**磁盘基线**中的位置。
	var restored model.Target
	baseIdx := -1
	for i, p := range d.persisted {
		if p.ID == id {
			restored, baseIdx = p, i
			break
		}
	}
	if baseIdx < 0 {
		return
	}
	delete(d.deleted, id)

	// ⚠️ 必须插回**原有的相对位置**，不能简单 append：
	// 草稿与磁盘的"是否有未保存更改"是按**逐项（含顺序）**比较的（gui_windows.go 的 sameTargets）。
	// 若插到末尾，"撤销删除"这个本该"什么都没改"的操作会凭空让界面挂上
	// "⚠ 有未保存更改"，并使 Apply 无谓地重排配置文件（2026-10-07 实机验证时发现）。
	// 插入点 = 草稿中第一个"在基线里排在该目标之后"的持久化目标之前。
	pos := len(d.targets)
	for j, t := range d.targets {
		if !d.isPersisted(t.ID) {
			continue
		}
		if d.persistedIndex(t.ID) > baseIdx {
			pos = j
			break
		}
	}
	d.targets = append(d.targets, model.Target{})
	copy(d.targets[pos+1:], d.targets[pos:])
	d.targets[pos] = restored
}

// persistedIndex 返回该 ID 在磁盘基线中的序号（不存在返回 -1）。
func (d *draft) persistedIndex(id string) int {
	for i := range d.persisted {
		if d.persisted[i].ID == id {
			return i
		}
	}
	return -1
}

// pendingDeleteTargets 返回处于"待删除"状态的磁盘 Target（保持磁盘顺序）。
func (d *draft) pendingDeleteTargets() []model.Target {
	var out []model.Target
	for _, p := range d.persisted {
		if d.deleted[p.ID] {
			out = append(out, p)
		}
	}
	return out
}
