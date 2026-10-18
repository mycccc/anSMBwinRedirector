#!/usr/bin/env bash
#
# build-release.sh —— 组装 anSMBwinRedirector 的 Windows x64 发布包。
#
# 用法：
#   scripts/build-release.sh <version> [release] [--update-syso] [--skip-tests]
#
# 例：
#   scripts/build-release.sh 1.0
#   scripts/build-release.sh 1.0 rc1 --skip-tests
#
# 它做四件事，**每一步都可复现、且带自动校验**（decisions/009 §15–§18 + 010 §B/§G.1）：
#
#   ① 预检：工具链 / gofmt / go vet / go test
#   ② Manifest 资源（.syso）：用 `go tool rsrc`（版本锁在 go.mod 的 tool 指令里）**重新生成**，
#      与仓库中已提交的 rsrc_amd64.syso **逐字节比对**；不一致直接失败（防"人工残留文件"）
#   ③ WinDivert 哈希链：assets/ 的实际 SHA-256 ⇔ assets_common.go 里的编译期常量
#   ④ 构建 + 组装发布包（exe + WinDivert.dll + WinDivert64.sys + language.json + LICENSE +
#      THIRD_PARTY_NOTICES.md + README.md），组装**后**再校验发布包里的副本，并打印整条可追溯链
#
# ⛔ 本脚本**不**做任何安装/卸载动作，也不会触碰系统上的 WinDivert service。

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
SRC="${REPO}/src"
CMD_PKG="./cmd/ansmbwinredirector"
MANIFEST_REL="cmd/ansmbwinredirector/anSMBwinRedirector.manifest"
SYSO_REL="cmd/ansmbwinredirector/rsrc_amd64.syso"
ASSETS_REL="internal/winvert/assets"
CONST_REL="internal/winvert/assets_common.go"
LANG_REL="internal/i18n/assets/language.json"
DIST="${REPO}/dist"          # 本地构建产物（.gitignore 忽略），与 src/ 平级

die() { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }
ok()  { printf '\033[32m✓ %s\033[0m\n' "$*"; }
say() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }

VERSION="${1:-}"
[ -n "${VERSION}" ] || die "用法: scripts/build-release.sh <version> [release] [--update-syso] [--skip-tests]"
shift || true

RELEASE="release"
UPDATE_SYSO=0
SKIP_TESTS=0
for a in "$@"; do
  case "${a}" in
    --update-syso) UPDATE_SYSO=1 ;;
    --skip-tests)  SKIP_TESTS=1 ;;
    -*)            die "未知参数: ${a}" ;;
    *)             RELEASE="${a}" ;;
  esac
done

command -v go >/dev/null || die "找不到 go"
cd "${SRC}"

GO_VERSION="$(go version | awk '{print $3}')"
GOMOD_GO="$(awk '/^go /{print $2}' go.mod)"
COMMIT="$(git -C "${REPO}" rev-parse --short HEAD 2>/dev/null || echo none)"
BUILD_TIME="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
PKG_NAME="anSMBwinRedirector-${VERSION}-win-x64"
OUT_DIR="${DIST}/release/${PKG_NAME}"

say "0. 参数"
cat <<EOF
  version      = ${VERSION}
  release      = ${RELEASE}
  commit       = ${COMMIT}
  buildTime    = ${BUILD_TIME}
  go           = ${GO_VERSION} (go.mod: go ${GOMOD_GO})
  输出         = ${OUT_DIR}
EOF

# 以下路径均相对 src/（脚本会先 cd 到 src）
# ---------------------------------------------------------------- ① 预检
say "1. 预检"
if [ -n "$(gofmt -l ./cmd ./internal || true)" ]; then gofmt -l ./cmd ./internal; die "gofmt 不干净"; fi
ok "gofmt 干净"
go vet ./... || die "go vet 失败"
ok "go vet 通过"
if [ "${SKIP_TESTS}" -eq 0 ]; then
  go test -count=1 ./... >/tmp/ansmb-test.log 2>&1 || { cat /tmp/ansmb-test.log; die "go test 失败"; }
  ok "go test 通过（$(grep -c '^ok' /tmp/ansmb-test.log) 个包）"
else
  printf '  （--skip-tests：跳过 go test）\n'
fi

# ------------------------------------------------- ② Manifest 资源可复现性
say "2. Manifest 资源（.syso）可复现性"
[ -f "${SYSO_REL}" ] || die "缺少 ${SYSO_REL}（应先由 go tool rsrc 生成并提交）"
TMP_SYSO="$(mktemp -t rsrc_XXXXXX).syso"
go tool rsrc -manifest "${MANIFEST_REL}" -arch amd64 -o "${TMP_SYSO}" || die "go tool rsrc 失败（go.mod 里应有 tool github.com/akavel/rsrc）"
if cmp -s "${TMP_SYSO}" "${SYSO_REL}"; then
  ok "重新生成的 .syso 与仓库中的**逐字节一致**（$(shasum -a 256 "${SYSO_REL}" | awk '{print substr($1,1,16)}')…）"
elif [ "${UPDATE_SYSO}" -eq 1 ]; then
  cp "${TMP_SYSO}" "${SYSO_REL}"
  ok "已用 --update-syso 更新 ${SYSO_REL}（请把这次改动提交）"
else
  die "${SYSO_REL} 与重新生成的结果不一致：要么 manifest 改了，要么仓库里的是手工残留。请用 --update-syso 重新生成并提交。"
fi
rm -f "${TMP_SYSO}"

# ------------------------------------------------------- ③ WinDivert 哈希链
say "3. WinDivert 哈希链（文件 ⇔ 编译期常量）"
[ -f "${ASSETS_REL}/WinDivert.dll" ] || die "缺少 ${ASSETS_REL}/WinDivert.dll"
[ -f "${ASSETS_REL}/WinDivert64.sys" ] || die "缺少 ${ASSETS_REL}/WinDivert64.sys"
[ -f "${ASSETS_REL}/WinDivert-LICENSE.txt" ] || die "缺少 WinDivert 许可文本（必须随包分发，decisions/009 §16）"
[ -f "${LANG_REL}" ] || die "缺少语言资源 ${LANG_REL}（必须随包分发，decisions/013 §5）"
grep -q '"zh-CN"' "${LANG_REL}" || die "language.json 缺少 zh-CN 块（decisions/013 §1）"

DLL_SHA="$(shasum -a 256 "${ASSETS_REL}/WinDivert.dll" | awk '{print $1}')"
SYS_SHA="$(shasum -a 256 "${ASSETS_REL}/WinDivert64.sys" | awk '{print $1}')"
CONST_DLL="$(sed -n 's/.*ExpectedDLLSHA256 = "\([0-9a-f]*\)".*/\1/p' "${CONST_REL}")"
CONST_SYS="$(sed -n 's/.*ExpectedSysSHA256 = "\([0-9a-f]*\)".*/\1/p' "${CONST_REL}")"
[ -n "${CONST_DLL}" ] && [ -n "${CONST_SYS}" ] || die "无法从 ${CONST_REL} 解析期望哈希常量"

[ "${DLL_SHA}" = "${CONST_DLL}" ] || die "WinDivert.dll 与源码常量不一致：文件=${DLL_SHA} 常量=${CONST_DLL}（升级 WinDivert 时必须同步更新 assets_common.go）"
[ "${SYS_SHA}" = "${CONST_SYS}" ] || die "WinDivert64.sys 与源码常量不一致：文件=${SYS_SHA} 常量=${CONST_SYS}"
ok "assets/ 与编译期常量一致"

# ------------------------------------------------------------ ④ 构建 + 组装
say "4. 构建 exe"
mkdir -p "${OUT_DIR}"
LDFLAGS="-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME} -X main.release=${RELEASE}"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "${LDFLAGS} -H windowsgui" -o "${OUT_DIR}/anSMBwinRedirector.exe" "${CMD_PKG}"
ok "已构建 anSMBwinRedirector.exe（windowsgui / amd64 / trimpath）"

say "5. 组装发布包"
cp "${ASSETS_REL}/WinDivert.dll"        "${OUT_DIR}/"
cp "${ASSETS_REL}/WinDivert64.sys"      "${OUT_DIR}/"
cp "${LANG_REL}"                        "${OUT_DIR}/language.json"
cp "${REPO}/LICENSE"                    "${OUT_DIR}/LICENSE"
cp "${REPO}/THIRD_PARTY_NOTICES.md"     "${OUT_DIR}/THIRD_PARTY_NOTICES.md"
cp "${REPO}/README.md"                  "${OUT_DIR}/README.md"

EXPECTED=(anSMBwinRedirector.exe WinDivert.dll WinDivert64.sys language.json LICENSE THIRD_PARTY_NOTICES.md README.md)
ACTUAL="$(cd "${OUT_DIR}" && ls -1 | sort)"
WANT="$(printf '%s\n' "${EXPECTED[@]}" | sort)"
[ "${ACTUAL}" = "${WANT}" ] || { printf '期望:\n%s\n实际:\n%s\n' "${WANT}" "${ACTUAL}"; die "发布包文件集合与 decisions/010 §B.1 不符"; }
ok "发布包恰好 ${#EXPECTED[@]} 个文件（010 §B.1）"

say "6. 组装后复验（发布包内的副本）"
PKG_DLL_SHA="$(shasum -a 256 "${OUT_DIR}/WinDivert.dll" | awk '{print $1}')"
PKG_SYS_SHA="$(shasum -a 256 "${OUT_DIR}/WinDivert64.sys" | awk '{print $1}')"
[ "${PKG_DLL_SHA}" = "${CONST_DLL}" ] || die "发布包里的 WinDivert.dll 哈希与常量不符"
[ "${PKG_SYS_SHA}" = "${CONST_SYS}" ] || die "发布包里的 WinDivert64.sys 哈希与常量不符"
ok "发布包内两个 WinDivert 文件与常量一致"

( cd "${DIST}/release" && shasum -a 256 "${PKG_NAME}"/* > "${PKG_NAME}.sha256" )
ok "已写出 ${DIST}/release/${PKG_NAME}.sha256"

say "可追溯链"
cat <<EOF
  上游发布物           WinDivert 2.2.2-A（x64）
  仓库内副本           ${ASSETS_REL}/{WinDivert.dll,WinDivert64.sys}
  SHA-256（DLL）       ${DLL_SHA}
  SHA-256（SYS）       ${SYS_SHA}
  编译期常量           ${CONST_REL}
                         ExpectedDLLSHA256 = ${CONST_DLL}
                         ExpectedSysSHA256 = ${CONST_SYS}
  发布包副本           ${PKG_NAME}/{WinDivert.dll,WinDivert64.sys}
  校验                 ① go test ./internal/winvert   ② 本脚本第 3 步（组装前）
                       ③ 本脚本第 6 步（组装后）
EOF

say "产物"
( cd "${OUT_DIR}" && ls -l )
printf '\n\033[32m✓ 完成:%s\033[0m\n' " ${OUT_DIR}"
printf '  提示：把整个目录拷到 Windows 上，**以管理员身份**运行 anSMBwinRedirector.exe。\n'
printf '  ⛔ 本脚本不做安装/卸载，也不会触碰系统上的 WinDivert service。\n'
