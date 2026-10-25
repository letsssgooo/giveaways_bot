package config

import (
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	testToken       = "12345:test-token-for-tests"
	testDatabaseURL = "postgres://test_user:test_password@localhost:5432/giveaways?sslmode=disable"
)

func validEnvironment() map[string]string {
	return map[string]string{
		"TELEGRAM_BOT_TOKEN": testToken,
		"DATABASE_URL":       testDatabaseURL,
	}
}

func fromEnvironment(values map[string]string) (Config, error) {
	return load(func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	})
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := fromEnvironment(validEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.Token != testToken || cfg.Database.URL != testDatabaseURL {
		t.Fatal("credentials were modified")
	}
	if cfg.LogLevel != slog.LevelInfo || cfg.ShutdownTimeout != 10*time.Second ||
		cfg.Telegram.PollTimeout != 30*time.Second || cfg.Telegram.RequestTimeout != 40*time.Second {
		t.Fatal("unexpected configuration defaults")
	}
}

func TestLoadOverrides(t *testing.T) {
	env := validEnvironment()
	env["DATABASE_URL"] = "postgresql://test_user:test_password@[::1]:5433/giveaways"
	env["LOG_LEVEL"] = "debug"
	env["SHUTDOWN_TIMEOUT"] = "2m"
	env["TELEGRAM_POLL_TIMEOUT"] = "15s"
	env["TELEGRAM_REQUEST_TIMEOUT"] = "20s"
	cfg, err := fromEnvironment(env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != slog.LevelDebug || cfg.ShutdownTimeout != 2*time.Minute ||
		cfg.Telegram.PollTimeout != 15*time.Second || cfg.Telegram.RequestTimeout != 20*time.Second {
		t.Fatal("configuration overrides were not applied")
	}
	if cfg.Database.URL != env["DATABASE_URL"] {
		t.Fatal("database URL was modified")
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"empty token", "TELEGRAM_BOT_TOKEN", ""},
		{"blank token", "TELEGRAM_BOT_TOKEN", " \t"},
		{"token with whitespace", "TELEGRAM_BOT_TOKEN", testToken + "\n"},
		{"token with control character", "TELEGRAM_BOT_TOKEN", testToken + "\x00"},
		{"empty database URL", "DATABASE_URL", ""},
		{"wrong database protocol", "DATABASE_URL", "https://localhost/giveaways"},
		{"missing database host", "DATABASE_URL", "postgres:///giveaways"},
		{"missing database name", "DATABASE_URL", "postgres://localhost/"},
		{"invalid database escape", "DATABASE_URL", "postgres://test_user:test_password%zz@localhost/giveaways"},
		{"invalid database port", "DATABASE_URL", "postgres://test_user:test_password@localhost:bad/giveaways"},
		{"database port outside range", "DATABASE_URL", "postgres://localhost:65536/giveaways"},
		{"database port zero", "DATABASE_URL", "postgres://localhost:0/giveaways"},
		{"database URL fragment", "DATABASE_URL", testDatabaseURL + "#fragment"},
		{"invalid database query", "DATABASE_URL", "postgres://localhost/giveaways?password=%zz"},
		{"empty log level", "LOG_LEVEL", ""},
		{"invalid log level", "LOG_LEVEL", testToken},
		{"empty shutdown timeout", "SHUTDOWN_TIMEOUT", ""},
		{"invalid shutdown timeout", "SHUTDOWN_TIMEOUT", testDatabaseURL},
		{"zero shutdown timeout", "SHUTDOWN_TIMEOUT", "0s"},
		{"negative shutdown timeout", "SHUTDOWN_TIMEOUT", "-1s"},
		{"duration overflow", "SHUTDOWN_TIMEOUT", "999999999999999999999999h"},
		{"invalid poll timeout", "TELEGRAM_POLL_TIMEOUT", "later"},
		{"zero poll timeout", "TELEGRAM_POLL_TIMEOUT", "0s"},
		{"fractional poll timeout", "TELEGRAM_POLL_TIMEOUT", "1500ms"},
		{"invalid request timeout", "TELEGRAM_REQUEST_TIMEOUT", "later"},
		{"negative request timeout", "TELEGRAM_REQUEST_TIMEOUT", "-1s"},
		{"request timeout equals poll timeout", "TELEGRAM_REQUEST_TIMEOUT", "30s"},
		{"request timeout below poll timeout", "TELEGRAM_REQUEST_TIMEOUT", "20s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnvironment()
			env[tt.key] = tt.value
			cfg, err := fromEnvironment(env)
			if err == nil {
				t.Fatal("invalid configuration was accepted")
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("error does not identify the parameter: %v", err)
			}
			if !reflect.DeepEqual(cfg, Config{}) {
				t.Fatal("a partial configuration was returned")
			}
			assertNoSecrets(t, err.Error())
		})
	}
}

func TestLoadReportsAllMissingParameters(t *testing.T) {
	_, err := fromEnvironment(map[string]string{"LOG_LEVEL": "unknown"})
	if err == nil {
		t.Fatal("missing required parameters were accepted")
	}
	for _, key := range []string{"TELEGRAM_BOT_TOKEN", "DATABASE_URL", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("missing error for %s: %v", key, err)
		}
	}
}

func TestLoadReadsProcessEnvironment(t *testing.T) {
	for name, value := range map[string]string{
		"TELEGRAM_BOT_TOKEN":       testToken,
		"DATABASE_URL":             testDatabaseURL,
		"LOG_LEVEL":                "WARN",
		"SHUTDOWN_TIMEOUT":         "10s",
		"TELEGRAM_POLL_TIMEOUT":    "30s",
		"TELEGRAM_REQUEST_TIMEOUT": "40s",
		"ADMIN_TELEGRAM_IDS":       "",
	} {
		t.Setenv(name, value)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.Token != testToken || cfg.LogLevel != slog.LevelWarn {
		t.Fatal("process environment was not applied")
	}
}

func TestLoadAdminTelegramIDs(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []int64
	}{
		{"empty", "", nil},
		{"blank", " \t", nil},
		{"single", "9876543210", []int64{9876543210}},
		{"multiple and duplicates", " 101, 9876543210,101 ", []int64{101, 9876543210}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnvironment()
			env["ADMIN_TELEGRAM_IDS"] = tt.value
			cfg, err := fromEnvironment(env)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cfg.AdminTelegramIDs, tt.want) {
				t.Fatalf("admin IDs: got %v, want %v", cfg.AdminTelegramIDs, tt.want)
			}
		})
	}
}

func TestLoadRejectsInvalidAdminTelegramIDsWithoutExposingInput(t *testing.T) {
	for _, value := range []string{"0", "-1", "@username", "101,", ",101", "101,,102", "1.5", "9223372036854775808", testToken} {
		env := validEnvironment()
		env["ADMIN_TELEGRAM_IDS"] = value
		cfg, err := fromEnvironment(env)
		if err == nil || !strings.Contains(err.Error(), "ADMIN_TELEGRAM_IDS") {
			t.Fatalf("invalid admin ID list was accepted: %v", err)
		}
		if strings.Contains(err.Error(), value) {
			t.Fatal("invalid admin ID input was exposed")
		}
		if !reflect.DeepEqual(cfg, Config{}) {
			t.Fatal("a partial configuration was returned")
		}
	}
}

func assertNoSecrets(t *testing.T, output string) {
	t.Helper()
	for _, secret := range []string{testToken, testDatabaseURL, "test-token-for-tests", "test_password"} {
		if strings.Contains(output, secret) {
			t.Fatal("a secret was exposed")
		}
	}
}
