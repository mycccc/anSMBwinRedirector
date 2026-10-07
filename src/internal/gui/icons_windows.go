//go:build windows

package gui

import (
	"image"
	"image/color"

	"github.com/lxn/walk"
)

// iconSize 是托盘图标的像素尺寸。
const iconSize = 32

// iconKind 是托盘图标的状态（decisions/008 §16 的四种图标状态）。
type iconKind int

const (
	iconStopped       iconKind = iota // 已停止
	iconRunning                       // 运行中
	iconRunningFailed                 // 运行中有失败
	iconUnavailable                   // 配置不可用
)

// 托盘图标**在运行时生成**：不引入新的二进制资源文件。
//
// ⚠️ 形状必须与"文字状态"互补：tooltip 与状态条里有文字（008 §16），
// 图标只是快速线索；008 §5.2 的"不得用颜色作为唯一区分"在这里同样适用
// —— "运行中系"用 ▶、"非运行系"用 ■，**形状不同**，不是只靠颜色。
func makeTrayIcon(k iconKind) (*walk.Icon, error) {
	img := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))

	base := color.RGBA{R: 0x77, G: 0x77, B: 0x77, A: 0xFF} // 已停止：灰
	switch k {
	case iconRunning:
		base = color.RGBA{R: 0x2E, G: 0x9E, B: 0x44, A: 0xFF} // 运行中：绿
	case iconRunningFailed:
		base = color.RGBA{R: 0xE0, G: 0x8A, B: 0x00, A: 0xFF} // 运行中有失败：橙
	case iconUnavailable:
		base = color.RGBA{R: 0xB0, G: 0x00, B: 0x00, A: 0xFF} // 配置不可用：红
	}
	fillCircle(img, iconSize/2, iconSize/2, iconSize/2-1, base)

	fg := color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	if k == iconRunning || k == iconRunningFailed {
		fillTriangle(img, 12, 9, 12, 23, 24, 16, fg) // ▶
	} else {
		fillRect(img, 11, 11, 21, 21, fg) // ■
	}
	return walk.NewIconFromImage(img)
}

func fillCircle(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	rr := r * r
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= rr {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func fillTriangle(img *image.RGBA, x0, y0, x1, y1, x2, y2 int, c color.RGBA) {
	minX := min3(x0, x1, x2)
	maxX := max3(x0, x1, x2)
	minY := min3(y0, y1, y2)
	maxY := max3(y0, y1, y2)

	edge := func(ax, ay, bx, by, px, py int) int {
		return (bx-ax)*(py-ay) - (by-ay)*(px-ax)
	}
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			d1 := edge(x0, y0, x1, y1, x, y)
			d2 := edge(x1, y1, x2, y2, x, y)
			d3 := edge(x2, y2, x0, y0, x, y)
			hasNeg := d1 < 0 || d2 < 0 || d3 < 0
			hasPos := d1 > 0 || d2 > 0 || d3 > 0
			if !(hasNeg && hasPos) {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

func max3(a, b, c int) int {
	if b > a {
		a = b
	}
	if c > a {
		a = c
	}
	return a
}
