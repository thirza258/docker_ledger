package database

import (
    "context"
    "errors"
    "fmt"
    "time"

    "gorm.io/driver/postgres"
    "gorm.io/gorm"
    gormlogger "gorm.io/gorm/logger"
    "gorm.io/plugin/opentelemetry/tracing"

    "github.com/thirzq/dockerledger/internal/config"
    "github.com/thirzq/dockerledger/internal/models"
    "github.com/thirzq/dockerledger/internal/telemetry"
)

// NewGormConnection creates a GORM DB connection using the shared config.
func NewGormConnection(cfg *config.Config) (*gorm.DB, error) {
    dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
        cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName, cfg.DBSSLMode)

    db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
        Logger: &slogGormLogger{},
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

    // Emit a span per SQL statement, as a child of the HTTP server span.
    // Metrics are left off: this stack has no metrics pipeline yet.
    if err := db.Use(tracing.NewPlugin(tracing.WithoutMetrics())); err != nil {
        return nil, fmt.Errorf("failed to install gorm tracing plugin: %w", err)
    }

    if err := db.AutoMigrate(&models.Container{}, &models.LogEntry{}, &models.WakeProxyState{}); err != nil {
        return nil, fmt.Errorf("auto‑migration failed: %w", err)
    }

    telemetry.Logger.Info("database connected and schemas migrated")
    return db, nil
}

// slogGormLogger routes GORM logs through slog, respecting the configured level.
type slogGormLogger struct{}

func (l *slogGormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
    return l // same behaviour at all levels
}

func (l *slogGormLogger) Info(ctx context.Context, msg string, data ...interface{}) {
    telemetry.WithRequestID(ctx).Info("gorm: "+fmt.Sprint(append([]interface{}{msg}, data...)...))
}

func (l *slogGormLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
    telemetry.WithRequestID(ctx).Warn("gorm: "+fmt.Sprint(append([]interface{}{msg}, data...)...))
}

func (l *slogGormLogger) Error(ctx context.Context, msg string, data ...interface{}) {
    // Suppress record-not-found errors — they are benign and expected
    // in first-time lookups within the repository layer.
    if errors.Is(gorm.ErrRecordNotFound, errFromData(data)) {
        return
    }
    telemetry.WithRequestID(ctx).Info("gorm: "+fmt.Sprint(append([]interface{}{msg}, data...)...))
}

func (l *slogGormLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
    elapsed := time.Since(begin)
    sql, rows := fc()
    attrs := []any{"latency_ms", elapsed.Milliseconds(), "rows", rows, "sql", sql}
    if err != nil {
        // Suppress record-not-found — expected in repository first-time lookups.
        if errors.Is(err, gorm.ErrRecordNotFound) {
            telemetry.WithRequestID(ctx).Debug("gorm query", append(attrs, "error", err)...)
            return
        }
        attrs = append(attrs, "error", err)
        telemetry.WithRequestID(ctx).Warn("gorm query", attrs...)
    } else {
        telemetry.WithRequestID(ctx).Debug("gorm query", attrs...)
    }
}

// errFromData checks if any variadic data element is a gorm.ErrRecordNotFound.
func errFromData(data []interface{}) error {
    for _, d := range data {
        if err, ok := d.(error); ok {
            return err
        }
    }
    return nil
}