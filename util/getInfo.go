package util

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/anaskhan96/soup"
)

// Info 爬虫根据番号查到的信息
type Info struct {
	OriginalTitle string // 原文标题
}

// ---- soup 发 GET 请求时携带 Cookie 的两种方式 ----
//
// 方式一（推荐，单个设置）：在 soup.Get 之前调用
//
//	soup.Cookie("key", "value")
//
// 可以多次调用设置多个 cookie；也可以设置全局请求头：
//
//	soup.Header("User-Agent", "Mozilla/5.0 ...")
//
// 方式二（批量设置）：直接给全局 map 赋值
//
//	soup.Cookies = map[string]string{"key1": "v1", "key2": "v2"}
//
// 注意：soup.Cookie / soup.Cookies 是包级全局状态，会影响之后所有 soup.Get，
// 因此在函数开头统一设置即可。
//
// Cookie 从环境变量 AV_FETCH_COOKIE 读取，支持两种格式（自动识别）：
//  1. Netscape cookies.txt 格式（浏览器插件 / curl cookie jar 批量导出），
//     每行 7 个 Tab 分隔字段：domain  includeSubdomains  path  secure
//     expires(unix时间戳)  name  value
//     ——整个导出文件可原样粘贴进 GitHub secret（支持多行）；
//     程序会按目标域名过滤、丢弃已过期的 cookie。
//  2. 请求头格式：key1=v1; key2=v2（从浏览器开发者工具复制的那种）。
type cookiePair struct {
	Name  string
	Value string
}

// parseCookieEnv 解析 AV_FETCH_COOKIE 的内容，只返回属于 host 域名的未过期 cookie。
func parseCookieEnv(raw, host string) []cookiePair {
	// 任意一行是“7 个 Tab 字段”即判定为 Netscape cookies.txt 格式
	netscape := false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(strings.Split(line, "\t")) >= 7 {
			netscape = true
			break
		}
	}

	if !netscape {
		// 请求头格式：key1=v1; key2=v2
		var pairs []cookiePair
		for _, part := range strings.Split(raw, ";") {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) == 2 && kv[0] != "" {
				pairs = append(pairs, cookiePair{kv[0], kv[1]})
			}
		}
		return pairs
	}

	now := time.Now().Unix()
	var pairs []cookiePair
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			// curl 导出的 HttpOnly cookie 行会带 #HttpOnly_ 前缀，需要保留
			if !strings.HasPrefix(line, "#HttpOnly_") {
				continue
			}
		}
		f := strings.Split(line, "\t")
		if len(f) < 7 {
			continue
		}
		domain := strings.TrimPrefix(strings.TrimPrefix(f[0], "#HttpOnly_"), ".")
		// 只保留目标域名（含其子域）的 cookie
		if host != domain && !strings.HasSuffix(host, "."+domain) {
			continue
		}
		// f[4] 是过期时间戳，0 表示会话级 cookie；已过期的丢弃
		if exp, err := strconv.ParseInt(f[4], 10, 64); err == nil && exp != 0 && exp < now {
			continue
		}
		if f[5] != "" {
			pairs = append(pairs, cookiePair{f[5], f[6]})
		}
	}
	return pairs
}

// configureCookies 读取环境变量并把 cookie 设置到 soup 的全局配置中。
func configureCookies(host string) {
	raw := strings.TrimSpace(os.Getenv("AV_FETCH_COOKIE"))
	if raw == "" {
		return
	}
	for _, c := range parseCookieEnv(raw, host) {
		soup.Cookie(c.Name, c.Value)
	}
}

// 搜索页地址与站点根地址
const (
	siteBase  = "https://javdb.com"
	searchURL = siteBase + "/search?q=%s&f=all"
)

// FetchInfo 根据番号抓取原文标题。
func FetchInfo(code string) (*Info, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("番号为空")
	}

	// q 是 query 参数，必须用 QueryEscape 而不是 PathEscape
	// （空格应编码成 + 而非 %20，& 等字符也要转义，避免破坏查询串）
	target := fmt.Sprintf(searchURL, url.QueryEscape(code))
	targetURL, _ := url.Parse(target)
	host := targetURL.Hostname()

	// 统一设置请求头与 cookie（soup 包级全局配置；cookie 按目标域名过滤）
	soup.Header("User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	soup.Header("Accept-Language", "zh-CN,zh;q=0.9,ja;q=0.8")
	configureCookies(host)

	html, err := soup.Get(target)
	if err != nil {
		return nil, fmt.Errorf("请求搜索页失败: %w", err)
	}
	doc := soup.HTMLParse(html)

	// TODO: 以下选择器以 javdb 搜索页结构为准，如站点改版需核对。
	// 搜索结果列表 div.movie-list 内，每个 div.item 的第一个 a 指向详情页（href 形如 /v/xxxx）
	link := doc.Find("div", "class", "movie-list").Find("div", "class", "item").Find("a")
	if link.Error != nil {
		return nil, fmt.Errorf("未找到 %s 的搜索结果: %v", code, link.Error)
	}
	detailPath := link.Attrs()["href"]
	if detailPath == "" {
		return nil, fmt.Errorf("未找到 %s 的详情页链接", code)
	}
	// href 可能是相对路径，统一拼成绝对地址
	if strings.HasPrefix(detailPath, "/") {
		detailPath = siteBase + detailPath
	}

	// 详情页（cookie / header 已在上面设置过，全局生效）
	detailHTML, err := soup.Get(detailPath)
	if err != nil {
		return nil, fmt.Errorf("请求详情页失败: %w", err)
	}
	detail := soup.HTMLParse(detailHTML)

	// 原文标题：详情页 <h2 class="title">
	// 其中 <strong class="current-title"> 是番号本身，真正的标题通常在
	// <span class="origin-name">（原文名）或标题节点的剩余文本里。
	titleNode := detail.Find("h2", "class", "title")
	if titleNode.Error != nil {
		return nil, fmt.Errorf("未找到原文标题: %v", titleNode.Error)
	}
	title := ""
	if origin := titleNode.Find("span", "class", "origin-name"); origin.Error == nil {
		title = origin.FullText()
	}
	if strings.TrimSpace(title) == "" {
		title = titleNode.FullText()
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("原文标题为空")
	}

	return &Info{OriginalTitle: title}, nil
}

// translateMaxRetry 翻译失败时的最大尝试次数（含首次）
const translateMaxRetry = 3

// Translate 调用本机 / CI 容器中安装的 translate-shell（trans 命令），
// 把原文标题翻译成中文。失败自动重试。
func Translate(text string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= translateMaxRetry; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		// -b / --brief 只输出译文；:zh 表示目标语言为中文
		cmd := exec.CommandContext(ctx, "trans", "-b", ":zh", text)
		out, err := cmd.Output()
		cancel()
		if err == nil {
			zh := strings.TrimSpace(string(out))
			if zh != "" {
				return zh, nil
			}
			lastErr = errors.New("translate-shell 返回空结果")
		} else {
			lastErr = err
		}
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}
	return "", lastErr
}
