package usagereport

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/xuri/excelize/v2"
)

func TestResolvePeriodCalendarBoundariesAndDST(t *testing.T) {
	for _, tc := range []struct {
		zone, now, selected, start, end, previousEnd string
		hours                                        int
	}{
		{"Asia/Shanghai", "2026-09-09T09:20:00+08:00", "", "2026-08-31T00:00:00+08:00", "2026-09-07T00:00:00+08:00", "2026-08-31T00:00:00+08:00", 168},
		{"Asia/Shanghai", "2026-09-09T09:20:00+08:00", "2026-09-07", "2026-09-07T00:00:00+08:00", "2026-09-09T09:20:00+08:00", "2026-09-02T09:20:00+08:00", 0},
		{"America/New_York", "2026-03-09T01:00:00-04:00", "", "2026-03-02T00:00:00-05:00", "2026-03-09T00:00:00-04:00", "2026-03-02T00:00:00-05:00", 167},
		{"America/New_York", "2026-11-02T00:00:00-05:00", "", "2026-10-26T00:00:00-04:00", "2026-11-02T00:00:00-05:00", "2026-10-26T00:00:00-04:00", 169},
		{"America/New_York", "2026-03-08T12:00:00-04:00", "2026-03-02", "2026-03-02T00:00:00-05:00", "2026-03-08T12:00:00-04:00", "2026-03-01T12:00:00-05:00", 0},
		{"UTC", "2026-01-01T00:00:00Z", "2025-12-29", "2025-12-29T00:00:00Z", "2026-01-01T00:00:00Z", "2025-12-25T00:00:00Z", 0},
	} {
		t.Run(tc.zone+tc.now+tc.selected, func(t *testing.T) {
			zone, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			now, _ := time.Parse(time.RFC3339, tc.now)
			p, err := ResolvePeriod(tc.selected, now, zone)
			if err != nil {
				t.Fatal(err)
			}
			if p.Start.Format(time.RFC3339) != tc.start || p.End.Format(time.RFC3339) != tc.end || p.PreviousEnd.Format(time.RFC3339) != tc.previousEnd {
				t.Fatalf("period = %+v", p)
			}
			if tc.hours > 0 && p.End.Sub(p.Start) != time.Duration(tc.hours)*time.Hour {
				t.Fatalf("duration = %s", p.End.Sub(p.Start))
			}
			windows := p.Windows()
			for i, window := range windows {
				if window.EndAt <= window.StartAt || (i > 0 && window.StartAt < windows[i-1].EndAt) {
					t.Fatalf("invalid windows: %+v", windows)
				}
			}
		})
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	for _, value := range []string{"2026-09-08", "2026-09-14", "invalid", "2026-9-7", "1969-12-29", "2026-02-30"} {
		if _, err := ResolvePeriod(value, now, time.UTC); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func reportFixture(t *testing.T) Report {
	t.Helper()
	p, err := ResolvePeriod("2026-08-31", time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	metrics := func(requests, raw, weighted int64) usage.WeightedMetrics {
		return usage.WeightedMetrics{RawMetrics: usage.RawMetrics{RequestCount: requests, SuccessCount: requests, TotalTokens: raw}, WeightedTokens: weighted}
	}
	r, err := Build(p, Catalog{
		Accounts:     map[string]string{"alpha": "alpha@example.com", "beta": "beta@example.com", "idle": "idle@example.com"},
		Teams:        map[string]string{"team-a": "研发", "team-idle": "运营"},
		UserTeams:    map[string]string{"alice@example.com": "team-a", "bob@example.com": "team-a", "idle@example.com": ""},
		UserAccounts: map[string]string{"alice@example.com": "beta", "bob@example.com": "alpha", "idle@example.com": "idle"},
	}, []usage.ReportUsageRow{
		{Window: 0, Account: "alpha", User: "alice@example.com", Usage: metrics(1, 50, 100)},
		{Window: 1, Account: "gamma", User: "gone@example.com", Usage: metrics(1, 10, 10)},
		{Window: 7, Account: "alpha", User: " ALICE@EXAMPLE.COM ", Usage: metrics(2, 100, 200)},
		{Window: 7, Account: "beta", User: "alice@example.com", Usage: metrics(1, 25, 50)},
		{Window: 8, Account: "alpha", User: "bob@example.com", Usage: metrics(1, 30, 30)},
		{Window: 9, Account: "old-account", User: "deleted@example.com", Usage: metrics(1, 5, 5)},
		{Window: 10, Account: "", User: "", Usage: metrics(1, 7, 0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReportReconcilesAllIdentitiesAndDeduplicatesActivity(t *testing.T) {
	r := reportFixture(t)
	if r.Current.TotalTokens != 167 || r.Current.WeightedTokens != 285 || r.Current.Users != 3 || r.Current.Accounts != 3 || r.Current.Teams != 1 || r.Current.RequestCount != 6 {
		t.Fatalf("current=%+v", r.Current)
	}
	for _, entries := range [][]Entry{r.Accounts, r.Teams, r.Users} {
		var raw, weighted, prior int64
		for i, e := range entries {
			raw += e.Current.TotalTokens
			weighted += e.Current.WeightedTokens
			prior += e.Previous.WeightedTokens
			if i > 0 && e.Current.TotalTokens > entries[i-1].Current.TotalTokens {
				t.Fatal("not sorted by raw usage")
			}
		}
		if raw != r.Current.TotalTokens || weighted != r.Current.WeightedTokens || prior != r.Previous.WeightedTokens {
			t.Fatalf("non-reconciling dimension: %+v", entries)
		}
	}
	if r.Users[0].ID != "alice@example.com" || r.Users[0].Current.Days != 1 || r.Users[0].Current.Accounts != 2 || r.Teams[0].Current.Users != 2 || r.Teams[0].Previous.WeightedTokens != 100 {
		t.Fatalf("dedup/current team attribution: %+v", r)
	}
	var daily int64
	for _, day := range r.Daily {
		daily += day.Current.WeightedTokens
	}
	if daily != r.Current.WeightedTokens {
		t.Fatal("daily totals diverge")
	}
}

func TestXLSXNumbersTotalsChartsAndUntrustedLabels(t *testing.T) {
	r := reportFixture(t)
	r.Users[0].Name = `=HYPERLINK("https://example.invalid","unsafe")`
	r.Teams[0].Name = "+1+1"
	data, err := XLSX(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if strings.Join(f.GetSheetList(), ",") != "Usage overview,Team statistics,Account details,User usage details,Daily trends" {
		t.Fatalf("sheets=%v", f.GetSheetList())
	}
	for _, tc := range []struct{ sheet, cell, want string }{
		{"Usage overview", "E8", "285"}, {"Usage overview", "B8", "167"},
		{"Team statistics", fmt.Sprintf("G%d", len(r.Teams)+8), "285"},
		{"Account details", fmt.Sprintf("L%d", len(r.Accounts)+8), "285"},
		{"User usage details", fmt.Sprintf("K%d", len(r.Users)+8), "285"},
		{"Daily trends", "E15", "285"}, {"User usage details", "B8", r.Users[0].Name}, {"Team statistics", "C8", "+1+1"},
	} {
		got, err := f.GetCellValue(tc.sheet, tc.cell, excelize.Options{RawCellValue: true})
		if err != nil || got != tc.want {
			t.Fatalf("%s!%s = %q err=%v, want %q", tc.sheet, tc.cell, got, err, tc.want)
		}
		formula, err := f.GetCellFormula(tc.sheet, tc.cell)
		if err != nil || formula != "" {
			t.Fatalf("unexpected formula: %s %v", formula, err)
		}
	}
	if kind, _ := f.GetCellType("User usage details", "K8"); kind != excelize.CellTypeNumber && kind != excelize.CellTypeUnset {
		t.Fatalf("numeric token cell type=%v", kind)
	}
	panes, err := f.GetPanes("User usage details")
	if err != nil || !panes.Freeze || panes.XSplit != 3 || panes.YSplit != 7 {
		t.Fatalf("panes=%+v err=%v", panes, err)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	charts := 0
	for _, file := range z.File {
		if strings.HasPrefix(file.Name, "xl/charts/chart") && strings.HasSuffix(file.Name, ".xml") {
			charts++
		}
		if strings.Contains(file.Name, "externalLinks") || strings.Contains(file.Name, "vbaProject") {
			t.Fatalf("unsafe workbook component %s", file.Name)
		}
	}
	if charts != 2 {
		t.Fatalf("chart count=%d", charts)
	}
}

func TestEmptyPartialWeekHasBlankFutureDaysAndUndefinedComparison(t *testing.T) {
	p, err := ResolvePeriod("2026-09-07", time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Build(p, Catalog{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := XLSX(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, tc := range []struct{ cell, want string }{{"E10", "0"}, {"E11", ""}, {"H11", ""}, {"I10", "—"}} {
		got, err := f.GetCellValue("Daily trends", tc.cell)
		if err != nil || got != tc.want {
			t.Fatalf("%s=%q want %q err=%v", tc.cell, got, tc.want, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := XLSX(ctx, r); err == nil {
		t.Fatal("canceled export succeeded")
	}
}
