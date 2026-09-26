package mock

import (
	"net/http"
	"time"

	"aat/bmkg/internal/mock/controller"
	"aat/bmkg/internal/mock/model"
	"aat/internal/httpkit"
)

func NewHandler(key string, interval, delay time.Duration) http.Handler {
	feed := model.NewFeed(time.Now().UTC(), httpkit.ID(), interval)
	ctrl := &controller.Controller{
		Feed:  feed,
		Delay: delay,
	}
	mux := httpkit.Router()
	private := mux.Group("", httpkit.Auth("X-BMKG-Key", key))

	mux.GET("/health", ctrl.Health)
	private.GET("/seismic-events", ctrl.SeismicEvents)
	private.GET("/tsunami-warnings", ctrl.TsunamiWarnings)

	return mux
}
