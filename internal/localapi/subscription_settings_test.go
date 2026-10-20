package localapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestSubscriptionSettingsValidationAndHeaders(t *testing.T) {
	for _, raw := range []string{`{"enabled":true,"updateHours":0,"title":"","showInfo":true}`, `{"enabled":true,"updateHours":169,"title":"","showInfo":true}`, `{"enabled":true,"updateHours":12,"title":"a\u0007","showInfo":true}`, `{"enabled":true,"updateHours":12,"title":"","showInfo":true,"extra":1}`} {
		if _, err := validateSubscriptionSettings(json.RawMessage(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	s, err := validateSubscriptionSettings(json.RawMessage(`{"enabled":true,"updateHours":24,"title":"我的节点","showInfo":true}`))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	expires := int64(1893456000000)
	subscriptionHeaders(w, s, clientLimit{Quota: 100, ExpiresAt: &expires}, 10, 20)
	if w.Header().Get("Profile-Update-Interval") != "24" || w.Header().Get("Profile-Title") != "base64:5oiR55qE6IqC54K5" || w.Header().Get("Subscription-Userinfo") != "upload=10; download=20; total=100; expire=1893456000" {
		t.Fatal(w.Header())
	}
	s.ShowInfo = false
	w = httptest.NewRecorder()
	subscriptionHeaders(w, s, clientLimit{}, 1, 2)
	if w.Header().Get("Subscription-Userinfo") != "" {
		t.Fatal("usage shown while disabled")
	}
}
