package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Addr                 string
	StoragePath          string
	CORSAllowedOrigins   string
	MaxKeySize           int
	MaxValueSize         int64
	MaxTxOps             int
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

func Load() Config {
	values := map[string]string{}
	for _, key := range knownKeys {
		if value := os.Getenv(key); value != "" {
			values[key] = value
		}
	}
	return fromValues(values)
}

func LoadFromFile(path string) (Config, error) {
	values, err := readEnvFile(path)
	if err != nil {
		return Config{}, err
	}
	return fromValues(values), nil
}

var knownKeys = []string{
	"KVHTTP_ADDR",
	"KVHTTP_STORAGE_PATH",
	"KVHTTP_CORS_ALLOWED_ORIGINS",
	"KVHTTP_MAX_KEY_SIZE",
	"KVHTTP_MAX_VALUE_SIZE",
	"KVHTTP_MAX_TX_OPS",
	"KVHTTP_DEFAULT_TX_TIMEOUT_MS",
	"KVHTTP_MAX_TX_TIMEOUT_MS",
	"KVHTTP_TX_CLEAN_INTERVAL_MS",
	"KVHTTP_TX_RESULT_RETENTION_MS",
	"KVHTTP_AUTH_CACHE_TTL_MS",
	"KVHTTP_AUTH_CACHE_MAX_ENTRIES",
	"KVHTTP_BOOTSTRAP_USER_ID",
	"KVHTTP_BOOTSTRAP_USERSPACE_ID",
	"KVHTTP_BOOTSTRAP_API_KEY",
	"KVHTTP_API_KEY_PEPPER",
	"KVHTTP_JWT_SECRET",
	"KVHTTP_JWT_ISSUER",
	"KVHTTP_JWT_AUDIENCE",
}

func fromValues(values map[string]string) Config {
	return Config{
		Addr:                 value(values, "KVHTTP_ADDR", "0.0.0.0:8080"),
		StoragePath:          value(values, "KVHTTP_STORAGE_PATH", "./data"),
		CORSAllowedOrigins:   value(values, "KVHTTP_CORS_ALLOWED_ORIGINS", "http://127.0.0.1:5173,http://localhost:5173"),
		MaxKeySize:           valueInt(values, "KVHTTP_MAX_KEY_SIZE", 4096),
		MaxValueSize:         int64(valueInt(values, "KVHTTP_MAX_VALUE_SIZE", 104857600)),
		MaxTxOps:             valueInt(values, "KVHTTP_MAX_TX_OPS", 1000),
		DefaultTxTimeoutMS:   valueInt(values, "KVHTTP_DEFAULT_TX_TIMEOUT_MS", 30000),
		MaxTxTimeoutMS:       valueInt(values, "KVHTTP_MAX_TX_TIMEOUT_MS", 300000),
		TxCleanIntervalMS:    valueInt(values, "KVHTTP_TX_CLEAN_INTERVAL_MS", 5000),
		TxResultRetentionMS:  valueInt(values, "KVHTTP_TX_RESULT_RETENTION_MS", 3600000),
		AuthCacheTTLMS:       valueInt(values, "KVHTTP_AUTH_CACHE_TTL_MS", 60000),
		AuthCacheMaxEntries:  valueInt(values, "KVHTTP_AUTH_CACHE_MAX_ENTRIES", 10000),
		BootstrapUserID:      value(values, "KVHTTP_BOOTSTRAP_USER_ID", "admin"),
		BootstrapUserspaceID: value(values, "KVHTTP_BOOTSTRAP_USERSPACE_ID", "admin_space"),
		BootstrapAPIKey:      value(values, "KVHTTP_BOOTSTRAP_API_KEY", "dev-secret-key"),
		APIKeyPepper:         value(values, "KVHTTP_API_KEY_PEPPER", ""),
		JWTSecret:            value(values, "KVHTTP_JWT_SECRET", "dev-jwt-secret"),
		JWTIssuer:            value(values, "KVHTTP_JWT_ISSUER", ""),
		JWTAudience:          value(values, "KVHTTP_JWT_AUDIENCE", ""),
	}
}

func value(values map[string]string, k, def string) string {
	if v := values[k]; v != "" {
		return v
	}
	return def
}

func valueInt(values map[string]string, k string, def int) int {
	v := values[k]
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
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
		values[key] = unquote(rawValue)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func unquote(value string) string {
	if len(value) >= 2 {
		first := value[0]
		last := value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			unquoted, err := strconv.Unquote(value)
			if err == nil {
				return unquoted
			}
			return value[1 : len(value)-1]
		}
	}
	return value
}
