package main

import (
	"aat/auth/internal/service"
	"aat/internal/httpkit"
	"time"
)

func main() {
	clients := map[string]string{
		"public":    httpkit.Secret("PUBLIC_CLIENT_SECRET"),
		"responder": httpkit.Secret("RESPONDER_CLIENT_SECRET"),
		"analyst":   httpkit.Secret("ANALYST_CLIENT_SECRET"),
	}
	internal := httpkit.Secret("AUTH_INTERNAL_TOKEN")
	seen := map[string]bool{internal: true}
	for _, secret := range clients {
		if len(secret) < 32 || seen[secret] {
			panic("client secrets must be distinct and at least 32 characters")
		}
		seen[secret] = true
	}
	if len(internal) < 32 {
		panic("AUTH_INTERNAL_TOKEN must be at least 32 characters")
	}
	store := service.NewStore(clients,
		time.Duration(httpkit.Integer("ACCESS_TTL_SECONDS", 300))*time.Second,
		time.Duration(httpkit.Integer("REFRESH_TTL_SECONDS", 3600))*time.Second,
		httpkit.Integer("AUTH_MAX_SESSIONS", 1000))
	httpkit.Serve("Auth", "8084", service.Handler(store, internal, httpkit.Integer("AUTH_MAX_CONCURRENT", 32)))
}
