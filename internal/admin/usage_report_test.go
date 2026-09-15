package admin

import (
	"bytes"
	"context"
	"errors"
	"mime"
	"net/http"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usagereport"
	"github.com/xuri/excelize/v2"
)

type fakeWeeklyUsageReader struct {
	fakeUsageReader
	windows []usage.ReportWindow
	err     error
}

func (reader *fakeWeeklyUsageReader) ReportUsage(_ context.Context, windows []usage.ReportWindow) ([]usage.ReportUsageRow, error) {
	reader.windows = windows
	return []usage.ReportUsageRow{{Window: 7, Account: "alpha", User: "alice@example.com", Usage: usage.WeightedMetrics{RawMetrics: usage.RawMetrics{RequestCount: 1, SuccessCount: 1, TotalTokens: 100}, WeightedTokens: 400}}}, reader.err
}

func TestAdminWeeklyReportAuthorizationDefaultsAndDownload(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	reader := &fakeWeeklyUsageReader{}
	server, err := New(Config{Store: store, Usage: reader, Now: func() time.Time { return time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	path := "/admin/api/overview/usage-report.xlsx"
	response := performAdminRequest(server, http.MethodGet, path, nil, nil, nil)
	if response.Code != http.StatusUnauthorized || len(reader.windows) != 0 {
		t.Fatalf("unauthenticated report: %d", response.Code)
	}
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	response = performAdminRequest(server, http.MethodGet, path, nil, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("download: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != usagereport.ContentType {
		t.Fatalf("headers=%v", response.Header())
	}
	_, params, err := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
	if err != nil || params["filename"] != "CCPA_Token_Report_2026-08-31_2026-09-06.xlsx" {
		t.Fatalf("disposition=%v %v", params, err)
	}
	zone, _ := time.LoadLocation("Asia/Shanghai")
	if len(reader.windows) != 14 || reader.windows[7].StartAt != time.Date(2026, 8, 31, 0, 0, 0, 0, zone).Unix() {
		t.Fatalf("windows=%+v", reader.windows)
	}
	f, err := excelize.OpenReader(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if value, _ := f.GetCellValue("Usage overview", "E8"); value != "400" {
		t.Fatalf("total=%q", value)
	}
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			for _, value := range row {
				if value == "test_external_alice" || value == "test-management-key" {
					t.Fatal("credential leaked")
				}
			}
		}
	}
	response = performAdminRequest(server, http.MethodGet, path+"?week_start=2026-09-08", nil, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_report_week")
	reader.windows = nil
	response = performAdminRequest(server, http.MethodGet, path+"?with_units=invalid", nil, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_report_format")
	if len(reader.windows) != 0 {
		t.Fatal("invalid units option queried usage")
	}
	response = performAdminRequest(server, http.MethodGet, path+"?with_units=true", nil, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("download with units: %d %s", response.Code, response.Body.String())
	}
	withUnits, err := excelize.OpenReader(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer withUnits.Close()
	if value, err := withUnits.GetCellValue("Usage overview", "E8"); err != nil || value != "400 Token" {
		t.Fatalf("units option was not applied: %q %v", value, err)
	}
	server.usageReportMu.Lock()
	response = performAdminRequest(server, http.MethodGet, path, nil, headers, nil)
	server.usageReportMu.Unlock()
	assertAdminError(t, response, http.StatusTooManyRequests, "report_busy")
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{usage.ErrReportTooLarge, http.StatusUnprocessableEntity, "report_too_large"},
		{context.DeadlineExceeded, http.StatusGatewayTimeout, "report_timeout"},
		{errors.New("database unavailable"), http.StatusInternalServerError, "internal_error"},
	} {
		reader.err = tc.err
		response = performAdminRequest(server, http.MethodGet, path, nil, headers, nil)
		assertAdminError(t, response, tc.status, tc.code)
		if response.Header().Get("Content-Disposition") != "" {
			t.Fatal("error advertised a workbook")
		}
	}
}
