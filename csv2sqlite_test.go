package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"AvInfoFetcher/util"

	_ "github.com/glebarez/go-sqlite" // 纯 Go 的 SQLite 驱动（不依赖 CGO），注册名 "sqlite"
)

// 本文件是一个独立的一次性导入工具：把仓库根目录的 vid.csv 导入 SQLite 文件。
// 人工审核过 CSV 后，手动执行一次即可生成 / 更新数据库：
//
//	go test -count=1 -run TestExportCSVToSQLite .
//
// 必须带 -count=1：go test 默认会缓存成功的结果，输入没变时再次运行可能
// 直接回放缓存而不会真正写库（例如手动删掉 vid.db 想重建时就会踩到）。
//
// 番号是主键，重复记录交给 INSERT OR IGNORE 自动跳过，所以：
//   - 第一次运行 = 全量建库；
//   - 之后每次运行只导入新增的番号，重复运行不会产生重复行。
const (
	importCSVPath = "vid.csv" // 待导入的 CSV（相对仓库根目录）
	importDBPath  = "vid.db"  // 生成的 SQLite 文件（相对仓库根目录）
)

const createTableSQL = `
CREATE TABLE IF NOT EXISTS videos (
	code           TEXT PRIMARY KEY,          -- 番号，主键，天然去重
	original_title TEXT NOT NULL DEFAULT '',  -- 原文标题
	chinese_title  TEXT NOT NULL DEFAULT '',  -- 中文标题
	remark         TEXT NOT NULL DEFAULT ''   -- 备注（封面图 URL）
);`

const insertSQL = `
INSERT OR IGNORE INTO videos (code, original_title, chinese_title, remark)
VALUES (?, ?, ?, ?);`

// TestExportCSVToSQLite 把 vid.csv 导入 vid.db，并打印本次新增/跳过的条数。
func TestExportCSVToSQLite(t *testing.T) {
	absCSV, _ := filepath.Abs(importCSVPath)

	// 复用主程序自身的 CSV 读取逻辑，保证解析行为与主程序一致
	// （自动去掉 BOM、短行补齐到 4 列）
	records, err := util.LoadCSV(importCSVPath)
	if err != nil {
		t.Skipf("跳过：读取 %s 失败（%v）", absCSV, err)
	}
	if len(records) <= 1 {
		t.Skipf("跳过：%s 里没有数据行", absCSV)
	}

	db, err := sql.Open("sqlite", importDBPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(createTableSQL); err != nil {
		t.Fatalf("建表失败: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("准备写入语句失败: %v", err)
	}

	rows, inserted := 0, 0
	for _, row := range records[1:] { // records[0] 是表头
		code := strings.TrimSpace(row[util.ColCode])
		if code == "" {
			continue // 跳过空行
		}
		res, err := stmt.Exec(
			code,
			strings.TrimSpace(row[util.ColOriginalTitle]),
			strings.TrimSpace(row[util.ColChineseTitle]),
			strings.TrimSpace(row[util.ColRemark]),
		)
		if err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			t.Fatalf("写入 %s 失败: %v", code, err)
		}
		rows++
		if n, err := res.RowsAffected(); err == nil {
			inserted += int(n) // 被 IGNORE 跳过的行返回 0
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatalf("关闭语句失败: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}

	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM videos").Scan(&total); err != nil {
		t.Fatalf("统计总行数失败: %v", err)
	}

	absDB, _ := filepath.Abs(importDBPath)
	t.Logf("CSV 读取          : %d 条（%s）", rows, absCSV)
	t.Logf("本次新增导入      : %d 条", inserted)
	t.Logf("跳过（已存在/重复）: %d 条", rows-inserted)
	t.Logf("数据库            : %s（当前共 %d 条）", absDB, total)

	if total == 0 {
		t.Fatal("数据库里没有任何记录")
	}
}
