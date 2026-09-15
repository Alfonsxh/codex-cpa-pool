package usagereport

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/xuri/excelize/v2"
)

func TestXLSXUnitsPreserveNumericValuesAcrossWorksheets(t *testing.T) {
	period := reportFixture(t).Period
	for _, tc := range []struct {
		amount int64
		plain  string
		units  string
		format string
	}{
		{400, "400", "400 Token", `#,##0" Token"`},
		{12_500, "12,500", "12.50 K", `0.00," K"`},
		{1_250_000, "1,250,000", "1.25 M", `0.00,," M"`},
		{1_250_000_000, "1,250,000,000", "1.25 B", `0.00,,," B"`},
	} {
		for _, withUnits := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/units=%t", tc.amount, withUnits), func(t *testing.T) {
				r, err := Build(period, Catalog{
					Accounts:  map[string]string{"alpha": "account@example.com"},
					Teams:     map[string]string{"team": "Platform"},
					UserTeams: map[string]string{"alice@example.com": "team"},
				}, []usage.ReportUsageRow{{Window: 7, Account: "alpha", User: "alice@example.com", Usage: usage.WeightedMetrics{
					RawMetrics: usage.RawMetrics{RequestCount: 2, SuccessCount: 2, TotalTokens: tc.amount}, WeightedTokens: tc.amount,
				}}})
				if err != nil {
					t.Fatal(err)
				}
				data, err := XLSX(context.Background(), r, XLSXOptions{WithUnits: withUnits})
				if err != nil {
					t.Fatal(err)
				}
				file, err := excelize.OpenReader(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				want := tc.plain
				if withUnits {
					want = tc.units
				}
				for _, cell := range []struct{ sheet, address string }{
					{"Usage overview", "B8"}, {"Usage overview", "E8"},
					{"Team statistics", "F8"}, {"Team statistics", "G8"}, {"Team statistics", "K8"}, {"Team statistics", "G9"},
					{"Account details", "K8"}, {"Account details", "L8"}, {"Account details", "L9"},
					{"User usage details", "J8"}, {"User usage details", "K8"}, {"User usage details", "K9"},
					{"Daily trends", "D8"}, {"Daily trends", "E8"}, {"Daily trends", "E15"},
				} {
					if withUnits {
						styleID, err := file.GetCellStyle(cell.sheet, cell.address)
						if err != nil {
							t.Fatal(err)
						}
						style, err := file.GetStyle(styleID)
						if err != nil || style.CustomNumFmt == nil || *style.CustomNumFmt != tc.format {
							t.Fatalf("%s!%s lost its numeric unit format: %+v %v", cell.sheet, cell.address, style, err)
						}
					}
					// Excelize's reader does not implement comma scaling. The
					// native K/M/B formats are checked after serialization above;
					// actual rendering is covered by spreadsheet-engine acceptance.
					if !withUnits || tc.amount < 1_000 {
						value, err := file.GetCellValue(cell.sheet, cell.address)
						if err != nil || value != want {
							t.Fatalf("%s!%s display=%q want=%q err=%v", cell.sheet, cell.address, value, want, err)
						}
					}
					raw, err := file.GetCellValue(cell.sheet, cell.address, excelize.Options{RawCellValue: true})
					if err != nil || raw != strconv.FormatInt(tc.amount, 10) {
						t.Fatalf("%s!%s raw value changed: %q %v", cell.sheet, cell.address, raw, err)
					}
				}
				if value, err := file.GetCellValue("Usage overview", "L33"); err != nil || value != "2" {
					t.Fatalf("request count gained Token units: %q %v", value, err)
				}
				if err := file.SetCellFormula("Usage overview", "B45", "SUM(B8,E8)"); err != nil {
					t.Fatal(err)
				}
				if value, err := file.CalcCellValue("Usage overview", "B45"); err != nil || value != strconv.FormatInt(2*tc.amount, 10) {
					t.Fatalf("numeric sum=%q err=%v", value, err)
				}
			})
		}
	}
}
