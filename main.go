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
		if util.IsProcessed(row) {
			log.Printf("[跳过] %s 已处理过", code)
			continue
		}

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

		zh, err := util.Translate(info.OriginalTitle)
		if err != nil {
			// 翻译失败同样不回写整行，下次重试
			log.Printf("[失败] %s 翻译出错: %v（本次跳过，下次重试）", code, err)
			failed++
			util.SleepBetweenRequests()
			continue
		}

		// 回写内存中的该行
		row[util.ColOriginalTitle] = strings.TrimSpace(info.OriginalTitle)
		row[util.ColChineseTitle] = strings.TrimSpace(zh)
		// 主演写入备注列；只在备注为空时写，避免覆盖人工填写的内容
		if row[util.ColRemark] == "" && len(info.Actors) > 0 {
			row[util.ColRemark] = strings.Join(info.Actors, "、")
		}
		records[i] = row

		// 每处理完一条立即落盘，防止中途超时/崩溃丢掉结果
		if err := util.SaveCSV(csvPath, records); err != nil {
			log.Fatalf("[严重] %s 写回 CSV 失败: %v", code, err)
		}
		log.Printf("[完成] %s -> %s", code, zh)
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
