package internal

import (
	"aat/aggregator/internal/controller"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestP4CrashEndpointOnlyWorksInAuthenticatedVerificationMode(t *testing.T) {
	for _, tc := range []struct {
		schema, token string
		want          int
	}{{"", "Bearer internal", 404}, {"p4_verify_012345abcdef_baseline", "", 401}, {"p4_verify_012345abcdef_baseline", "Bearer internal", 202}} {
		called := false
		ctrl := &controller.Controller{SatelliteVerificationSchema: tc.schema, VerificationCrash: func() { called = true }}
		handler := NewHandler(ctrl, "internal")
		r := httptest.NewRequest(http.MethodPost, "/internal/satellite/verification/crash", nil)
		r.Header.Set("Authorization", tc.token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d want %d", w.Code, tc.want)
		}
		if called != (tc.want == 202) {
			t.Fatal("crash hook escaped mode/auth guard")
		}
	}
}
func TestP4VerificationRejectsUnrelatedWrites(t *testing.T) {
	handler := NewHandler(&controller.Controller{SatelliteVerificationSchema: "p4_verify_012345abcdef_baseline"}, "internal")
	r := httptest.NewRequest(http.MethodPost, "/internal/field-reports", nil)
	r.Header.Set("Authorization", "Bearer internal")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("verification accepted an unrelated write: %d", w.Code)
	}
}
