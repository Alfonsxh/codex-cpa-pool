package usagereport

import (
	"fmt"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/xuri/excelize/v2"
)

func identityCount(entries []Entry) int {
	count := 0
	for _, e := range entries {
		if e.ID != "" {
			count++
		}
	}
	return count
}

func (x *workbook) summary(r Report) {
	sheet := x.summarySheet
	x.setup(sheet, r, 12, i18n.Text(x.language, "usagereport.usage_through")+r.Period.GeneratedAt.Format(time.DateTime)+i18n.Text(x.language, "usagereport.includes_current_and_historical_identities_including_those_without_request_records"))
	for _, c := range []string{"B", "E", "H", "K"} {
		x.check(x.file.SetColWidth(sheet, c, c, 28))
	}
	for _, c := range []string{"C", "F", "I", "L"} {
		x.check(x.file.SetColWidth(sheet, c, c, 12))
	}
	for _, c := range []string{"D", "G", "J"} {
		x.check(x.file.SetColWidth(sheet, c, c, 5))
	}
	fill := "F2F6FC"
	note := x.cellStyle("note", fill, "left", false)
	kpi := x.cellStyle("kpi", fill, "left", true)
	m, p := r.Current, r.Previous
	for _, card := range []struct {
		col    int
		label  string
		value  any
		tokens bool
	}{
		{2, i18n.Text(x.language, "usagereport.raw_tokens"), m.TotalTokens, true}, {5, i18n.Text(x.language, "usagereport.weighted_tokens"), m.WeightedTokens, true},
		{8, i18n.Text(x.language, "usagereport.active_accounts"), m.Accounts, false}, {11, i18n.Text(x.language, "usagereport.active_users"), m.Users, false},
	} {
		x.check(x.file.SetCellStyle(sheet, cell(card.col, 7), cell(card.col+1, 10), note))
		x.put(sheet, cell(card.col, 7), card.label, note)
		style := kpi
		if card.tokens {
			if card.col == 2 {
				style = x.cellStyle("rawKPI", fill, "left", true)
			}
			style = x.tokenStyle(style, card.value)
			x.merged(sheet, cell(card.col, 8), cell(card.col+1, 8), card.value, style)
		} else {
			x.put(sheet, cell(card.col, 8), card.value, style)
		}
	}
	x.check(x.file.SetRowHeight(sheet, 7, 23))
	x.check(x.file.SetRowHeight(sheet, 8, 35))
	x.check(x.file.SetRowHeight(sheet, 9, 8))
	x.check(x.file.SetRowHeight(sheet, 10, 24))
	x.put(sheet, "I8", identityCount(r.Accounts), x.cellStyle("denominator", fill, "right", false))
	x.put(sheet, "L8", identityCount(r.Users), x.cellStyle("denominator", fill, "right", false))
	for _, pair := range []struct{ address, label string }{{"B10", i18n.Text(x.language, "usagereport.change_from_previous")}, {"E10", i18n.Text(x.language, "usagereport.average_weight")}, {"H10", i18n.Text(x.language, "usagereport.account_activity_rate")}, {"K10", i18n.Text(x.language, "usagereport.user_activity_rate")}} {
		x.put(sheet, pair.address, pair.label, note)
	}
	x.put(sheet, "C10", change(m.TotalTokens, p.TotalTokens), x.cellStyle("delta", fill, "right", false))
	x.put(sheet, "F10", ratio(m.WeightedTokens, m.TotalTokens), x.cellStyle("factor", fill, "right", false))
	x.put(sheet, "I10", ratio(int64(m.Accounts), int64(identityCount(r.Accounts))), x.cellStyle("percent", fill, "right", false))
	x.put(sheet, "L10", ratio(int64(m.Users), int64(identityCount(r.Users))), x.cellStyle("percent", fill, "right", false))
	x.changeColor(sheet, "C10")
	for row := 12; row <= 26; row++ {
		x.check(x.file.SetRowHeight(sheet, row, 18))
	}
	x.summaryCharts(r)
	x.check(x.file.SetCellStyle(sheet, "B28", "L28", x.style(&excelize.Style{Border: []excelize.Border{{Type: "bottom", Color: lineColor, Style: 1}}})))
	x.put(sheet, "B30", i18n.Text(x.language, "usagereport.weekly_observations"), x.cellStyle("section", "FFFFFF", "left", true))
	x.check(x.file.SetRowHeight(sheet, 30, 28))
	for _, pair := range []struct{ address, label string }{{"B32", i18n.Text(x.language, "usagereport.highest_usage")}, {"E32", i18n.Text(x.language, "usagereport.largest_increase")}, {"H32", i18n.Text(x.language, "usagereport.largest_decrease")}, {"K32", i18n.Text(x.language, "usagereport.request_statistics")}} {
		x.put(sheet, pair.address, pair.label, x.cellStyle("note", "FFFFFF", "left", false))
	}
	nameStyle := x.cellStyle("text", "FFFFFF", "left", true)
	pctStyle := x.cellStyle("percent", "FFFFFF", "right", false)
	deltaStyle := x.cellStyle("delta", "FFFFFF", "right", false)
	for _, address := range []string{"B33", "E33", "H33"} {
		x.put(sheet, address, i18n.Text(x.language, "usagereport.no_data"), nameStyle)
	}
	if len(r.Teams) > 0 && r.Teams[0].Current.TotalTokens > 0 {
		e := r.Teams[0]
		x.put(sheet, "B33", e.Name, nameStyle)
		x.put(sheet, "C33", ratio(e.Current.TotalTokens, m.TotalTokens), pctStyle)
	}
	var rising, falling *Entry
	var mostRise, mostFall float64
	for i := range r.Teams {
		e := &r.Teams[i]
		if e.Previous.TotalTokens <= 0 {
			continue
		}
		delta := float64(e.Current.TotalTokens)/float64(e.Previous.TotalTokens) - 1
		if delta > mostRise {
			mostRise = delta
			rising = e
		}
		if delta < mostFall {
			mostFall = delta
			falling = e
		}
	}
	if rising != nil {
		x.put(sheet, "E33", rising.Name, nameStyle)
		x.put(sheet, "F33", mostRise, deltaStyle)
	}
	if falling != nil {
		x.put(sheet, "H33", falling.Name, nameStyle)
		x.put(sheet, "I33", mostFall, deltaStyle)
	}
	height := 29.0
	for _, entry := range []*Entry{rising, falling} {
		if entry != nil {
			height = max(height, textRowHeight(entry.Name, 28))
		}
	}
	if len(r.Teams) > 0 {
		height = max(height, textRowHeight(r.Teams[0].Name, 28))
	}
	x.check(x.file.SetRowHeight(sheet, 33, height))
	x.changeColor(sheet, "F33")
	x.changeColor(sheet, "I33")
	x.put(sheet, "K33", i18n.Text(x.language, "usagereport.requests"), x.cellStyle("text", "FFFFFF", "left", false))
	x.put(sheet, "L33", m.RequestCount, x.cellStyle("number", "FFFFFF", "right", true))
	x.put(sheet, "K34", i18n.Text(x.language, "usagereport.success_rate"), x.cellStyle("note", "FFFFFF", "left", false))
	x.put(sheet, "L34", ratio(m.SuccessCount, m.RequestCount), pctStyle)
	x.put(sheet, "B37", i18n.Text(x.language, "usagereport.reporting_rules"), x.cellStyle("section", "FFFFFF", "left", true))
	for i, note := range []string{
		i18n.Text(x.language, "usagereport.share_default_ranking_per_user_usage_and_changes_use_raw"),
		i18n.Text(x.language, "usagereport.weighted_tokens_use_values_saved_when_requests_were_collected_historical"),
		i18n.Text(x.language, "usagereport.both_periods_use_current_team_membership_account_usage_is_attributed"),
		i18n.Text(x.language, "usagereport.active_means_at_least_one_request_in_the_reporting_period"),
		i18n.M("usagereport.previous_period_end_exclusive_change_is_when_previous_usage_is", i18n.Params{"Start": r.Period.PreviousStart.Format(time.DateTime), "End": r.Period.PreviousEnd.Format(time.DateTime)}).Render(x.language),
	} {
		x.merged(sheet, cell(2, 38+i), cell(12, 38+i), note, x.cellStyle("note", "FFFFFF", "left", false))
		x.check(x.file.SetRowHeight(sheet, 38+i, textRowHeight(note, 155)))
	}
}

func (x *workbook) changeColor(sheet, address string) {
	up, err := x.file.NewConditionalStyle(&excelize.Style{Font: &excelize.Font{Color: amberColor}})
	x.check(err)
	down, err := x.file.NewConditionalStyle(&excelize.Style{Font: &excelize.Font{Color: tealColor}})
	x.check(err)
	x.check(x.file.SetConditionalFormat(sheet, address, []excelize.ConditionalFormatOptions{
		{Type: "cell", Criteria: ">", Value: "0", Format: &up}, {Type: "cell", Criteria: "<", Value: "0", Format: &down},
	}))
}

func (x *workbook) chartFormat(peak int64) string {
	if x.withUnits {
		return compactTokenNumberFormat(float64(peak))
	}
	return "#,##0"
}

func (x *workbook) summaryCharts(r Report) {
	var peak int64
	for _, d := range r.Daily {
		peak = max(peak, d.Current.TotalTokens, d.Previous.TotalTokens)
	}
	axisFont := excelize.Font{Family: "Arial", Size: 10, Color: mutedColor}
	base := excelize.Chart{
		Dimension:    excelize.ChartDimension{Width: 595, Height: 350},
		Legend:       excelize.ChartLegend{Position: "top", Font: &axisFont},
		XAxis:        excelize.ChartAxis{Font: axisFont},
		YAxis:        excelize.ChartAxis{Font: axisFont, MajorGridLines: true, Minimum: ptr(0.0), NumFmt: excelize.ChartNumFmt{CustomNumFmt: x.chartFormat(peak)}},
		ShowBlanksAs: "gap",
	}
	line := base
	line.Type = excelize.Line
	line.Title = []excelize.RichTextRun{{Text: i18n.Text(x.language, "usagereport.daily_token_usage"), Font: &excelize.Font{Family: "Arial", Size: 14, Color: inkColor}}}
	for _, series := range []struct {
		column, color string
		dash          excelize.ChartDashType
	}{{"D", blueColor, excelize.ChartDashSolid}, {"G", "A6B4C9", excelize.ChartDashDash}} {
		fill := excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{series.color}}
		line.Series = append(line.Series, excelize.ChartSeries{
			Name: fmt.Sprintf("'%s'!$%s$7", x.dailySheet, series.column), Categories: fmt.Sprintf("'%s'!$C$8:$C$14", x.dailySheet), Values: fmt.Sprintf("'%s'!$%s$8:$%s$14", x.dailySheet, series.column, series.column),
			Fill: fill, Line: excelize.ChartLine{Type: excelize.ChartLineSolid, Dash: series.dash, Fill: fill, Width: 2},
		})
	}
	x.check(x.file.AddChart(x.summarySheet, "B12", &line))
	count := 0
	for _, e := range r.Teams {
		if count == 8 || e.Current.TotalTokens <= 0 {
			break
		}
		count++
	}
	if count == 0 {
		x.merged(x.summarySheet, "H17", "L20", i18n.Text(x.language, "usagereport.no_team_usage_in_this_period"), x.cellStyle("note", "FFFFFF", "center", false))
		return
	}
	bars := base
	bars.Type = excelize.Col
	bars.Legend.Position = "none"
	title := i18n.Text(x.language, "usagereport.team_token_usage")
	if len(r.Teams) > 8 {
		title += " Top8"
	}
	bars.Title = []excelize.RichTextRun{{Text: title, Font: &excelize.Font{Family: "Arial", Size: 14, Color: inkColor}}}
	bars.YAxis.NumFmt.CustomNumFmt = x.chartFormat(r.Teams[0].Current.TotalTokens)
	bars.Series = []excelize.ChartSeries{{Name: fmt.Sprintf("'%s'!$F$7", x.teamSheet), Categories: fmt.Sprintf("'%s'!$C$8:$C$%d", x.teamSheet, 7+count), Values: fmt.Sprintf("'%s'!$F$8:$F$%d", x.teamSheet, 7+count), Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{blueColor}}}}
	x.check(x.file.AddChart(x.summarySheet, "H12", &bars))
}
