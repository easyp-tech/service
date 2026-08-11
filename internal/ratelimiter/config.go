package ratelimiter

import "time"

// Config содержит параметры rate limiting.
type Config struct {
	RequestsPerSecond float64       `yaml:"requests_per_second" env:"REQUESTS_PER_SECOND"`
	Burst             int           `yaml:"burst" env:"BURST"`
	CleanupInterval   time.Duration `yaml:"cleanup_interval" env:"CLEANUP_INTERVAL"`
}

// DefaultConfig возвращает конфигурацию по умолчанию.
func DefaultConfig() Config {
	return Config{
		RequestsPerSecond: 10.0,
		Burst:             20,
		CleanupInterval:   10 * time.Minute,
	}
}

// setDefault заменяет непригодные значения на умолчания, возвращая имена
// заменённых полей — вызывающий их логирует, потому что тихая подмена
// настройки хуже, чем её отсутствие.
//
// Существует ради CleanupInterval: StartCleanup отдаёт его в time.NewTicker,
// который на неположительном значении паникует — из фоновой горутины, снаружи
// барьера, то есть уносит процесс уже после того, как старт отчитался об
// успехе. Config.Validate такое значение отвергает, но лимитер собирают и
// напрямую, и он не должен уметь падать.
//
// Подменяется здесь, а не в StartCleanup, потому что то же поле служит порогом
// устаревания вёдер в cleanup: разойдись эти два значения, первая же уборка
// удалила бы все вёдра.
func (c *Config) setDefault() []string {
	var replaced []string

	defaults := DefaultConfig()

	if c.CleanupInterval <= 0 {
		c.CleanupInterval = defaults.CleanupInterval

		replaced = append(replaced, "cleanup_interval")
	}

	if c.RequestsPerSecond <= 0 {
		c.RequestsPerSecond = defaults.RequestsPerSecond

		replaced = append(replaced, "requests_per_second")
	}

	if c.Burst <= 0 {
		c.Burst = defaults.Burst

		replaced = append(replaced, "burst")
	}

	return replaced
}
