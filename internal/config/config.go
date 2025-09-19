package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config содержит конфигурацию сервиса
type Config struct {
	Server   ServerConfig
	Registry RegistryConfig
}

// ServerConfig содержит настройки сервера
type ServerConfig struct {
	Host string
	Port int
}

// RegistryConfig содержит настройки реестра образов
type RegistryConfig struct {
	URL string
}

// Load загружает конфигурацию из переменных окружения
func Load() (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Host: getEnv("SERVER_HOST", "localhost"),
			Port: getEnvInt("SERVER_PORT", 8080),
		},
		Registry: RegistryConfig{
			URL: getEnv("REGISTRY_URL", "yakwilik"),
		},
	}

	return cfg, nil
}

// Address возвращает адрес сервера в формате host:port
func (s *ServerConfig) Address() string {
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

// GetImageName формирует полное имя образа плагина
func (r *RegistryConfig) GetImageName(pluginInfo string) string {
	return fmt.Sprintf("%s/%s", r.URL, pluginInfo)
}

// getEnv возвращает значение переменной окружения или значение по умолчанию
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvInt возвращает целочисленное значение переменной окружения или значение по умолчанию
func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}
