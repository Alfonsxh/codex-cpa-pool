package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestLanguageNegotiation(t *testing.T) {
	for input, want := range map[string]Language{"": English, "zh-CN": Chinese, "en-US": English, "fr, zh-Hans;q=0.8, en;q=0.5": Chinese, "zh;q=0, en;q=0.4": English, "zh;q=0.2,en-GB;q=0.8": English, "*": English, "invalid;;;": English, strings.Repeat("zh,", 3000): English} {
		if got := Negotiate(input); got != want {
			t.Errorf("%q: %s, want %s", input, got, want)
		}
	}
}

func TestTypedMessagesPreserveDataPluralAndCauses(t *testing.T) {
	for count, want := range map[int]string{0: "Saved 0 settings", 1: "Saved 1 setting", 2: "Saved 2 settings"} {
		if got := Text(English, "admin.saved_settings", Params{"Count": count}); got != want {
			t.Errorf("%s, want %s", got, want)
		}
	}
	cause := errors.New("opaque diagnostic {{.Count}} 中文")
	message := M("notifications.invalid_notification_timezone", Params{"Detail": cause}).WithCause(cause)
	if !errors.Is(message, cause) {
		t.Fatal("wrapped cause was lost")
	}
	if got := message.Render(English); got != "Invalid notification timezone: "+cause.Error() {
		t.Fatal(got)
	}
	nested := M("admin.must_be_a_boolean", Params{"Field": Ref("admin.product_name")})
	if got := nested.Render(Chinese); got != "产品名称 必须为布尔值" {
		t.Fatal(got)
	}
	found, ok := ErrorMessage(fmt.Errorf("context: %w", nested))
	if !ok || found.ID != nested.ID {
		t.Fatal("typed identity lost")
	}
	if got := Text(English, "admin.saved_settings"); strings.Contains(got, "<no value>") || strings.Contains(got, "{{") {
		t.Fatal(got)
	}
}

// Validate every catalog and all statically referenced IDs. Missing parameters
// and missing singular forms fail here, before fallback text reaches a user.
func TestCatalogsAndSourceBindings(t *testing.T) {
	catalogs := map[Language]map[string]map[string]string{}
	parameter := regexp.MustCompile(`\.([A-Z][A-Za-z0-9]*)`)
	for _, lang := range []Language{English, Chinese} {
		raw, err := catalogFS.ReadFile("catalog/" + string(lang) + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var messages []map[string]string
		if err = json.Unmarshal(raw, &messages); err != nil {
			t.Fatal(err)
		}
		catalog := map[string]map[string]string{}
		for _, message := range messages {
			id := message["id"]
			if catalog[id] != nil {
				t.Fatalf("duplicate %s", id)
			}
			catalog[id] = message
			params := Params{}
			for _, field := range []string{"one", "other"} {
				for _, match := range parameter.FindAllStringSubmatch(message[field], -1) {
					params[match[1]] = 2
				}
			}
			for _, count := range []int{0, 1, 2} {
				if _, ok := params["Count"]; ok {
					params["Count"] = count
				}
				got := Text(lang, id, params)
				if got == "The operation could not be completed." || got == "操作未能完成。" || strings.Contains(got, "<no value>") || strings.Contains(got, "%!") {
					t.Errorf("%s %s count=%d: %s", lang, id, count, got)
				}
			}
		}
		catalogs[lang] = catalog
	}
	keys := func(c map[string]map[string]string) []string {
		var out []string
		for key := range c {
			out = append(out, key)
		}
		sort.Strings(out)
		return out
	}
	if !reflect.DeepEqual(keys(catalogs[English]), keys(catalogs[Chinese])) {
		t.Fatal("catalog IDs differ")
	}
	for id, en := range catalogs[English] {
		parameters := func(message string) []string {
			set := map[string]bool{}
			for _, m := range parameter.FindAllStringSubmatch(message, -1) {
				set[m[1]] = true
			}
			var out []string
			for k := range set {
				out = append(out, k)
			}
			sort.Strings(out)
			return out
		}
		if !reflect.DeepEqual(parameters(en["other"]), parameters(catalogs[Chinese][id]["other"])) {
			t.Errorf("parameter mismatch: %s", id)
		}
	}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || (pkg.Name != "i18n" && pkg.Name != "httpi18n") {
				return true
			}
			index := 0
			switch sel.Sel.Name {
			case "Text":
				index = 1
			case "M", "Ref":
			default:
				return true
			}
			if len(call.Args) <= index {
				return true
			}
			literal, ok := call.Args[index].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			id, _ := strconv.Unquote(literal.Value)
			if catalogs[English][id] == nil {
				t.Errorf("missing %s used in %s", id, path)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
