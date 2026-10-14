package util

import (
	"encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// utf8BOM Excel 打开中文 CSV 时需要的 UTF-8 BOM，读写时都保留
const utf8BOM = "\xef\xbb\xbf"

// CSV 各列下标，对应表头：番号,原文标题,中文标题,备注
const (
	ColCode          = 0 // 番号
	ColOriginalTitle = 1 // 原文标题
	ColChineseTitle  = 2 // 中文标题
	ColRemark        = 3 // 备注
)

// MinCols 一行至少应有的列数
const MinCols = 4

// LoadCSV 读取整个 CSV 文件，返回全部行（含表头）。
// 短行会自动补齐到 MinCols 列，方便后续按下标写入。
func LoadCSV(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // 允许每行列数不一致
	r.LazyQuotes = true

	var records [][]string
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		records = append(records, padRow(row, MinCols))
	}
	if len(records) > 0 {
		// 去掉可能存在的 UTF-8 BOM，避免把 BOM 当成“番号”表头的一部分
		records[0][0] = strings.TrimPrefix(records[0][0], utf8BOM)
	}
	return records, nil
}

// SaveCSV 原子写入 CSV：先写同目录临时文件，再 rename 覆盖，
// 保证程序中途崩溃不会写坏原文件。
func SaveCSV(path string, records [][]string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".vid-*.csv.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// 任何异常路径都尝试清理临时文件
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	w := csv.NewWriter(tmp)
	// 先写 UTF-8 BOM，保证 Excel 打开中文不乱码
	if _, err := tmp.WriteString(utf8BOM); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := w.WriteAll(records); err != nil {
		_ = tmp.Close()
		return err
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func padRow(row []string, n int) []string {
	if len(row) >= n {
		return row
	}
	padded := make([]string, n)
	copy(padded, row)
	return padded
}

// RowState 一行的处理状态
type RowState int

const (
	// RowNew 全新行：只有番号，其余列为空，需要完整抓取
	RowNew RowState = iota
	// RowNeedTranslate 有番号+原文标题但没有中文标题：
	// 上次抓取成功但翻译失败，直接拿原文标题翻译即可，不用重新抓取
	RowNeedTranslate
	// RowDone 原文标题和中文标题都有，视为已完成
	RowDone
)

// StateOf 判断一行的处理状态。
// 以“原文标题”“中文标题”两列是否有内容为准（人工预填“备注”列不影响判断）。
func StateOf(row []string) RowState {
	hasOriginal := len(row) > ColOriginalTitle && strings.TrimSpace(row[ColOriginalTitle]) != ""
	hasChinese := len(row) > ColChineseTitle && strings.TrimSpace(row[ColChineseTitle]) != ""
	switch {
	case hasOriginal && hasChinese:
		return RowDone
	case hasOriginal:
		return RowNeedTranslate
	default:
		return RowNew
	}
}
