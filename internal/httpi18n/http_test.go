package httpi18n

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/gin-gonic/gin"
)

func TestConcurrentLanguagesAndOpaquePayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Header("Vary", "Origin") }, Middleware())
	payload := gin.H{"message": i18n.M("admin.saved_settings", i18n.Params{"Count": 1}), "name": "可用", "output": "账号不存在", "user_data": gin.H{"message": "已保存 2 项配置"}}
	router.GET("/", func(c *gin.Context) { JSON(c, http.StatusOK, payload) })
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lang, want := "en", "Saved 1 setting"
			if i%2 == 1 {
				lang, want = "zh-CN", "已保存 1 项配置"
			}
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Accept-Language", lang)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Error(err)
				return
			}
			if body["message"] != want || body["name"] != "可用" || body["output"] != "账号不存在" || body["user_data"].(map[string]any)["message"] != "已保存 2 项配置" {
				t.Errorf("payload: %s", response.Body.String())
			}
			if body["message_key"] != "admin.saved_settings" || response.Header().Get("Content-Language") != lang || len(response.Header().Values("Vary")) != 2 {
				t.Errorf("metadata: %v %v", body, response.Header())
			}
		}(i)
	}
	wg.Wait()
	if _, ok := payload["message"].(*i18n.Message); !ok {
		t.Fatal("shared payload mutated")
	}
}

func TestErrorEnvelopeRetainsMachineContract(t *testing.T) {
	router := gin.New()
	router.Use(Middleware())
	router.GET("/", func(c *gin.Context) {
		Error(c, 400, i18n.M("admin.must_be_a_boolean", i18n.Params{"Field": i18n.Ref("admin.product_name")}), "invalid_request", "request_error")
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Language", "zh-CN")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	var body struct {
		Error struct {
			Notice
			Code, Type string
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "invalid_request" || body.Error.Type != "request_error" || body.Error.MessageKey != "admin.must_be_a_boolean" || body.Error.Message != "产品名称 必须为布尔值" {
		t.Fatalf("%s", response.Body.String())
	}
}
