package util


import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// Info 爬虫根据番号查到的信息
type Info struct {
	OriginalTitle string // 原文标题
	Actors        string // 主要演员（多人时自行决定分隔符，如逗号）
}

// FetchInfo 根据番号抓取原文标题和主要演员。
//
// 注意：这里是占位实现，具体的抓取逻辑（搜索 URL 拼接、beautiful soup 解析等）
// 由你自己填写。如果你的爬虫是一个外部命令，可以像下面这样调用：
//
//	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
//	defer cancel()
//	out, err := exec.CommandContext(ctx, "你的爬虫命令", code).Output()
//	if err != nil { return nil, err }
//	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
//	return &Info{OriginalTitle: lines[0], Actors: lines[1]}, nil
func FetchInfo(code string) (*Info, error) {
	// TODO: 替换成你自己的爬虫实现
	return nil, errors.New("FetchInfo 尚未实现：请在 util/getInfo.go 中填入你的爬虫逻辑")
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
