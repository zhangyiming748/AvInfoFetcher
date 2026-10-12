package util

import (
	"encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// CSV 各列下标，对应表头：番号,原文标题,中文标题,主要演员,备注
const (
	ColCode          = 0 // 番号
	ColOriginalTitle = 1 // 原文标题
	ColChineseTitle  = 2 // 中文标题
	ColActors        = 3 // 主要演员
	ColRemark        = 4 // 备注
)

// MinCols 一行至少应有的列数（含备注列）
const MinCols = 5

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

// IsProcessed 判断一行是否已经处理过：
// 以“原文标题”列是否有内容为准（而不是“是否不止番号一列”，
// 因为人工预填“备注”等列不应被误判为已处理）。
func IsProcessed(row []string) bool {
	return len(row) > ColOriginalTitle && row[ColOriginalTitle] != ""
}
