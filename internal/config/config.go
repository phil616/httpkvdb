package config

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr                 string
	StoragePath          string
	CORSAllowedOrigins   string
	MaxKeySize           int
	MaxValueSize         int64
	MaxImportSize        int64
	MaxTxOps             int
	MaxTxBodySize        int64
	DefaultTxTimeoutMS   int
	MaxTxTimeoutMS       int
	TxCleanIntervalMS    int
	TxResultRetentionMS  int
	AuthCacheTTLMS       int
	AuthCacheMaxEntries  int
	BootstrapUserID      string
	BootstrapUserspaceID string
	BootstrapAPIKey      string
	APIKeyPepper         string
	JWTSecret            string
	JWTIssuer            string
	JWTAudience          string
}

func Load() (Config, error) {
	values := map[string]string{}
	for _, assignment := range os.Environ() {
		key, value, _ := strings.Cut(assignment, "=")
		if strings.HasPrefix(key, "KVHTTP_") {
			values[key] = value
		}
	}
	return loadValues(values)
}

func LoadFromFile(path string) (Config, error) {
	values, err := readEnvFile(path)
	if err != nil {
		return Config{}, err
	}
	return loadValues(values)
}

func defaults() Config {
	return Config{
		Addr:                 "0.0.0.0:8080",
		StoragePath:          "./data",
		CORSAllowedOrigins:   "http://127.0.0.1:5173,http://localhost:5173",
		MaxKeySize:           4096,
		MaxValueSize:         104857600,
		MaxImportSize:        1073741824,
		MaxTxOps:             1000,
		MaxTxBodySize:        268435456,
		DefaultTxTimeoutMS:   30000,
		MaxTxTimeoutMS:       300000,
		TxCleanIntervalMS:    5000,
		TxResultRetentionMS:  3600000,
		AuthCacheTTLMS:       60000,
		AuthCacheMaxEntries:  10000,
		BootstrapUserID:      "admin",
		BootstrapUserspaceID: "admin_space",
		BootstrapAPIKey:      "dev-secret-key",
		APIKeyPepper:         "",
		JWTSecret:            "dev-jwt-secret",
		JWTIssuer:            "",
		JWTAudience:          "",
	}
}

// Field pointers keep parsing types explicit and avoid inferring them from key names.
func configFields(c *Config) map[string]any {
	return map[string]any{
		"KVHTTP_ADDR":                   &c.Addr,
		"KVHTTP_STORAGE_PATH":           &c.StoragePath,
		"KVHTTP_CORS_ALLOWED_ORIGINS":   &c.CORSAllowedOrigins,
		"KVHTTP_MAX_KEY_SIZE":           &c.MaxKeySize,
		"KVHTTP_MAX_VALUE_SIZE":         &c.MaxValueSize,
		"KVHTTP_MAX_IMPORT_SIZE":        &c.MaxImportSize,
		"KVHTTP_MAX_TX_OPS":             &c.MaxTxOps,
		"KVHTTP_MAX_TX_BODY_SIZE":       &c.MaxTxBodySize,
		"KVHTTP_DEFAULT_TX_TIMEOUT_MS":  &c.DefaultTxTimeoutMS,
		"KVHTTP_MAX_TX_TIMEOUT_MS":      &c.MaxTxTimeoutMS,
		"KVHTTP_TX_CLEAN_INTERVAL_MS":   &c.TxCleanIntervalMS,
		"KVHTTP_TX_RESULT_RETENTION_MS": &c.TxResultRetentionMS,
		"KVHTTP_AUTH_CACHE_TTL_MS":      &c.AuthCacheTTLMS,
		"KVHTTP_AUTH_CACHE_MAX_ENTRIES": &c.AuthCacheMaxEntries,
		"KVHTTP_BOOTSTRAP_USER_ID":      &c.BootstrapUserID,
		"KVHTTP_BOOTSTRAP_USERSPACE_ID": &c.BootstrapUserspaceID,
		"KVHTTP_BOOTSTRAP_API_KEY":      &c.BootstrapAPIKey,
		"KVHTTP_API_KEY_PEPPER":         &c.APIKeyPepper,
		"KVHTTP_JWT_SECRET":             &c.JWTSecret,
		"KVHTTP_JWT_ISSUER":             &c.JWTIssuer,
		"KVHTTP_JWT_AUDIENCE":           &c.JWTAudience,
	}
}

func loadValues(values map[string]string) (Config, error) {
	cfg := defaults()
	fields := configFields(&cfg)
	for key, raw := range values {
		field, known := fields[key]
		if !known {
			return Config{}, fmt.Errorf("unknown config key %q", key)
		}
		if raw == "" {
			continue
		} // Empty values use the documented default, including empty optional fields.
		switch target := field.(type) {
		case *string:
			*target = raw
		case *int:
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				return Config{}, fmt.Errorf("%s must be a decimal integer", key)
			}
			*target = parsed
		case *int64:
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return Config{}, fmt.Errorf("%s must be a decimal integer", key)
			}
			*target = parsed
		}
	}
	return cfg, cfg.Validate()
}

// Validate prevents invalid limits and duration overflow from reaching storage or timers.
func (c Config) Validate() error {
	if c.MaxKeySize <= 0 || c.MaxKeySize > 32768 {
		return fmt.Errorf("KVHTTP_MAX_KEY_SIZE must be between 1 and 32768 bytes")
	}
	for _, item := range []struct {
		name  string
		value int64
	}{
		{"KVHTTP_MAX_VALUE_SIZE", c.MaxValueSize}, {"KVHTTP_MAX_IMPORT_SIZE", c.MaxImportSize},
		{"KVHTTP_MAX_TX_BODY_SIZE", c.MaxTxBodySize}, {"KVHTTP_MAX_TX_OPS", int64(c.MaxTxOps)},
		{"KVHTTP_AUTH_CACHE_MAX_ENTRIES", int64(c.AuthCacheMaxEntries)},
	} {
		if item.value <= 0 {
			return fmt.Errorf("%s must be positive", item.name)
		}
	}
	for _, item := range []struct {
		name  string
		value int
	}{
		{"KVHTTP_DEFAULT_TX_TIMEOUT_MS", c.DefaultTxTimeoutMS}, {"KVHTTP_MAX_TX_TIMEOUT_MS", c.MaxTxTimeoutMS},
		{"KVHTTP_TX_CLEAN_INTERVAL_MS", c.TxCleanIntervalMS}, {"KVHTTP_TX_RESULT_RETENTION_MS", c.TxResultRetentionMS},
		{"KVHTTP_AUTH_CACHE_TTL_MS", c.AuthCacheTTLMS},
	} {
		if item.value <= 0 || int64(item.value) > math.MaxInt64/int64(time.Millisecond) {
			return fmt.Errorf("%s must be a positive millisecond duration within time.Duration range", item.name)
		}
	}
	if c.DefaultTxTimeoutMS > c.MaxTxTimeoutMS {
		return fmt.Errorf("KVHTTP_DEFAULT_TX_TIMEOUT_MS must not exceed KVHTTP_MAX_TX_TIMEOUT_MS")
	}
	return nil
}

func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid config line %d", lineNo)
		}
		key = strings.TrimSpace(key)
		rawValue = strings.TrimSpace(rawValue)
		if key == "" {
			return nil, fmt.Errorf("invalid config line %d", lineNo)
		}
		if _, known := configFields(&Config{})[key]; !known {
			return nil, fmt.Errorf("unknown config key on line %d", lineNo)
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("duplicate config key on line %d", lineNo)
		}
		parsed, err := unquote(rawValue)
		if err != nil {
			return nil, fmt.Errorf("invalid quoted config value on line %d", lineNo)
		}
		values[key] = parsed
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func unquote(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", fmt.Errorf("unmatched single quote")
		}
		return value[1 : len(value)-1], nil
	}
	if value[0] == '"' {
		return strconv.Unquote(value)
	}
	return value, nil
}
