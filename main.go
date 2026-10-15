package main

import (
	"errors"
	"log"
	"strings"

	"AvInfoFetcher/util"
)

// csvPath CSV 文件固定为 workspace 下的 vid.csv
const csvPath = "vid.csv"

func main() {
	records, err := util.LoadCSV(csvPath)
	if err != nil {
		log.Fatalf("读取 CSV 失败: %v", err)
	}
	if len(records) == 0 {
		log.Fatal("CSV 为空（连表头都没有）")
	}

	// 先统一规范历史数据里的标题：中文标点转英文标点，
	// 文件系统不允许的字符转空格（有的行是之前版本写入的，可能带全角标点）
	normalized := false
	for i := 1; i < len(records); i++ {
		for _, col := range []int{util.ColOriginalTitle, util.ColChineseTitle} {
			if fixed := util.SanitizeTitle(records[i][col]); fixed != records[i][col] {
				records[i][col] = fixed
				normalized = true
			}
		}
	}
	if normalized {
		if err := util.SaveCSV(csvPath, records); err != nil {
			log.Fatalf("[严重] 规范历史标题后写回 CSV 失败: %v", err)
		}
		log.Println("[规范] 已把标题列里的中文标点替换为英文标点")
	}

	// 记录初始状态，跑完后判断文件是否有变化
	initialCSV := serialize(records)

	processed, failed := 0, 0
	// records[0] 是表头，直接跳过
	for i := 1; i < len(records); i++ {
		row := records[i]
		code := strings.TrimSpace(row[util.ColCode])
		if code == "" {
			continue // 空行忽略
		}

		switch util.StateOf(row) {
		case util.RowDone:
			log.Printf("[跳过] %s 已处理过", code)
			continue
		case util.RowNeedTranslate:
			// 已有原文标题、缺中文标题：上次抓取成功但翻译失败，
			// 不用重新抓取，直接拿第二列翻译
			log.Printf("[翻译] %s 已有原文标题，仅补翻译", code)
			zh, err := util.Translate(strings.TrimSpace(row[util.ColOriginalTitle]))
			if err != nil {
				log.Printf("[失败] %s 翻译出错: %v（本次跳过，下次重试）", code, err)
				failed++
				continue
			}
			row[util.ColChineseTitle] = util.SanitizeTitle(zh)
			records[i] = row
			if err := util.SaveCSV(csvPath, records); err != nil {
				log.Fatalf("[严重] %s 写回 CSV 失败: %v", code, err)
			}
			log.Printf("[完成] %s -> %s", code, row[util.ColChineseTitle])
			processed++
			continue
		}

		// RowNew：全新行，走完整抓取流程
		log.Printf("[查询] %s ...", code)
		info, err := util.FetchInfo(code)
		if err != nil {
			if errors.Is(err, util.ErrNotFound) {
				// 番号在库里不存在（或只有相似推荐）：跳过本条，
				// 不算失败、不回写，下次运行还会再试（结果一般仍是未找到）
				log.Printf("[未找到] %s 没有准确匹配的结果，跳过本条", code)
				util.SleepBetweenRequests()
				continue
			}
			// 查询失败：不回写，保持空白，下次运行自动重试
			log.Printf("[失败] %s 抓取信息出错: %v（本次跳过，下次重试）", code, err)
			failed++
			util.SleepBetweenRequests()
			continue
		}

		// 抓取成功先回写原文标题：即使后面翻译失败，下次也能走"仅补翻译"分支
		row[util.ColOriginalTitle] = util.SanitizeTitle(info.OriginalTitle)
		// 备注列写封面缩略图 URL；只在备注为空时写，避免覆盖人工填写的内容
		if row[util.ColRemark] == "" && info.Cover != "" {
			row[util.ColRemark] = info.Cover
		}
		records[i] = row
		if err := util.SaveCSV(csvPath, records); err != nil {
			log.Fatalf("[严重] %s 写回 CSV 失败: %v", code, err)
		}

		zh, err := util.Translate(info.OriginalTitle)
		if err != nil {
			// 翻译失败：原文标题已落盘，下次运行走 RowNeedTranslate 分支补翻译
			log.Printf("[失败] %s 翻译出错: %v（原文标题已保存，下次仅补翻译）", code, err)
			failed++
			util.SleepBetweenRequests()
			continue
		}

		row[util.ColChineseTitle] = util.SanitizeTitle(zh)
		records[i] = row

		// 每处理完一条立即落盘，防止中途超时/崩溃丢掉结果
		if err := util.SaveCSV(csvPath, records); err != nil {
			log.Fatalf("[严重] %s 写回 CSV 失败: %v", code, err)
		}
		log.Printf("[完成] %s -> %s", code, row[util.ColChineseTitle])
		processed++
		util.SleepBetweenRequests()
	}

	log.Printf("全部结束：成功 %d 条，失败/待重试 %d 条", processed, failed)

	// 没有任何变化时提示无需提交（workflow 里据此跳过空提交）
	if serialize(records) == initialCSV {
		log.Println("CSV 无变化，无需提交")
	}
}

func serialize(records [][]string) string {
	var b strings.Builder
	for _, row := range records {
		b.WriteString(strings.Join(row, "\x1f"))
		b.WriteByte('\n')
	}
	return b.String()
}
