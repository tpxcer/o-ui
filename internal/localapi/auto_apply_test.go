package localapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApplySavedRequiresJSONAndRuntime(t *testing.T) {
	mux := http.NewServeMux()
	registerApplySaved(mux, nil)
	for _, c := range []struct {
		contentType string
		want        int
	}{{"application/json", 503}, {"text/plain", 503}} {
		r := httptest.NewRequest("POST", "/api/runtime/apply-saved", strings.NewReader("{}"))
		r.Header.Set("Content-Type", c.contentType)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Fatal(c.contentType, w.Code)
		}
	}
}
