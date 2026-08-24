package database

import (
    "context"
    "errors"
    "fmt"
    "strings"
    "time"

    "gorm.io/driver/postgres"
    "gorm.io/gorm"
    gormlogger "gorm.io/gorm/logger"
    "gorm.io/plugin/opentelemetry/tracing"

    "github.com/thirzq/dockerledger/internal/config"
    "github.com/thirzq/dockerledger/internal/models"
    "github.com/thirzq/dockerledger/internal/telemetry"
)

// slowQueryThreshold is how long a statement may take before its trace line is
// promoted from debug to warn. Slow queries are the ones worth seeing without
// turning on debug logging for every statement the service runs.
const slowQueryThreshold = 200 * time.Millisecond

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
    return l // the threshold is handled by slog via LOG_LEVEL
}

// gormMessage renders one of GORM's log messages.
//
// GORM's logger interface is printf-shaped: msg is a format string and data
// holds its arguments. Rendering it with fmt.Sprint instead ignored the verbs
// and inserted no separator between operands, so messages came out mangled
// ("migrating tablecontainers"). Messages also carry a trailing newline, which
// has no place in a structured log line.
func gormMessage(msg string, data []interface{}) string {
    switch {
    case len(data) == 0:
        // Nothing to interpolate.
    case strings.ContainsRune(msg, '%'):
        msg = fmt.Sprintf(msg, data...)
    default:
        // No verbs to fill, so append the values rather than dropping them.
        parts := make([]string, 0, len(data)+1)
        parts = append(parts, msg)
        for _, d := range data {
            parts = append(parts, fmt.Sprint(d))
        }
        msg = strings.Join(parts, " ")
    }
    return strings.TrimSpace(msg)
}

func (l *slogGormLogger) Info(ctx context.Context, msg string, data ...interface{}) {
    telemetry.WithRequestID(ctx).Info(gormMessage(msg, data), "component", "gorm")
}

func (l *slogGormLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
    telemetry.WithRequestID(ctx).Warn(gormMessage(msg, data), "component", "gorm")
}

func (l *slogGormLogger) Error(ctx context.Context, msg string, data ...interface{}) {
    // Suppress record-not-found errors — they are benign and expected
    // in first-time lookups within the repository layer.
    if errors.Is(errFromData(data), gorm.ErrRecordNotFound) {
        return
    }
    telemetry.WithRequestID(ctx).Error(gormMessage(msg, data), "component", "gorm")
}

func (l *slogGormLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
    elapsed := time.Since(begin)
    sql, rows := fc()

    attrs := []any{"component", "gorm", "latency_ms", elapsed.Milliseconds(), "rows", rows, "sql", sql}
    logger := telemetry.WithRequestID(ctx)

    // Only the routine per-statement trace keeps the message "gorm query":
    // config/otel-collector-config.yaml drops that exact string so the debug
    // firehose never reaches Loki. Failures and slow statements get their own
    // messages, or that same filter would swallow them too.
    switch {
    case err != nil && errors.Is(err, gorm.ErrRecordNotFound):
        // Expected in repository first-time lookups.
        logger.Debug("gorm query", append(attrs, "error", err.Error())...)
    case err != nil:
        logger.Warn("gorm query failed", append(attrs, "error", err.Error())...)
    case elapsed >= slowQueryThreshold:
        logger.Warn("gorm slow query", append(attrs, "threshold_ms", slowQueryThreshold.Milliseconds())...)
    default:
        logger.Debug("gorm query", attrs...)
    }
}

// errFromData returns the first error among GORM's variadic log arguments.
func errFromData(data []interface{}) error {
    for _, d := range data {
        if err, ok := d.(error); ok {
            return err
        }
    }
    return nil
}
