package models

import (
    "time"
)


type DockerHealthResponse struct {
	Connected bool `json:"connected"`
}

type DockerContainer struct {
	Id     string   `json:"Id"`
	Names  []string `json:"Names"`
	State  string   `json:"State"`
	Image  string   `json:"Image"`
	Status string   `json:"Status"`
}

type DockerListAllContainer struct {
	Containers []DockerContainer `json:"containers"` // exported field with proper tag
}

type Container struct {
    ID        string    `gorm:"primaryKey;size:64" json:"id"`          
    Name      string    `gorm:"not null;index" json:"name"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type LogEntry struct {
    ID          uint      `gorm:"primaryKey" json:"id"`
    ContainerID string    `gorm:"not null;index;size:64;constraint:OnDelete:CASCADE" json:"container_id"`
    Message     string    `gorm:"not null;type:text" json:"message"`
    Timestamp   time.Time `gorm:"not null;index" json:"timestamp"`
    Stream      string    `gorm:"size:10;check:stream IN ('stdout','stderr')" json:"stream"`
}