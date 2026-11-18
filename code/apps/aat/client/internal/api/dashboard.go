package api

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed web/*
var dashboard embed.FS

func registerDashboard(router *gin.Engine) {
	for path, asset := range map[string]string{"/": "index.html", "/dashboard.css": "dashboard.css", "/dashboard.js": "dashboard.js", "/favicon.svg": "favicon.svg", "/favicon.ico": "favicon.svg"} {
		router.GET(path, func(c *gin.Context) {
			c.Header("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
			c.Header("X-Content-Type-Options", "nosniff")
			c.Header("Referrer-Policy", "no-referrer")
			c.Header("Cache-Control", "no-store")
			types := map[string]string{"index.html": "text/html; charset=utf-8", "dashboard.css": "text/css; charset=utf-8", "dashboard.js": "text/javascript; charset=utf-8", "favicon.svg": "image/svg+xml"}
			body, err := dashboard.ReadFile("web/" + asset)
			if err != nil {
				c.Status(http.StatusInternalServerError)
				return
			}
			c.Data(200, types[asset], body)
		})
	}
}
