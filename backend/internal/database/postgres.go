package database

import (
    "fmt"
    "log"
    "time"

    "gorm.io/driver/postgres"
    "gorm.io/gorm"
    "gorm.io/gorm/logger"

    "github.com/thirzq/dockerledger/internal/config"
    "github.com/thirzq/dockerledger/internal/models"
)

// NewGormConnection creates a GORM DB connection using the shared config.
func NewGormConnection(cfg *config.Config) (*gorm.DB, error) {
    dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
        cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName, cfg.DBSSLMode)

    db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
        Logger: logger.Default.LogMode(logger.Info),
    })
    if err != nil {
        return nil, fmt.Errorf("failed to connect database: %w", err)
    }

    // Configure connection pool
    sqlDB, err := db.DB()
    if err != nil {
        return nil, err
    }
    sqlDB.SetMaxOpenConns(25)
    sqlDB.SetMaxIdleConns(10)
    sqlDB.SetConnMaxLifetime(5 * time.Minute)

    // Auto‑migrate schemas
    if err := db.AutoMigrate(&models.Container{}, &models.LogEntry{}); err != nil {
        return nil, fmt.Errorf("auto‑migration failed: %w", err)
    }

    log.Println("Database connected and schemas migrated")
    return db, nil
}