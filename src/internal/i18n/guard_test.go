package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// G13-B 守卫（decisions/013 §6.1 判据 B）：**用户可见的 GUI 文案不得绕过 localization**。
//
// 形式是一个**自收缩棘轮**（ratchet）：
//   - 处在 `pendingI18nMigration` 里的文件：允许暂时仍有字面量，但测试会打印残留数（便于跟踪进度）；
//     ⚠️ 一旦某个文件被迁移干净（0 处），**必须把它从清单里删掉**，否则本测试失败 —— 清单只能变小。
//   - **不在**清单里的文件：必须 0 处违规 —— 新代码不得再引入任何字面量。
//
// ⛔ 这是"不得绕过"的自动化边界；⛔ 不是"禁止源码里出现任何英文字符串"（判据 C 明确排除
// 内部标识 / 协议常量 / ErrorCode 等 —— 所以本测试只盯 GUI 源文件里的**字面量**）。
// 判据 B 的**检查对象 = 用户可见文案产出点**（decisions/013 §6.2），而不是"GUI 包目录"。
//
// ⚠️ 为什么必须这样划：只扫 internal/gui 会得到**假绿** —— 状态栏、错误摘要、复制详情、
// 启动阻塞提示其实由 snapshot / diag / app 生产。
//
// ⛔ **有意排除**：各包内部 `errors.New` / `fmt.Errorf` / `ErrorInfo.Detail` 等**诊断数据层**
// 文案（model / config / controller / winvert / credential / connect / runtime …）。
// 把它们拉进来会把本判据变成"全仓字符串翻译"，那不是 decisions/013 的意图（§6.2）。
var (
	// scannedDirs：整目录都是用户可见文案产出点。
	scannedDirs = []string{"internal/gui", "cmd/ansmbwinredirector"}
	// scannedFiles：目录里混有诊断数据，因此**按文件**声明用户可见的产出点。
	scannedFiles = []string{
		"internal/snapshot/snapshot.go",    // Summary()：状态摘要
		"internal/diag/codes.go",           // 错误 Summary / Note / Category
		"internal/diag/format.go",          // 复制详情的固定模板与字段标签
		"internal/app/app.go",              // 启动阻塞提示
		"internal/app/platform_windows.go", // 启动阻塞提示（平台/权限/单实例）
		"internal/app/platform_other.go",   // 启动阻塞提示（非 Windows 桩）
	}
)

// ✅ ⑤-1（2026-10-08）：internal/gui + cmd 已迁移干净。
// ✅ ⑤-2A（2026-10-08）：上游用户可见文案产出点（snapshot / diag / app，101 处）也已迁移干净。
// ⇒ 本清单**已清空**。⛔ 不允许再往里加文件；新代码一律 `T("key")`。
var pendingI18nMigration = map[string]bool{}

// stripGoComments 去掉 Go 源码中的注释（保留字符串/字符/rune 字面量）。
//
// ⚠️ 必需：注释里常出现"被引号括起来的中文说明"，那是**注释**不是 UI 文案。
// 不剥离会把它们误判成待本地化字面量（2026-10-08 首次统计时确实被虚高了 40%）。
func stripGoComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			quote := c
			b.WriteByte(c)
			i++
			for i < len(s) {
				if s[i] == '\\' && quote != '`' && i+1 < len(s) {
					b.WriteByte(s[i])
					b.WriteByte(s[i+1])
					i += 2
					continue
				}
				b.WriteByte(s[i])
				if s[i] == quote {
					i++
					break
				}
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i += 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// guiSourceDirs 见 scannedDirs（保留旧名以免混淆已删除的引用）。
var guiSourceDirs = scannedDirs

var (
	// CJK 字面量：GUI 源文件里出现即视为用户可见文案。
	reCJKLit = regexp.MustCompile(`"[^"\n]*[\p{Han}\p{Hiragana}\p{Katakana}][^"\n]*"`)
	// 纯字面量（不含格式化动词）直接交给常见 UI 设值入口。
	reSetter = regexp.MustCompile(`(?:SetText|SetTitle)\(\s*"([^"%]*)"\s*\)|\bText:\s*"([^"%]*)"`)
)

// looksLikeText 判断一个纯字面量是否"像可翻译文案"。
//
// ⚠️ 要求含**字母**：纯符号/数字（例如占位符 `"…"`、`"---"`、`"445"`）**不是**可翻译文案，
// 把它们也判成违规会迫使我们去本地化标点（判据 C 的精神：不把内部标识/常量当文案）。
func looksLikeText(lit string) bool {
	for _, r := range lit {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// scanViolations 返回该文件中"疑似用户可见字面量"的处数。
func scanViolations(src string) (cjk int, setter int) {
	cjk = len(reCJKLit.FindAllString(src, -1))
	for _, m := range reSetter.FindAllStringSubmatch(src, -1) {
		lit := m[1] + m[2]
		if strings.TrimSpace(lit) == "" {
			continue // 空占位不算文案
		}
		if !looksLikeText(lit) {
			continue // 纯符号/数字（如 "…"）不算可翻译文案
		}
		setter++
	}
	return cjk, setter
}

func TestGUIHasNoUserVisibleLiteralsOutsidePendingList(t *testing.T) {
	pkgDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(pkgDir)) // src/

	type row struct {
		file        string
		cjk, setter int
	}
	var rows []row
	found := map[string]bool{}

	// scan 记录一个源文件的违规情况（rel 为相对模块根的斜杠路径）。
	scan := func(rel, abs string) {
		b, rerr := os.ReadFile(abs)
		if rerr != nil {
			t.Fatalf("读取 %s 失败: %v", rel, rerr)
		}
		cjk, setter := scanViolations(stripGoComments(string(b)))
		if cjk == 0 && setter == 0 {
			return
		}
		found[rel] = true
		rows = append(rows, row{rel, cjk, setter})
	}

	// ① 整目录都是用户可见文案产出点。
	for _, dir := range scannedDirs {
		base := filepath.Join(root, dir)
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			scan(rel, path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// ② 目录里混有诊断数据 ⇒ 只按声明扫描其中的用户可见产出点（见 scannedFiles 注释）。
	for _, rel := range scannedFiles {
		scan(rel, filepath.Join(root, filepath.FromSlash(rel)))
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].file < rows[j].file })
	total := 0
	for _, r := range rows {
		total += r.cjk + r.setter
		if !pendingI18nMigration[r.file] {
			t.Errorf("⛔ %s 含 %d 处疑似用户可见字面量，但**不在** pendingI18nMigration 清单里 —— "+
				"新代码必须一律经 T()（decisions/013 §6.1 B）", r.file, r.cjk+r.setter)
		} else {
			t.Logf("待迁移 %-42s CJK=%d 纯字面量=%d", r.file, r.cjk, r.setter)
		}
	}
	t.Logf("GUI 字面量总计（越少越好）：%d 处", total)

	// 自收缩：已经迁移干净的文件必须从清单移除，否则清单会永远留着、失去意义。
	for f := range pendingI18nMigration {
		if found[f] {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(f))
		if _, err := os.Stat(abs); err != nil {
			t.Errorf("pendingI18nMigration 里的 %s 已不存在，请从清单移除", f)
			continue
		}
		t.Errorf("✅ %s 已无字面量 —— 请把它从 pendingI18nMigration 清单移除（清单只能变小）", f)
	}
}
