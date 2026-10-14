package util

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/anaskhan96/soup"
)

// fetchLogger 同时输出到控制台与工作目录下的 fetch.log（追加写），
// 用于记录每次 HTTP 请求的结果（成功/失败/疑似拦截），事后可排查。
var fetchLogger *log.Logger

func init() {
	f, err := os.OpenFile("fetch.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// 文件打不开时不 fatal，退化为只写控制台
		fetchLogger = log.New(os.Stdout, "[FETCH] ", log.LstdFlags)
		fetchLogger.Printf("警告：无法打开 fetch.log（%v），日志仅输出到控制台", err)
		return
	}
	fetchLogger = log.New(io.MultiWriter(os.Stdout, f), "[FETCH] ", log.LstdFlags)
}

// Info 爬虫根据番号查到的信息
type Info struct {
	OriginalTitle string   // 原文标题
	Actors        []string // 主要演员
}

// Cookie 从环境变量 AV_FETCH_COOKIE 读取，支持两种格式（自动识别）：
//  1. Netscape cookies.txt 格式（浏览器插件 / curl cookie jar 批量导出），
//     每行 7 个 Tab 分隔字段：domain  includeSubdomains  path  secure
//     expires(unix时间戳)  name  value
//     ——整个导出文件可原样粘贴进 GitHub secret（支持多行）；
//     程序会按目标域名过滤、丢弃已过期的 cookie。
//  2. 请求头格式：key1=v1; key2=v2（从浏览器开发者工具复制的那种）。
//
// 解析结果存到包级变量 requestCookies，由 httpGet 在每次请求时附加。
type cookiePair struct {
	Name  string
	Value string
}

// requestCookies 当前生效的 Cookie（configureCookies 设置，httpGet 使用）
var requestCookies []cookiePair

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

// configureCookies 读取环境变量，解析出属于 host 的 cookie 存入包级变量。
// 返回实际加载的 Cookie 数量，便于日志排查。
func configureCookies(host string) int {
	raw := strings.TrimSpace(os.Getenv("AV_FETCH_COOKIE"))
	if raw == "" {
		requestCookies = nil
		return 0
	}
	requestCookies = parseCookieEnv(raw, host)
	return len(requestCookies)
}

// ErrCloudflareBlocked 表示请求被 Cloudflare 等防护页拦截（返回 200 但内容是拦截页）。
var ErrCloudflareBlocked = errors.New("请求被 Cloudflare 拦截")

// httpClient 统一的 HTTP 客户端。必须带超时：soup.Get 无超时，
// 网络挂起时会一直卡住，直到 workflow 的 timeout-minutes 兜底。
var httpClient = &http.Client{Timeout: 30 * time.Second}

const (
	userAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	acceptLanguage = "zh-CN,zh;q=0.9,ja;q=0.8"
)

// httpGet 发 GET 请求并记录日志（URL、状态码、字节数、底层错误）。
// 返回内容；被拦截返回 ErrCloudflareBlocked；非 200 返回带状态码的错误。
func httpGet(target string) (string, error) {
	fetchLogger.Printf("GET %s", target)

	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", acceptLanguage)
	for _, c := range requestCookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		// 底层错误（TLS 失败/连接重置/超时等）完整记录，soup 会把这层吞掉
		fetchLogger.Printf("FAIL %s -> %v", target, err)
		return "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fetchLogger.Printf("FAIL %s -> 读取响应体失败: %v", target, err)
		return "", fmt.Errorf("读取响应体失败: %w", err)
	}
	html := string(body)
	fetchLogger.Printf("RESP %s status=%d len=%d", target, resp.StatusCode, len(html))

	// Cloudflare 拦截：403/503 状态码，或 200 但内容是质询页
	// （正常详情页含 Cloudflare analytics，但不会是 Just a moment 标题）
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
		fetchLogger.Printf("BLOCKED %s status=%d", target, resp.StatusCode)
		return "", fmt.Errorf("%w（HTTP %d）", ErrCloudflareBlocked, resp.StatusCode)
	}
	lower := strings.ToLower(html)
	if strings.Contains(lower, "just a moment") ||
		strings.Contains(lower, "attention required") ||
		strings.Contains(lower, "cloudflare") && strings.Contains(lower, "verify you are human") {
		fetchLogger.Printf("BLOCKED %s (Cloudflare/Just a moment), len=%d", target, len(html))
		return "", ErrCloudflareBlocked
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP 状态码异常: %d", resp.StatusCode)
	}
	return html, nil
}

// 搜索页地址与站点根地址
const (
	siteBase  = "https://javdb.com"
	searchURL = siteBase + "/search?q=%s&f=all"
)

// normalizeCode 归一化番号用于比对：转小写并只保留字母和数字，
// 即连字符 "-"、下划线 "_"、空白等分隔符一律忽略，
// 但字母与数字本身必须完全一致。
//
// 之所以连字符也忽略：站点番号形如 "BDM-001"，而输入可能写成
// "bdm_001" 或 "BDM 001"，若只删下划线/空格会得到 "bdm001"，
// 仍与 "bdm-001" 对不上，因此统一按字母+数字比较。
//
// 例：
//
//	"BDM-001"、"bdm-001"、"bdm_001"、" BDM 001 "、"BDM001"
//	都会归一成 "bdm001"；而 "BDM-002" 归一为 "bdm002"，不相等。
func normalizeCode(s string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1 // 删除所有分隔符（-、_、空格等）
	}, s))
}

// codeMatches 判断两个番号在归一化后是否完全一致。
func codeMatches(a, b string) bool {
	return normalizeCode(a) == normalizeCode(b)
}

// 请求之间的随机停顿范围，防止请求频率过快被站点封禁
const (
	requestIntervalMin = 1 * time.Second
	requestIntervalMax = 3 * time.Second
)

// SleepBetweenRequests 随机停顿 1~3 秒，用于每条番号处理之间、
// 以及搜索页与详情页请求之间，降低因频率过快被封禁的风险。
// Go 1.20+ 的全局 rand 自动随机种子，无需手动 Seed。
func SleepBetweenRequests() {
	d := requestIntervalMin +
		time.Duration(rand.Int63n(int64(requestIntervalMax-requestIntervalMin)))
	time.Sleep(d)
}

// ErrNotFound 表示没有与番号准确匹配的结果（站点只返回了相似推荐或没有结果）。
// 调用方应用 errors.Is 识别它并当作“本条不存在”跳过，而不是视为抓取失败。
var ErrNotFound = errors.New("没有准确匹配的结果")

// saveArtifactHTML 把请求到的 HTML 保存到 artifacts/ 目录，供事后排查或作为 workflow artifact 上传。
// 文件名格式：artifacts/{code}_{pageType}.html（pageType 为 search / detail）
func saveArtifactHTML(code, pageType, html string) {
	dir := "artifacts"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fetchLogger.Printf("警告：创建 artifacts 目录失败: %v", err)
		return
	}
	// 番号可能含路径分隔符等不安全字符，统一替换为下划线
	safeCode := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, code)
	path := filepath.Join(dir, safeCode+"_"+pageType+".html")
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		fetchLogger.Printf("警告：保存 %s 失败: %v", path, err)
		return
	}
	fetchLogger.Printf("已保存 %s (%d 字节)", path, len(html))
}

// parseSearchResult 解析搜索结果页 HTML，返回详情页路径（形如 /v/xxxx）。
//
// 只采信第一条结果（站点通常第一条才是准确匹配），并把第一条
// div.video-title > strong 里的番号与输入番号做归一化比对，一致才返回，
// 防止站点在“无准确结果”时返回相似推荐。
func parseSearchResult(html, code string) (string, error) {
	doc := soup.HTMLParse(html)

	// TODO: 以下选择器以 javdb 搜索页结构为准，如站点改版需核对。
	list := doc.Find("div", "class", "movie-list")
	if list.Error != nil {
		return "", fmt.Errorf("%w: %s 的搜索结果为空", ErrNotFound, code)
	}
	item := list.Find("div", "class", "item")
	if item.Error != nil {
		return "", fmt.Errorf("%w: %s 的搜索结果为空", ErrNotFound, code)
	}

	// div.video-title > strong 里是该结果的真实番号，用来确认不是相似推荐
	realCode := ""
	if titleDiv := item.Find("div", "class", "video-title"); titleDiv.Error == nil {
		if strong := titleDiv.Find("strong"); strong.Error == nil {
			realCode = strings.TrimSpace(strong.FullText())
		}
	}
	if realCode == "" || !codeMatches(code, realCode) {
		return "", fmt.Errorf("%w: 番号 %s 的第一条结果为 %q，可能只是相似推荐", ErrNotFound, code, realCode)
	}

	// 第一个 <a> 指向详情页（href 形如 /v/xxxx）
	link := item.Find("a")
	if link.Error != nil {
		return "", fmt.Errorf("未找到 %s 的详情页链接: %v", code, link.Error)
	}
	detailPath := link.Attrs()["href"]
	if detailPath == "" {
		return "", fmt.Errorf("未找到 %s 的详情页链接", code)
	}
	return detailPath, nil
}

// parseDetailPage 解析详情页 HTML，返回原文标题与主要演员。
func parseDetailPage(html string) (title string, actors []string, err error) {
	doc := soup.HTMLParse(html)

	// 标题：h2.title 里的 strong.current-title（旧版页面为 span.origin-name，
	// 两者都没有时退化为标题节点的全部文本）
	titleNode := doc.Find("h2", "class", "title")
	if titleNode.Error != nil {
		return "", nil, fmt.Errorf("未找到原文标题: %v", titleNode.Error)
	}
	if cur := titleNode.Find("strong", "class", "current-title"); cur.Error == nil {
		title = cur.FullText()
	} else if origin := titleNode.Find("span", "class", "origin-name"); origin.Error == nil {
		title = origin.FullText()
	}
	if strings.TrimSpace(title) == "" {
		title = titleNode.FullText()
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", nil, errors.New("原文标题为空")
	}

	// 演员：movie-panel-info 里按 <strong> 的字段名定位 panel-block
	panel := doc.Find("nav", "class", "movie-panel-info")
	if panel.Error != nil {
		return "", nil, fmt.Errorf("未找到信息面板: %v", panel.Error)
	}
	// panelField 按字段名（如“演员:”）找对应 panel-block 的 span.value 节点
	panelField := func(label string) (soup.Root, error) {
		for _, block := range panel.FindAll("div", "class", "panel-block") {
			s := block.Find("strong")
			if s.Error == nil && s.FullText() == label {
				if v := block.Find("span", "class", "value"); v.Error == nil {
					return v, nil
				}
			}
		}
		return soup.Root{}, fmt.Errorf("未找到字段 %q", label)
	}

	// 演员字段的 span.value 内每个 <a> 是一个演员。
	// 有的影片详情页没有演员信息，缺失时返回空列表即可，不视为失败。
	if actorValue, err := panelField("演员:"); err == nil {
		for _, a := range actorValue.FindAll("a") {
			if name := strings.TrimSpace(a.FullText()); name != "" {
				actors = append(actors, name)
			}
		}
	}

	return title, actors, nil
}

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

	// 加载 Cookie（请求头在 httpGet 里统一设置）
	if n := configureCookies(host); n > 0 {
		fetchLogger.Printf("已加载 %d 个 Cookie", n)
	}

	html, err := httpGet(target)
	if err != nil {
		return nil, err
	}
	saveArtifactHTML(code, "search", html)

	detailPath, err := parseSearchResult(html, code)
	if err != nil {
		return nil, err
	}
	// href 可能是相对路径，统一拼成绝对地址
	if strings.HasPrefix(detailPath, "/") {
		detailPath = siteBase + detailPath
	}

	// 搜索页与详情页请求之间随机停 1~3 秒，防止频率过快被封禁
	SleepBetweenRequests()

	// 详情页（cookie / header 已在上面设置过，全局生效）
	detailHTML, err := httpGet(detailPath)
	if err != nil {
		return nil, fmt.Errorf("请求详情页失败: %w", err)
	}
	saveArtifactHTML(code, "detail", detailHTML)

	title, actors, err := parseDetailPage(detailHTML)
	if err != nil {
		return nil, err
	}

	return &Info{OriginalTitle: title, Actors: actors}, nil
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
