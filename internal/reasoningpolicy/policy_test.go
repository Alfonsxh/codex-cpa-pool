package reasoningpolicy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOnlyHigherExplicitEffortsAreLowered(t *testing.T) {
	for _, effort := range append(Levels(), "auto", "unknown", "future-effort", "") {
		for _, field := range []string{"responses", "chat"} {
			t.Run(field+"/"+effort, func(t *testing.T) {
				body := `{ "reasoning": { "effort": "` + effort + `", "context": "all_turns" }, "input": "ultra high max" }`
				if field == "chat" {
					body = `{ "reasoning_effort": "` + effort + `", "messages": [{"role":"user","content":"ultra"}] }`
				}
				got, err := Rewrite([]byte(body), "xhigh")
				if err != nil {
					t.Fatal(err)
				}
				if effort != "max" && effort != "ultra" {
					if string(got) != body {
						t.Fatalf("lower/unranked input changed: %s", got)
					}
					return
				}
				var data map[string]any
				if err := json.Unmarshal(got, &data); err != nil {
					t.Fatal(err)
				}
				value := data["reasoning_effort"]
				if field == "responses" {
					value = data["reasoning"].(map[string]any)["effort"]
				}
				if value != "xhigh" {
					t.Fatalf("mapped effort = %v", value)
				}
			})
		}
	}
	for _, body := range []string{`{ "model":"model", "input":[] }`, `{"reasoning":null}`, `{"reasoning":{"effort":null}}`, `{"reasoning":{"effort":123}}`} {
		got, err := Rewrite([]byte(body), "xhigh")
		if err != nil || string(got) != body {
			t.Fatalf("omitted/non-string effort changed: %s / %v", got, err)
		}
	}
}

func TestRewritePreservesOpaquePayloadsAndExactNumbers(t *testing.T) {
	body := `{"model":"model(ultra)","reasoning":{"effort":"max","context":"all_turns","encrypted":"keep\\nmax"},"large":9007199254740993,"tools":[{"parameters":{"reasoning":{"effort":"ultra"}}}],"input":[{"type":"image","url":"data:image/png;base64,ultra"}]}`
	got, err := Rewrite([]byte(body), "xhigh")
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal([]byte(body), &before)
	_ = json.Unmarshal(got, &after)
	for _, key := range []string{"large", "tools", "input"} {
		if string(before[key]) != string(after[key]) {
			t.Fatalf("%s changed: %s", key, after[key])
		}
	}
	if string(after["model"]) != `"model(xhigh)"` || !strings.Contains(string(after["reasoning"]), `"effort":"xhigh"`) {
		t.Fatalf("selectors not capped: %s", got)
	}
	if !strings.Contains(string(after["reasoning"]), `"encrypted":"keep\\nmax"`) {
		t.Fatalf("opaque data changed: %s", got)
	}
}

func TestEveryConfiguredLimitAndInvalidEnvelopes(t *testing.T) {
	for i, limit := range Levels() {
		for j, effort := range Levels() {
			body := `{"reasoning_effort":"` + effort + `"}`
			got, err := Rewrite([]byte(body), limit)
			want := effort
			if j > i {
				want = limit
			}
			if err != nil || string(got) != `{"reasoning_effort":"`+want+`"}` {
				t.Fatalf("%s capped at %s = %s / %v", effort, limit, got, err)
			}
		}
	}
	for _, body := range []string{`null`, `[]`, `{"reasoning":{"effort":"ultra","effort":"low"}}`, `{"model":"m(ultra)","model":"m(low)"}`, `{"reasoning":`, `{} {}`} {
		if _, err := Rewrite([]byte(body), "xhigh"); err == nil {
			t.Fatalf("accepted ambiguous/invalid body %s", body)
		}
		got, err := Rewrite([]byte(body), "")
		if err != nil || string(got) != body {
			t.Fatal("disabled policy changed compatibility")
		}
	}
	if _, err := MessageType([]byte(`{"type":"response.create","type":"response.cancel"}`)); err == nil {
		t.Fatal("duplicate event type accepted")
	}
	for _, value := range []any{"auto", "invalid", "", nil, 4} {
		if _, err := FromSettings(map[string]any{SettingKey: value}); err == nil {
			t.Fatalf("invalid limit accepted: %v", value)
		}
	}
	for _, settings := range []map[string]any{nil, {SettingKey: "unlimited"}} {
		if got, err := FromSettings(settings); err != nil || got != "" {
			t.Fatalf("default = %q / %v", got, err)
		}
	}
}
