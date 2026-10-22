package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProviderEndpointPlaceholders(t *testing.T) {
	plain, err := providerEndpoint("", "123", "k/ey")
	if err != nil || !strings.HasPrefix(plain, defaultProviderURL+"?") || !strings.Contains(plain, "veid=123") || !strings.Contains(plain, "api_key=k%2Fey") {
		t.Fatal("empty URL should use 64clouds with query parameters", plain, err)
	}
	custom, err := providerEndpoint("https://api.example.test/info/{veid}?key={api_key}", "9", "s3")
	if err != nil || custom != "https://api.example.test/info/9?key=s3" {
		t.Fatal("placeholders should be replaced without duplicating parameters", custom, err)
	}
	if _, err := providerEndpoint("ftp://api.example.test/", "1", "2"); err == nil {
		t.Fatal("non-http URL accepted")
	}
}

func TestParseProviderInfo(t *testing.T) {
	info, err := parseProviderInfo([]byte(`{"error":0,"hostname":"vps1","node_location":"US, Los Angeles","plan":"kvmv3-20g","plan_monthly_data":1099511627776,"plan_disk":21474836480,"plan_ram":1073741824,"plan_swap":"0","data_counter":"123456789","data_next_reset":1793000000,"ip_addresses":["203.0.113.5"],"ptr":{"203.0.113.5":"vps1.example"}}`))
	if err != nil || info.NodeLocation != "US, Los Angeles" || info.PlanMonthlyData != "1099511627776" || info.DataCounter != "123456789" || info.DataNextReset != 1793000000 || info.PTR["203.0.113.5"] != "vps1.example" || len(info.IPAddresses) != 1 {
		t.Fatal(info, err)
	}
	nested, err := parseProviderInfo([]byte(`{"success":true,"data":{"nodeLocation":"JP","dataCounter":5}}`))
	if err != nil || nested.NodeLocation != "JP" || nested.DataCounter != "5" {
		t.Fatal("nested data should be read", nested, err)
	}
	for _, body := range []string{`{"error":700005,"message":"Authentication failure"}`, `{"success":false,"msg":"bad key"}`, `[]`} {
		if _, err := parseProviderInfo([]byte(body)); err == nil {
			t.Fatal("provider error accepted", body)
		}
	}
}

func TestFetchProviderCachesResults(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("veid") != "42" || r.URL.Query().Get("api_key") != "secret" {
			http.Error(w, "bad", 403)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "plan": "p1", "data_counter": 10})
	}))
	defer server.Close()
	providerCache.Lock()
	providerCache.entries = map[string]providerCacheEntry{}
	providerCache.Unlock()
	for i := 0; i < 2; i++ {
		info, err := fetchProvider(context.Background(), server.URL+"/v1/getServiceInfo", "42", "secret")
		if err != nil || info.Plan != "p1" {
			t.Fatal(info, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("successful result should be cached", calls.Load())
	}
	if _, err := fetchProvider(context.Background(), server.URL, "42", "wrong"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatal("HTTP error should be reported", err)
	}
	if _, err := fetchProvider(context.Background(), server.URL, "", "x"); err == nil {
		t.Fatal("missing VEID accepted")
	}
}

// API KEY只写不读：响应不含密钥，留空保存沿用已保存的密钥；拉取结果附在套餐状态里。
func TestServerPlanProviderKeyIsWriteOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "first-key" {
			http.Error(w, "bad", 403)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": 0, "node_location": "US, LA"})
	}))
	defer server.Close()
	providerCache.Lock()
	providerCache.entries = map[string]providerCacheEntry{}
	providerCache.Unlock()
	_, _, call, _ := planTestServer(t)
	plan := map[string]any{"mode": "provider", "node": "", "name": "", "resources": "", "quotaGB": 0, "resetDay": 1, "billing": "both", "interfaces": []string{}, "providerURL": server.URL, "providerVEID": "7", "providerKey": "first-key"}
	w := call("PUT", "/api/system/plan", map[string]any{"revision": 0, "data": plan})
	if w.Code != 200 || strings.Contains(w.Body.String(), "first-key") {
		t.Fatal("key must never be returned", w.Code, w.Body.String())
	}
	plan["providerKey"] = ""
	w = call("PUT", "/api/system/plan", map[string]any{"revision": 1, "data": plan})
	var status struct {
		HasProviderKey bool          `json:"hasProviderKey"`
		Provider       *providerInfo `json:"provider"`
		ProviderError  string        `json:"providerError"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || !status.HasProviderKey || status.Provider == nil || status.Provider.NodeLocation != "US, LA" {
		t.Fatal("blank key should keep the saved one and pull provider data", w.Code, w.Body.String())
	}
	if w = call("PUT", "/api/system/plan", map[string]any{"revision": 2, "data": map[string]any{"mode": "cloud", "node": "", "name": "", "resources": "", "quotaGB": 0, "resetDay": 1, "billing": "both", "interfaces": []string{}}}); w.Code != 400 {
		t.Fatal("unknown mode accepted", w.Code)
	}
}
