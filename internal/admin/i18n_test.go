package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"unicode"
)

func TestConfigurationLocalizationPreservesValuesAndUsesStableIDs(t *testing.T) {
	server, store := newTestAdmin(t)
	if err := store.UpdateSettings(context.Background(), map[string]any{"branding.product_name": "产品设计"}); err != nil {
		t.Fatal(err)
	}
	var catalogs []configurationCatalogResponse
	for _, lang := range []string{"en", "zh-CN", "en"} {
		headers := map[string]string{"X-Management-Key": "test-management-key", "Accept-Language": lang}
		response := performAdminRequest(server, http.MethodGet, "/admin/api/settings/configuration", nil, headers, nil)
		if response.Code != 200 || response.Header().Get("Content-Language") != lang {
			t.Fatalf("%d %s", response.Code, response.Body.String())
		}
		var catalog configurationCatalogResponse
		decodeAdminResponse(t, response, &catalog)
		for _, group := range catalog.Groups {
			if group.ID == "" || strings.ContainsFunc(group.ID, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
				t.Fatal(group.ID)
			}
			for _, field := range group.Fields {
				if strings.Contains(field.Label, "operation could not") || strings.Contains(field.Label, "未能完成") || strings.Contains(field.Label, "admin.") {
					t.Fatalf("missing field label: %s=%s", field.Key, field.Label)
				}
				if lang == "en" && strings.ContainsFunc(field.Label+field.Description, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
					t.Fatalf("Chinese display text: %s", field.Key)
				}
				if field.Key == "branding.product_name" && field.Value != "产品设计" {
					t.Fatal("user content changed")
				}
			}
		}
		catalogs = append(catalogs, catalog)
	}
	if !reflect.DeepEqual(catalogs[0].Groups, catalogs[2].Groups) {
		t.Fatal("Chinese request mutated English metadata")
	}
	for i, enGroup := range catalogs[0].Groups {
		zhGroup := catalogs[1].Groups[i]
		if enGroup.ID != zhGroup.ID || enGroup.Name == zhGroup.Name {
			t.Fatalf("group identity/presentation: %v %v", enGroup.ID, zhGroup.ID)
		}
		for j, en := range enGroup.Fields {
			zh := zhGroup.Fields[j]
			if en.Key != zh.Key || !reflect.DeepEqual(en.Value, zh.Value) || !reflect.DeepEqual(en.Default, zh.Default) || en.ApplyMode != zh.ApplyMode || en.UnitCode != zh.UnitCode {
				t.Fatalf("business facts changed: %s", en.Key)
			}
			for k, choice := range en.Choices {
				if choice.Value != zh.Choices[k].Value {
					t.Fatal("choice identity changed")
				}
			}
		}
	}
}

func TestHTTPDefaultAndLocalizedValidationMetadata(t *testing.T) {
	server, _ := newTestAdmin(t)
	for _, lang := range []string{"", "zh-CN"} {
		headers := map[string]string{"X-Management-Key": "test-management-key", "Accept-Language": lang}
		response := performAdminRequest(server, http.MethodPost, "/admin/api/settings/configuration", map[string]any{"confirm": "save", "values": map[string]any{"cpa.proxy_enabled": "bad"}}, headers, nil)
		var body struct {
			Error struct {
				Code, Message string
				MessageKey    string         `json:"message_key"`
				Params        map[string]any `json:"message_params"`
			}
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		want := "Enable default upstream proxy must be a boolean"
		if lang == "zh-CN" {
			want = "启用默认上游代理 必须为布尔值"
		}
		if response.Code != 400 || body.Error.Code != "invalid_request" || body.Error.MessageKey != "admin.must_be_a_boolean" || body.Error.Message != want || body.Error.Params["Field"] == nil {
			t.Fatalf("%d %s", response.Code, response.Body.String())
		}
	}
	response := performAdminRequest(server, http.MethodGet, "/admin/api/session", nil, nil, nil)
	if response.Header().Get("Content-Language") != "en" || !strings.Contains(response.Body.String(), "Management session") {
		t.Fatalf("unauthenticated default: %s", response.Body.String())
	}
}
