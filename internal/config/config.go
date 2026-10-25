// Package config содержит загрузку и проверку технической конфигурации.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	minPort = 1
	maxPort = 65535

	defaultTelegramPollTimeout    = 30 * time.Second
	defaultTelegramRequestTimeout = 40 * time.Second
	defaultShutdownTimeout        = 10 * time.Second
	defaultLogLevel               = slog.LevelInfo
)

// Config содержит технические настройки. Настройки акции хранятся в БД.
// Конфигурацию целиком не выводим: она содержит токен и строку подключения.
type Config struct {
	Telegram         Telegram
	Database         Database
	AdminTelegramIDs []int64
	LogLevel         slog.Level
	ShutdownTimeout  time.Duration
}

// Telegram содержит настройки клиента Bot API.
type Telegram struct {
	Token          string
	PollTimeout    time.Duration
	RequestTimeout time.Duration
}

// Database содержит строку подключения к PostgreSQL в формате URL.
type Database struct {
	URL string
}

// Load читает окружение и возвращает конфигурацию только после успешной проверки.
// Ошибки содержат имена параметров и требования, но не их значения.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	var problems []error

	duration := func(name string, fallback time.Duration) time.Duration {
		value, exists := lookup(name)
		if !exists {
			return fallback
		}
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			problems = append(problems, fmt.Errorf("%s must be a positive duration, for example 10s", name))
			return 0
		}
		return parsed
	}

	token, _ := lookup("TELEGRAM_BOT_TOKEN")
	databaseURL, _ := lookup("DATABASE_URL")

	cfg := Config{
		Telegram: Telegram{
			Token:          token,
			PollTimeout:    duration("TELEGRAM_POLL_TIMEOUT", defaultTelegramPollTimeout),
			RequestTimeout: duration("TELEGRAM_REQUEST_TIMEOUT", defaultTelegramRequestTimeout),
		},
		Database:        Database{URL: databaseURL},
		LogLevel:        defaultLogLevel,
		ShutdownTimeout: duration("SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
	}
	if value, exists := lookup("LOG_LEVEL"); exists {
		if err := cfg.LogLevel.UnmarshalText([]byte(value)); err != nil {
			problems = append(problems, errors.New("LOG_LEVEL must be a slog level: DEBUG, INFO, WARN or ERROR, optionally with a numeric offset"))
		}
	}
	if value, exists := lookup("ADMIN_TELEGRAM_IDS"); exists {
		var err error
		cfg.AdminTelegramIDs, err = parseAdminTelegramIDs(value)
		if err != nil {
			problems = append(problems, err)
		}
	}
	if err := validate(cfg); err != nil {
		problems = append(problems, err)
	}
	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(problems...))
	}
	return cfg, nil
}

// Первые администраторы задаются явно по неизменяемому Telegram ID.
// Пустой список не выдает права первому написавшему пользователю.
func parseAdminTelegramIDs(value string) ([]int64, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var ids []int64
	seen := make(map[int64]bool)
	for part := range strings.SplitSeq(value, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("ADMIN_TELEGRAM_IDS must be comma-separated positive Telegram user IDs")
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, nil
}

func validate(cfg Config) error {
	var problems []error

	if strings.TrimSpace(cfg.Telegram.Token) == "" {
		problems = append(problems, errors.New("TELEGRAM_BOT_TOKEN is required"))
	} else if strings.ContainsFunc(cfg.Telegram.Token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		problems = append(problems, errors.New("TELEGRAM_BOT_TOKEN must not contain whitespace or control characters"))
	}
	if strings.TrimSpace(cfg.Database.URL) == "" {
		problems = append(problems, errors.New("DATABASE_URL is required"))
	} else if !validDatabaseURL(cfg.Database.URL) {
		problems = append(problems, errors.New("DATABASE_URL must be a postgres:// or postgresql:// URL with a host, database name and valid port"))
	}
	if cfg.Telegram.PollTimeout > 0 && cfg.Telegram.PollTimeout%time.Second != 0 {
		problems = append(problems, errors.New("TELEGRAM_POLL_TIMEOUT must be a whole number of seconds"))
	}
	if cfg.Telegram.RequestTimeout > 0 && cfg.Telegram.RequestTimeout <= cfg.Telegram.PollTimeout {
		problems = append(problems, errors.New("TELEGRAM_REQUEST_TIMEOUT must exceed TELEGRAM_POLL_TIMEOUT"))
	}
	return errors.Join(problems...)
}

func validDatabaseURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Opaque != "" ||
		parsed.Hostname() == "" || strings.Trim(parsed.Path, "/") == "" || parsed.Fragment != "" {
		return false
	}
	if _, err := url.ParseQuery(parsed.RawQuery); err != nil {
		return false
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < minPort || number > maxPort {
			return false
		}
	}
	return true
}
