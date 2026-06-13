package config

import (
    "log"
    "os"
    "time"

    "github.com/joho/godotenv"
    "github.com/thirzq/dockerledger/internal/wakeproxy"
)

type Config struct {
    // Database
    DBHost     string
    DBPort     string
    DBUser     string
    DBPassword string
    DBName     string
    DBSSLMode  string

    // Server
    ServerPort string

    // Docker
    DockerHost string

    // Timeouts
    ShutdownTimeout time.Duration

    OpenRouterAPIKey string
    OpenRouterModel  string

    Wakeproxy *wakeproxy.Config `yaml:"wakeproxy"`

}

// Load initializes configuration from .env file and environment variables.
func Load() *Config {
    // Try to load .env file (ignore if not found)
    if err := godotenv.Load(); err != nil {
        log.Println("No .env file found, relying on environment variables")
    }

    return &Config{
        DBHost:     getEnv("DB_HOST", "localhost"),
        DBPort:     getEnv("DB_PORT", "5432"),
        DBUser:     getEnv("DB_USER", "postgres"),
        DBPassword: getEnv("DB_PASSWORD", ""),
        DBName:     getEnv("DB_NAME", "dockerledger"),
        DBSSLMode:  getEnv("DB_SSLMODE", "disable"),

        ServerPort: getEnv("SERVER_PORT", "8080"),

        DockerHost: getEnv("DOCKER_HOST", "unix:///var/run/docker.sock"),

        ShutdownTimeout: getEnvAsDuration("SHUTDOWN_TIMEOUT", 5*time.Second),

        OpenRouterAPIKey: getEnv("OPENROUTER_API_KEY", ""),
        OpenRouterModel:  getEnv("OPENROUTER_MODEL", "google/gemma-4-26b-a4b-it"),
    }
}

// Helper to get environment variable with fallback
func getEnv(key, fallback string) string {
    if value := os.Getenv(key); value != "" {
        return value
    }
    return fallback
}

// Helper to parse duration from environment
func getEnvAsDuration(key string, fallback time.Duration) time.Duration {
    if value := os.Getenv(key); value != "" {
        if d, err := time.ParseDuration(value); err == nil {
            return d
        }
    }
    return fallback
}