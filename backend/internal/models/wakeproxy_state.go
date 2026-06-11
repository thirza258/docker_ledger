package models

type WakeProxyState struct {
    ContainerID string `gorm:"primaryKey"`
    Active      bool   `gorm:"default:true"`
}