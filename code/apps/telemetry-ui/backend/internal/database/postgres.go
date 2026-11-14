package database

import (
	"context"
	"errors"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Open(ctx context.Context, dsn string) (*gorm.DB, error) {
	if dsn == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, errors.New("invalid PostgreSQL configuration")
	}
	pool, err := db.DB()
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(4)
	pool.SetMaxIdleConns(2)
	pool.SetConnMaxLifetime(30 * time.Minute)
	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, errors.New("cannot connect to PostgreSQL; check DATABASE_URL and database availability")
	}
	return db, nil
}

func URLFromEnv() string {
	if value := os.Getenv("DATABASE_URL"); value != "" {
		return value
	}
	return os.Getenv("AUTH_DATABASE_URL") // Deprecated compatibility alias.
}

func Close(db *gorm.DB) error {
	pool, err := db.DB()
	if err != nil {
		return err
	}
	return pool.Close()
}
