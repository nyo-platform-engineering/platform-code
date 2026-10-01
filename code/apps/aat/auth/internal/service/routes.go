package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"aat/internal/httpkit"
	"github.com/gin-gonic/gin"
)

func decode(c *gin.Context, body any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(body); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("expected one JSON document")
	}
	return nil
}

func Handler(store *Store, internalToken string, concurrent int) http.Handler {
	router := httpkit.Router()
	router.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	})
	router.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	send := func(c *gin.Context, pair Pair, err error) {
		if errors.Is(err, ErrCapacity) {
			c.Header("Retry-After", "1")
			c.JSON(429, gin.H{"error": "session capacity reached"})
		} else if err != nil {
			c.JSON(401, gin.H{"error": "invalid credentials or token"})
		} else {
			c.JSON(200, pair)
		}
	}
	router.POST("/auth/token", func(c *gin.Context) {
		var body struct {
			ClientID string `json:"client_id"`
			Secret   string `json:"client_secret"`
		}
		if decode(c, &body) != nil {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}
		pair, err := store.Login(body.ClientID, body.Secret)
		send(c, pair, err)
	})
	router.POST("/auth/refresh", func(c *gin.Context) {
		var body struct {
			Token string `json:"refresh_token"`
		}
		if decode(c, &body) != nil {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}
		pair, err := store.Refresh(body.Token)
		send(c, pair, err)
	})
	private := router.Group("/internal", httpkit.Auth("Authorization", "Bearer "+internalToken))
	private.POST("/introspect", func(c *gin.Context) {
		var body struct {
			Token string `json:"token"`
		}
		if decode(c, &body) != nil {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}
		id, active := store.Identity(body.Token)
		if !active {
			c.JSON(200, gin.H{"active": false})
			return
		}
		c.JSON(200, gin.H{"active": true, "client_id": id})
	})
	return httpkit.Limit(concurrent, router)
}
