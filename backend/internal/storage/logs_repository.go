package storage

import (
    "context"
    "log/slog"
    "time"

    "gorm.io/gorm"
    "github.com/thirzq/dockerledger/internal/models"
)

type LogRepository struct {
    db *gorm.DB
}

func NewLogRepository(db *gorm.DB) *LogRepository {
    return &LogRepository{db: db}
}

func (r *LogRepository) InsertLog(ctx context.Context, containerID, message, stream string) error {
    logEntry := models.LogEntry{
        ContainerID: containerID,
        Message:     message,
        Stream:      stream,
        // timestamp is NOT NULL with no DB default, so it has to be set here;
        // leaving it zero writes year 1 and breaks every time-range query.
        Timestamp: time.Now(),
    }
    return r.db.WithContext(ctx).Create(&logEntry).Error
}

func (r *LogRepository) GetRecentLogs(ctx context.Context, containerID string, limit int) ([]models.LogEntry, error) {
    var logs []models.LogEntry
    err := r.db.WithContext(ctx).
        Where("container_id = ?", containerID).
        Order("timestamp DESC").
        Limit(limit).
        Find(&logs).Error
    return logs, err
}

func (r *LogRepository) BatchInsertLogs(ctx context.Context, logs []models.LogEntry) error {
    if len(logs) == 0 {
        return nil
    }
    return r.db.WithContext(ctx).CreateInBatches(logs, 1000).Error
}

func (r *LogRepository) SearchLogs(ctx context.Context, query, containerName string, fromTime, toTime *time.Time, limit int) ([]models.LogEntry, error) {
    var logs []models.LogEntry
    db := r.db.WithContext(ctx)

    // The table backing models.LogEntry is log_entries — there is no "logs"
    // table, so every column reference has to be qualified with log_entries.
    searchPattern := "%" + query + "%"
    db = db.Table("log_entries").
        Joins("JOIN containers ON containers.id = log_entries.container_id").
        Select("log_entries.*, containers.name as container_name").
        Where("log_entries.message ILIKE ?", searchPattern)

    if containerName != "" {
        db = db.Where("containers.name = ?", containerName)
    }
    if fromTime != nil {
        db = db.Where("log_entries.timestamp >= ?", *fromTime)
    }
    if toTime != nil {
        db = db.Where("log_entries.timestamp <= ?", *toTime)
    }

    if limit <= 0 || limit > 1000 {
        limit = 100
    }

    err := db.Order("log_entries.timestamp DESC").Limit(limit).Find(&logs).Error
    return logs, err
}

func (r *LogRepository) GetLogsSince(ctx context.Context, since time.Time, limit int) ([]models.LogEntry, error) {
    var logs []models.LogEntry
    err := r.db.WithContext(ctx).
        Table("log_entries").
        Joins("JOIN containers ON containers.id = log_entries.container_id").
        Select("log_entries.*, containers.name as container_name").
        Where("log_entries.timestamp >= ?", since).
        Order("log_entries.timestamp DESC").
        Limit(limit).
        Find(&logs).Error
    return logs, err
}

func (r *LogRepository) GetLogsByContainerSince(
    ctx context.Context,
    containerName string,
    since time.Time,
    limit int,
) ([]models.LogEntry, error) {

    var container models.Container
    if err := r.db.WithContext(ctx).
        Where("name = ?", containerName).
        First(&container).Error; err != nil {
        return nil, err
    }

    var logs []models.LogEntry
    err := r.db.WithContext(ctx).
        Where("container_id = ? AND timestamp >= ?", container.ID, since).
        Order("timestamp DESC").
        Limit(limit).
        Find(&logs).Error

    // No join here, so fill the name in from the container we just looked up —
    // callers (the AI prompt builder) render it on every line.
    for i := range logs {
        logs[i].ContainerName = container.Name
    }

    return logs, err
}

// DeleteLogsBefore removes log entries older than the given cutoff.
// Returns the number of rows deleted.
func (r *LogRepository) DeleteLogsBefore(ctx context.Context, before time.Time) (int64, error) {
    result := r.db.WithContext(ctx).
        Where("timestamp < ?", before).
        Delete(&models.LogEntry{})
    if result.Error != nil {
        return 0, result.Error
    }
    if result.RowsAffected > 0 {
        slog.Info("log retention cleanup",
            "deleted", result.RowsAffected,
            "before", before.Format(time.RFC3339),
        )
    }
    return result.RowsAffected, nil
}