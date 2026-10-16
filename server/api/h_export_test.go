package api

import (
	"bytes"
	"testing"

	"github.com/xuri/excelize/v2"
)

// TestExportWorkbookSheets 回归测试：导出工作簿的多 sheet 结构。
// 复现 exportFile 的完整流程（建业务 sheet → 写数据 → 删默认 Sheet1 → 写出），
// 内存读回验证所有 sheet 与数据行都存在——防止 excelize 用法回归（如列名/单元格名混淆）。
func TestExportWorkbookSheets(t *testing.T) {
	var buf bytes.Buffer
	f := excelize.NewFile()
	defer f.Close()

	build := func(f *excelize.File) error {
		if err := newSheet(f, "用户列表",
			[]string{"ID", "用户名"}, []float64{6, 14}); err != nil {
			return err
		}
		if err := addRows(f, "用户列表", [][]any{{1, "admin"}}); err != nil {
			return err
		}
		if err := newSheet(f, "权限明细",
			[]string{"用户名", "集群"}, []float64{14, 12}); err != nil {
			return err
		}
		return addRows(f, "权限明细", [][]any{{"admin", "全部集群"}})
	}
	if err := build(f); err != nil {
		t.Fatalf("build: %v", err)
	}
	f.DeleteSheet("Sheet1")
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}

	r, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	sheets := r.GetSheetList()
	if len(sheets) != 2 {
		t.Fatalf("期望 2 个 sheet，实际 %d 个: %v", len(sheets), sheets)
	}
	for _, want := range []string{"用户列表", "权限明细"} {
		rows, err := r.GetRows(want)
		if err != nil {
			t.Fatalf("读 %s: %v", want, err)
		}
		if len(rows) != 2 { // 表头 + 1 行数据
			t.Fatalf("sheet %q 期望 2 行（表头+数据），实际 %d 行: %v", want, len(rows), rows)
		}
	}
}

func TestColLetter(t *testing.T) {
	cases := map[int]string{1: "A", 2: "B", 26: "Z", 27: "AA", 52: "AZ", 53: "BA"}
	for n, want := range cases {
		if got := colLetter(n); got != want {
			t.Errorf("colLetter(%d) = %q, 期望 %q", n, got, want)
		}
	}
}
