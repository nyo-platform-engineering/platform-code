package policy

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
)

func TestPolicyCoverage(t *testing.T) {
	routes := gin.RoutesInfo{}
	for endpoint := range policies {
		routes = append(routes, gin.RouteInfo{Method: endpoint.Method, Path: endpoint.Path})
	}
	if err := ValidateRoutes(routes); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRoutes(append(routes, gin.RouteInfo{Method: "GET", Path: "/api/v1/new"})); err == nil {
		t.Fatal("unclassified route accepted")
	}
	if err := ValidateRoutes(routes[1:]); err == nil {
		t.Fatal("stale policy accepted")
	}
}
func TestUnclassifiedRouteDeniedAtRuntime(t *testing.T) {
	r := gin.New()
	r.Use(Enforce(auth.LocalAuthenticator{}))
	r.GET("/api/v1/new", func(c *gin.Context) { t.Error("unclassified handler ran") })
	out := httptest.NewRecorder()
	r.ServeHTTP(out, httptest.NewRequest("GET", "/api/v1/new", nil))
	if out.Code != 403 {
		t.Fatalf("got %d", out.Code)
	}
}
