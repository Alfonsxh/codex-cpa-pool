package quota

import (
	"reflect"
	"sync"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
)

func TestQuotaPresentationDoesNotMutateSharedSnapshot(t *testing.T) {
	window := WeeklyWindow{Key: "default:primary_window", Label: "常规周限额", UsedPercent: 42}
	snapshot := Snapshot{Accounts: []AccountQuota{{Account: "alpha", Weekly: &window, WeeklyWindows: []WeeklyWindow{window, {Key: "additional:model:primary_window", Label: "用户命名", UsedPercent: 12}}}}}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lang, want := i18n.English, "Weekly limit"
			if i%2 != 0 {
				lang, want = i18n.Chinese, "常规周限额"
			}
			presented := snapshot.Localized(lang).(Snapshot)
			account := presented.Accounts[0]
			if account.Weekly.Label != want || account.WeeklyWindows[0].Label != want || account.WeeklyWindows[1].Label != "用户命名" || account.Weekly.UsedPercent != 42 {
				t.Errorf("bad presentation: %+v", account)
			}
		}(i)
	}
	wg.Wait()
	if !reflect.DeepEqual(snapshot.Accounts[0].WeeklyWindows[0], window) || window.Label != "常规周限额" {
		t.Fatal("cached snapshot changed")
	}
}

func TestAuthoredWindowLabelsFollowTheResponseLanguage(t *testing.T) {
	payload := decodeObject(t, `{
      "rate_limit":{"allowed":true,"limit_reached":false,
        "primary_window":{"limit_window_seconds":604800,"used_percent":10}},
      "additional_rate_limits":[
        {"rate_limit":{"limit_reached":false,
          "primary_window":{"limit_window_seconds":604800,"used_percent":20}}},
        {"limit_name":"Codex Models","metered_feature":"codex_models",
          "rate_limit":{"limit_reached":false,
          "primary_window":{"limit_window_seconds":604800,"used_percent":30}}}
      ]
    }`)
	snapshot := Snapshot{Accounts: []AccountQuota{Normalize("alpha", payload)}}
	if len(snapshot.Accounts[0].WeeklyWindows) != 3 {
		t.Fatalf("windows = %#v", snapshot.Accounts[0].WeeklyWindows)
	}
	for _, testCase := range []struct {
		lang                      i18n.Language
		weekly, unnamed, upstream string
	}{
		{i18n.English, "Weekly limit", "Additional weekly limit 1", "Codex Models"},
		{i18n.Chinese, "常规周限额", "附加周限额 1", "Codex Models"},
	} {
		account := snapshot.Localized(testCase.lang).(Snapshot).Accounts[0]
		if account.Weekly.Label != testCase.weekly || account.WeeklyWindows[0].Label != testCase.weekly ||
			account.WeeklyWindows[1].Label != testCase.unnamed || account.WeeklyWindows[2].Label != testCase.upstream {
			t.Errorf("%s labels = %#v", testCase.lang, account.WeeklyWindows)
		}
	}
	if snapshot.Accounts[0].WeeklyWindows[1].Label != "Additional weekly limit 1" {
		t.Fatal("cached snapshot changed")
	}

	inspection := ResetInspection{Windows: []ResetWindow{
		{Key: "default:primary_window", Label: "Upstream default"},
		{Key: "additional:additional-2:primary_window", Label: "Additional weekly limit 2"},
		{Key: "additional:codex_models:primary_window", Label: "Codex Models"},
	}}
	presented := inspection.Localized(i18n.Chinese).(ResetInspection)
	if presented.Windows[0].Label != "常规周限额" || presented.Windows[1].Label != "附加周限额 2" ||
		presented.Windows[2].Label != "Codex Models" {
		t.Fatalf("reset labels = %#v", presented.Windows)
	}
	if inspection.Windows[1].Label != "Additional weekly limit 2" {
		t.Fatal("reset inspection changed")
	}
}
