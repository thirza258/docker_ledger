package storage

import (
    "context"

    "gorm.io/gorm"
    "github.com/thirzq/dockerledger/internal/models"
)

type ContainerRepository struct {
    db *gorm.DB
}

func NewContainerRepository(db *gorm.DB) *ContainerRepository {
    return &ContainerRepository{db: db}
}

// Upsert inserts or updates a container by ID.
func (r *ContainerRepository) Upsert(ctx context.Context, id, name string) error {
    container := models.Container{
        ID:   id,
        Name: name,
    }
    // GORM's Save: updates all fields, but we need to avoid overwriting CreatedAt.
    // Use OnConflict clause (PostgreSQL specific) or Upsert with Clauses.
    return r.db.WithContext(ctx).Where("id = ?", id).Assign(&container).FirstOrCreate(&container).Error
}

// FindAll returns all containers.
func (r *ContainerRepository) FindAll(ctx context.Context) ([]models.Container, error) {
    var containers []models.Container
    err := r.db.WithContext(ctx).Order("name").Find(&containers).Error
    return containers, err
}
