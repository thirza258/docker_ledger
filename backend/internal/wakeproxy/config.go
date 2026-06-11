package wakeproxy

import "time"

type ServiceConfig struct {
	Name      string `yaml:"name"`
	Host      string `yaml:"host"`
	Container string `yaml:"container"`
	Port      int    `yaml:"port"`
	Network   string `yaml:"network"`
}

type Config struct {
	ListenAddr   string          `yaml:"listen_addr"`
	AdminEnabled bool            `yaml:"admin_enabled"`
	AdminAddr    string          `yaml:"admin_addr"`
	IdleTimeout  time.Duration   `yaml:"idle_timeout"`
	Services     []ServiceConfig `yaml:"services"`
}