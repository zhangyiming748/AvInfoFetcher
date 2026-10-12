package util

import (
	"os"
	"testing"
)

// TestAVFetchCookieEnv 用于在 GitHub Actions 中验证 secret AV_FETCH_COOKIE
// 是否成功注入为环境变量、并能被 cookie 解析逻辑识别。
//
// 安全注意：本测试只打印长度/条数/cookie 名称，绝不打印 cookie 值，
// 避免敏感信息出现在 Actions 日志中。
//
// 本地未设置该环境变量时自动 skip，不影响日常开发。
func TestAVFetchCookieEnv(t *testing.T) {
	raw := os.Getenv("AV_FETCH_COOKIE")
	if raw == "" {
		t.Skip("未设置环境变量 AV_FETCH_COOKIE，跳过（本地开发正常现象）")
	}

	t.Logf("已读到 AV_FETCH_COOKIE：总长度 %d 字符，共 %d 行", len(raw), lines(raw))

	// 用搜索站点域名跑一遍解析，确认格式能被识别
	const host = "www.javdb.com"
	pairs := parseCookieEnv(raw, host)
	t.Logf("按目标域名 %s 过滤后解析出 %d 条 cookie", host, len(pairs))
	for _, c := range pairs {
		t.Logf("  cookie 名称: %s（值长度 %d，已隐藏内容）", c.Name, len(c.Value))
	}

	if len(pairs) == 0 {
		t.Fatalf("环境变量存在但没有解析出属于 %s 的 cookie："+
			"请检查 cookie 文件是否包含目标域名，或先用 scripts/cookies2secret.sh --list-domains 查看", host)
	}
}

func lines(s string) int {
	n := 1
	for _, r := range s {
		if r == '\n' {
			n++
		}
	}
	return n
}
