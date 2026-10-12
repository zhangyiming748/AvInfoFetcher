#!/usr/bin/env bash
#
# cookies2secret.sh —— 过滤 / 转换 Netscape cookies.txt
#
# 用法:
#   1) 就地过滤文件（默认）：只保留与指定域名相关的条目，直接改写文件，自动备份
#        ./scripts/cookies2secret.sh <域名> <cookies.txt>
#        例: ./scripts/cookies2secret.sh javdb.com cookies.txt
#
#   2) 查看文件里都有哪些域名:
#        ./scripts/cookies2secret.sh --list-domains <cookies.txt>
#
#   3) 过滤并输出单行 Cookie（不改动原文件，用于粘贴进 GitHub Secret）:
#        ./scripts/cookies2secret.sh --secret <域名> <cookies.txt>
#
# 域名匹配为双向匹配：javdb.com 可匹配 www.javdb.com / .javdb.com，反之亦然。

set -euo pipefail

mode="filter"   # filter | list | secret
if [[ "${1:-}" == "--list-domains" ]]; then
  mode="list"
  shift
elif [[ "${1:-}" == "--secret" ]]; then
  mode="secret"
  shift
fi

if [[ $mode == "list" ]]; then
  [[ $# -eq 1 ]] || { echo "用法: $0 --list-domains <cookies.txt>" >&2; exit 1; }
  domain_filter=""
  file="$1"
else
  [[ $# -eq 2 ]] || {
    echo "用法:" >&2
    echo "  $0 <域名> <cookies.txt>            # 就地过滤文件（自动备份为 .bak）" >&2
    echo "  $0 --secret <域名> <cookies.txt>   # 输出单行 Cookie，不改原文件" >&2
    echo "  $0 --list-domains <cookies.txt>    # 列出文件包含的域名" >&2
    exit 1
  }
  domain_filter="$1"
  file="$2"
fi

if [[ ! -f "$file" ]]; then
  echo "错误: 找不到文件 $file" >&2
  exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "错误: 需要 python3（macOS 自带）" >&2
  exit 1
fi

python3 - "$file" "$domain_filter" "$mode" <<'PY'
import os, shutil, sys, tempfile, time

path, want, mode = sys.argv[1], sys.argv[2].lstrip(".").lower(), sys.argv[3]
now = int(time.time())

# 解析一行 Netscape cookie；返回 (domain, expires, name, value, 原始行)
def parse_line(line):
    body = line.rstrip("\r\n")
    if not body or body.startswith("#"):
        if not body.startswith("#HttpOnly_"):  # curl 的 HttpOnly 行需要保留
            return None
    f = body.split("\t")
    if len(f) < 7:
        return None
    dom = f[0]
    if dom.startswith("#HttpOnly_"):
        dom = dom[len("#HttpOnly_"):]
    try:
        expires = int(f[4])
    except ValueError:
        expires = 0
    return dom.lstrip(".").lower(), expires, f[5], f[6], body

def domain_match(dom):
    return dom == want or dom.endswith("." + want) or want.endswith("." + dom)

with open(path, "r", encoding="utf-8", errors="replace", newline="") as fh:
    lines = fh.readlines()

# --list-domains：列出文件中所有域名及统计
if mode == "list":
    stats = {}
    for line in lines:
        r = parse_line(line)
        if not r:
            continue
        dom, exp = r[0], r[1]
        total, expired = stats.get(dom, (0, 0))
        stats[dom] = (total + 1, expired + (1 if exp != 0 and exp < now else 0))
    if not stats:
        print("文件里没有可识别的 cookie 行", file=sys.stderr)
        sys.exit(2)
    print(f"{'域名':<40} {'总数':>4} {'已过期':>6}")
    for dom in sorted(stats):
        total, expired = stats[dom]
        print(f"{dom:<40} {total:>4} {expired:>6}")
    sys.exit(0)

kept_lines = []   # 过滤后保留的原始行（Netscape 格式）
pairs = []        # 单行 Cookie 用
seen = set()
kept = dropped = 0

# 保留文件开头的注释头（如 "# Netscape HTTP Cookie File"）
for line in lines:
    body = line.rstrip("\r\n")
    r = parse_line(line)
    if r is None:
        if not kept_lines and body.startswith("#"):
            kept_lines.append(line)
        continue
    dom, exp, name, value, raw = r
    if not domain_match(dom):
        dropped += 1
        continue
    kept += 1
    kept_lines.append(line if line.endswith("\n") else line + "\n")
    if name and name not in seen:
        seen.add(name)
        pairs.append(f"{name}={value}")

if kept == 0:
    print(f"错误: 没有与域名 {want!r} 相关的 cookie（可先用 --list-domains 查看文件包含的域名）",
          file=sys.stderr)
    sys.exit(2)

if mode == "secret":
    # 只输出单行 Cookie，不改动原文件
    print("; ".join(pairs))
    print(f"# 命中 {kept} 条，剔除其他域名 {dropped} 条", file=sys.stderr)
    sys.exit(0)

# mode == filter：就地过滤，先备份再原子替换
backup = path + ".bak"
shutil.copy2(path, backup)

dir_name = os.path.dirname(os.path.abspath(path))
fd, tmp_path = tempfile.mkstemp(dir=dir_name, prefix=".cookies-", suffix=".tmp")
try:
    with os.fdopen(fd, "w", encoding="utf-8", newline="") as out:
        out.writelines(kept_lines)
    os.replace(tmp_path, path)
except BaseException:
    os.remove(tmp_path)
    raise

print(f"已过滤 {path}：保留 {kept} 条与 {want} 相关的条目，剔除 {dropped} 条无关条目")
print(f"原文件已备份到 {backup}")
PY
