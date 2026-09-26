package main

import (
	"net/http"

	"aat/aggregator/internal/aggregate"
	"aat/internal/httpkit"
)

type server struct {
	store aggregate.Store
}

func handler(store aggregate.Store, token string) http.Handler {
	s := &server{store: store}
	mux := httpkit.Router()
	private := mux.Group("", httpkit.Auth("Authorization", "Bearer "+token))

	mux.GET("/health", s.health)
	private.GET("/internal/hazards", s.hazards)
	private.POST("/internal/ingest/bmkg", s.ingest("BMKG"))
	private.POST("/internal/ingest/pvmbg", s.ingest("PVMBG"))

	return mux
}
