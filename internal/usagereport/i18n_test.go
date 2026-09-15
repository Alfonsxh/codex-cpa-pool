package usagereport

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/xuri/excelize/v2"
)

func TestBilingualExportsKeepNumericCellsAndValidChartSheetReferences(t *testing.T) {
	report := reportFixture(t)
	report.Users[0].Name = "用户自填名称"
	for _, lang := range []i18n.Language{i18n.English, i18n.Chinese} {
		data, err := XLSX(context.Background(), report, XLSXOptions{Language: lang})
		if err != nil {
			t.Fatal(err)
		}
		f, err := excelize.OpenReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		summary, daily, team, user := "Usage overview", "Daily trends", "Team statistics", "User usage details"
		if lang == i18n.Chinese {
			summary, daily, team, user = "用量总览", "每日趋势", "团队统计", "用户使用明细"
		}
		if value, err := f.GetCellValue(summary, "B8", excelize.Options{RawCellValue: true}); err != nil || value != "167" {
			t.Fatalf("numeric total %s: %s %v", lang, value, err)
		}
		if value, _ := f.GetCellValue(user, "B8"); value != "用户自填名称" {
			t.Fatal("user label changed")
		}
		f.Close()
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		charts := ""
		for _, entry := range archive.File {
			if strings.HasPrefix(entry.Name, "xl/charts/chart") && strings.HasSuffix(entry.Name, ".xml") {
				reader, _ := entry.Open()
				content, _ := io.ReadAll(reader)
				reader.Close()
				charts += string(content)
			}
		}
		if !strings.Contains(charts, daily) || !strings.Contains(charts, team) || strings.Contains(charts, "#REF!") {
			t.Fatalf("invalid chart references for %s", lang)
		}
		if lang == i18n.English && strings.ContainsAny(charts, "每日趋势团队统计") {
			t.Fatal("English chart uses old sheet references")
		}
	}
}
