package diag

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Level 是诊断条目的级别（decisions/009 §8.2）。
//
// v1 **不落盘任何日志**（decisions/009 §8.1）：条目只存在于内存中有界的环形缓冲，
// 仅用于"复制详情"，进程退出即消失。
type Level string

const (
	LevelInfo  Level = "Info"
	LevelWarn  Level = "Warn"
	LevelError Level = "Error"
)

// Field 是结构化键值（可选）。
type Field struct {
	K string
	V string
}

// Entry 是一条诊断条目。
type Entry struct {
	Level   Level
	Time    time.Time
	Source  Source
	Message string
	Fields  []Field
}

// DefaultRingCapacity 是环形缓冲容量（decisions/009 §22.1 裁定：200 条）。
const DefaultRingCapacity = 200

// Ring 是有界内存环形缓冲。
type Ring struct {
	mu   sync.Mutex
	buf  []Entry
	next int
	full bool
	cap  int
}

// NewRing 创建缓冲；capacity <= 0 时使用 DefaultRingCapacity。
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultRingCapacity
	}
	return &Ring{buf: make([]Entry, capacity), cap: capacity}
}

// Add 追加一条条目，超出容量时覆盖最旧的一条。
func (r *Ring) Add(e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = e
	r.next++
	if r.next == r.cap {
		r.next = 0
		r.full = true
	}
}

// Info / Warn / Error 是便捷方法。
func (r *Ring) Info(src Source, format string, a ...any) {
	r.Add(Entry{Level: LevelInfo, Source: src, Message: fmt.Sprintf(format, a...)})
}

func (r *Ring) Warn(src Source, format string, a ...any) {
	r.Add(Entry{Level: LevelWarn, Source: src, Message: fmt.Sprintf(format, a...)})
}

func (r *Ring) Error(src Source, format string, a ...any) {
	r.Add(Entry{Level: LevelError, Source: src, Message: fmt.Sprintf(format, a...)})
}

// Snapshot 返回**时间倒序**（最新在前）的条目副本。
func (r *Ring) Snapshot() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.next
	if r.full {
		n = r.cap
	}
	out := make([]Entry, 0, n)
	for i := 0; i < n; i++ {
		idx := r.next - 1 - i
		if idx < 0 {
			idx += r.cap
		}
		out = append(out, r.buf[idx])
	}
	return out
}

// Len 返回当前条目数。
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return r.cap
	}
	return r.next
}

// formatEntry 渲染单条诊断条目。
func formatEntry(e Entry) string {
	ts := e.Time.Format("15:04:05")
	var b strings.Builder
	fmt.Fprintf(&b, "%s %-5s %s: %s", ts, e.Level, e.Source, e.Message)
	for _, f := range e.Fields {
		fmt.Fprintf(&b, " | %s=%s", f.K, f.V)
	}
	return b.String()
}

// FormatRecent 渲染最近条目（时间倒序）。
func FormatRecent(entries []Entry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(formatEntry(e))
		b.WriteByte('\n')
	}
	return b.String()
}
