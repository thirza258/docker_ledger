package storage

import (
    "context"
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
        
        // Timestamp will be set by GORM's default: now()
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

    searchPattern := "%" + query + "%"
    db = db.Joins("JOIN containers ON containers.id = logs.container_id").
        Where("logs.message ILIKE ?", searchPattern)

    if containerName != "" {
        db = db.Where("containers.name = ?", containerName)
    }
    if fromTime != nil {
        db = db.Where("logs.timestamp >= ?", *fromTime)
    }
    if toTime != nil {
        db = db.Where("logs.timestamp <= ?", *toTime)
    }

    if limit <= 0 || limit > 1000 {
        limit = 100
    }

    err := db.Order("logs.timestamp DESC").Limit(limit).Find(&logs).Error
    return logs, err
}

func (r *LogRepository) GetLogsSince(ctx context.Context, since time.Time, limit int) ([]models.LogEntry, error) {
    var logs []models.LogEntry
    err := r.db.WithContext(ctx).
        Joins("JOIN containers ON containers.id = logs.container_id").
        Select("logs.*, containers.name as container_name").
        Where("logs.timestamp >= ?", since).
        Order("logs.timestamp DESC").
        Limit(limit).
        Find(&logs).Error
    return logs, err
}

func (r *LogRepository) GetLogsByContainerSince(ctx context.Context, containerName string, since time.Time, limit int) ([]models.LogEntry, error) {
    var logs []models.LogEntry
    err := r.db.WithContext(ctx).
        Where("container_name = ? AND timestamp >= ?", containerName, since).
        Order("timestamp DESC").
        Limit(limit).
        Find(&logs).Error
    return logs, err
}