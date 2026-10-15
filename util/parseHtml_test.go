package util

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/anaskhan96/soup"
)

// readTestHTML 读取测试用的本地 HTML 文件。
// 把从浏览器“查看源代码”复制的内容存到 util/ 下的某个 .html 文件，
// 然后把文件名传给本函数即可。文件不存在时跳过测试，避免 CI 失败。
func readTestHTML(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Skipf("跳过：未找到 %s（%v）。请把浏览器源码保存到该路径后再运行。", name, err)
	}
	return string(data)
}

// TestParseSearchPage 解析搜索页，验证能否从 div.movie-list 的第一个 div.item
// 中取到真实番号并与输入番号比对，再取出详情页链接。
//
// 搜索结果可能有多条，但通常只有第一条是准确匹配，因此这里固定取第一个 item。
// 同时站点在“无准确结果”时会返回相似推荐，所以必须把第一条 video-title > strong
// 里的番号与输入番号比对一致（忽略大小写、下划线、空格），才认为命中。
//
// 对应 HTML 结构（util/search.html）：
//
//	<div class="movie-list ...">
//	  <div class="item">
//	    <a href="/v/mQNQv" class="box" title="...">
//	      ...
//	      <div class="video-title"><strong>BDM-001</strong> ...</div>
//	    </a>
//	  </div>
//	  ...
//	</div>
//
// 把 javdb 搜索结果页源码保存到 util/search.html 后运行：
//
//	go test ./util -run TestParseSearchPage -v
func TestParseSearchPage(t *testing.T) {
	html := readTestHTML(t, "search.html")
	doc := soup.HTMLParse(html)

	list := doc.Find("div", "class", "movie-list")
	if list.Error != nil {
		t.Fatalf("未找到 div.movie-list: %v", list.Error)
	}

	// 搜索结果可能有多条，只有第一条是准确匹配，固定取第一个 item
	item := list.Find("div", "class", "item")
	if item.Error != nil {
		t.Fatalf("未找到 div.item: %v", item.Error)
	}

	// 先取 video-title > strong 里的真实番号，确认第一条不是相似推荐
	titleDiv := item.Find("div", "class", "video-title")
	if titleDiv.Error != nil {
		t.Fatalf("未找到 div.video-title: %v", titleDiv.Error)
	}
	codeNode := titleDiv.Find("strong")
	if codeNode.Error != nil {
		t.Fatalf("未找到 video-title 内的 strong: %v", codeNode.Error)
	}
	realCode := strings.TrimSpace(codeNode.FullText())
	t.Logf("第一条结果番号: %s", realCode)

	// 用带下划线/空格/小写的输入形态验证归一化比对：应视为同一个番号
	queryCode := " bdm_001 "
	if !codeMatches(queryCode, realCode) {
		t.Fatalf("番号比对不一致：输入 %q，第一条为 %q", queryCode, realCode)
	}

	// 完全不同的番号必须比对失败，避免把相似推荐当成命中
	if codeMatches("BDM-002", realCode) {
		t.Fatalf("不应匹配：BDM-002 与 %q 是不同番号", realCode)
	}

	// 第一个 <a> 指向详情页（href 形如 /v/xxxx）
	link := item.Find("a")
	if link.Error != nil {
		t.Fatalf("未找到 a 标签: %v", link.Error)
	}
	href := link.Attrs()["href"]
	t.Logf("详情页链接: %s", href)
	if href == "" {
		t.Fatal("href 为空")
	}
	if !strings.HasPrefix(href, "/v/") {
		t.Fatalf("href 不是详情页路径，实际值: %q", href)
	}
}

// TestCodeMatches 验证番号归一化比对：忽略大小写和所有分隔符
// （连字符 "-"、下划线 "_"、空格等），但字母与数字本身必须完全对上。
func TestCodeMatches(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"完全相同", "BDM-001", "BDM-001", true},
		{"忽略大小写", "bdm-001", "BDM-001", true},
		{"下划线对连字符", "BDM_001", "BDM-001", true},
		{"忽略空格", " BDM 001 ", "bdm-001", true},
		{"无分隔符写法", "BDM001", "BDM-001", true},
		{"下划线空格大小写混合", " bdm_001 ", "BDM-001", true},
		{"编号不同", "BDM-002", "BDM-001", false},
		{"系列字母不同", "ABC-001", "BDM-001", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codeMatches(c.a, c.b); got != c.want {
				t.Fatalf("codeMatches(%q, %q) = %v, want %v（归一化: %q vs %q）",
					c.a, c.b, got, c.want, normalizeCode(c.a), normalizeCode(c.b))
			}
		})
	}
}

// TestSanitizeTitle 验证标题规范化：中文标点转英文标点，
// 文件系统不允许的字符（\ / : * ? " < > |）转空格，并合并多余空白。
func TestSanitizeTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"全角与号", "巨乳美女＆子宮調教", "巨乳美女&子宮調教"},
		{"全角波浪线", "アスリートマニア17 ～競泳編～", "アスリートマニア17 ~競泳編~"},
		{"句号", "掌控脏话。控制射精", "掌控脏话.控制射精"},
		{"书名号方括号", "【FANZA限定】系列記念！", "[FANZA限定]系列記念!"},
		{"问号转空格去尾", "特別編？", "特別編"},
		{"冒号转空格", "A：B", "A B"},
		{"省略号", "テスト……終", "テスト...終"},
		{"多余空白合并", "  A   B  ", "A B"},
		{"空串", "   ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SanitizeTitle(c.in); got != c.want {
				t.Fatalf("SanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestParseSearchResultNotFound 验证当第一条结果与输入番号不一致（相似推荐）时，
// parseSearchResult 返回 ErrNotFound，调用方据此跳过本条而不是当作失败。
func TestParseSearchResultNotFound(t *testing.T) {
	html := readTestHTML(t, "search.html")

	// search.html 的第一条是 BDM-001，用一个对不上的番号去匹配
	_, err := parseSearchResult(html, "NOTEXIST-999")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际: %v", err)
	}

	// 归一化后能对上的番号（大小写/分隔符不同）应正常返回详情页路径
	path, err := parseSearchResult(html, " bdm_001 ")
	if err != nil {
		t.Fatalf("期望匹配成功，实际报错: %v", err)
	}
	t.Logf("匹配成功，详情页路径: %s", path)
}

// TestParseDetailPage 用真实详情页（util/detail.html）验证 parseDetailPage
// 能取到原文标题和封面缩略图 URL。
//
// 对应结构：
//
//	<h2 class="title is-4">
//	  <strong>BDM-001 </strong>                                  ← 番号
//	  <strong class="current-title">巨乳美女ドM喉ま○こ… </strong> ← 标题
//	</h2>
//	<div class="column column-video-cover">
//	  <a data-fancybox="gallery" href="https://c0.jdbstatic.com/covers/mq/mQNQv.jpg">
//	    <img class="video-cover" src="https://c0.jdbstatic.com/covers/mq/mQNQv.jpg"> ← 封面
//	  </a>
//	</div>
//
//	go test ./util -run TestParseDetailPage -v
func TestParseDetailPage(t *testing.T) {
	html := readTestHTML(t, "detail.html")

	title, cover, err := parseDetailPage(html)
	if err != nil {
		t.Fatalf("parseDetailPage 失败: %v", err)
	}
	t.Logf("标题: %q", title)
	t.Logf("封面: %q", cover)
	if title == "" {
		t.Fatal("标题为空")
	}
	if cover == "" {
		t.Fatal("封面缩略图为空")
	}
	if !strings.HasPrefix(cover, "https://") {
		t.Fatalf("封面不是完整 URL: %q", cover)
	}
}
