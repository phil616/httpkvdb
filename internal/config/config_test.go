package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUsesEnvironment(t *testing.T) {
	t.Setenv("KVHTTP_ADDR", "127.0.0.1:9999")
	t.Setenv("KVHTTP_MAX_KEY_SIZE", "123")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9999" {
		t.Fatalf("addr = %q", cfg.Addr)
	}
	if cfg.MaxKeySize != 123 {
		t.Fatalf("max key size = %d", cfg.MaxKeySize)
	}
}

func TestLoadFromFileUsesFileAndDefaultsNotEnvironment(t *testing.T) {
	t.Setenv("KVHTTP_ADDR", "127.0.0.1:9999")
	path := filepath.Join(t.TempDir(), ".env.local")
	content := `
# local config
export KVHTTP_STORAGE_PATH = "./local data"
KVHTTP_BOOTSTRAP_API_KEY='file-secret'
KVHTTP_API_KEY_PEPPER="file-pepper"
KVHTTP_MAX_TX_OPS=42
KVHTTP_CORS_ALLOWED_ORIGINS=http://127.0.0.1:5173
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "0.0.0.0:8080" {
		t.Fatalf("file load should not read env addr, got %q", cfg.Addr)
	}
	if cfg.StoragePath != "./local data" {
		t.Fatalf("storage path = %q", cfg.StoragePath)
	}
	if cfg.BootstrapAPIKey != "file-secret" {
		t.Fatalf("bootstrap key was not read from file")
	}
	if cfg.APIKeyPepper != "file-pepper" {
		t.Fatalf("api key pepper was not read from file")
	}
	if cfg.MaxTxOps != 42 {
		t.Fatalf("max tx ops = %d", cfg.MaxTxOps)
	}
}

func TestLoadFromFileRejectsInvalidLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(path, []byte("not-a-pair\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromFile(path); err == nil {
		t.Fatalf("expected invalid config line error")
	}
}

func TestImportLimitIsIndependentOfValueLimit(t *testing.T) {
	t.Setenv("KVHTTP_MAX_VALUE_SIZE", "1024")
	t.Setenv("KVHTTP_MAX_IMPORT_SIZE", "8192")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxValueSize != 1024 || cfg.MaxImportSize != 8192 {
		t.Fatal("import and value limits must be independent")
	}
	if got := defaults().MaxImportSize; got != 1<<30 {
		t.Fatalf("default import limit = %d", got)
	}
}

func TestRejectsInvalidConfigurationWithoutEchoingValues(t *testing.T) {
	for _, entry := range []struct{ key, value string }{
		{"KVHTTP_MAX_KEY_SIZE", "32769"}, {"KVHTTP_MAX_VALUE_SIZE", "0"}, {"KVHTTP_MAX_IMPORT_SIZE", "-1"},
		{"KVHTTP_MAX_TX_BODY_SIZE", "not-a-number"}, {"KVHTTP_TX_CLEAN_INTERVAL_MS", "0"},
		{"KVHTTP_MAX_TX_TIMEOUT_MS", "9223372036854775807"}, {"KVHTTP_MAX_TX_TIMEOUT_MS", "1"},
		{"KVHTTP_AUTH_CACHE_MAX_ENTRIES", "-1"}, {"KVHTTP_UNKNOWN_KEY", "must-not-appear-in-error"},
	} {
		_, err := loadValues(map[string]string{entry.key: entry.value})
		if err == nil {
			t.Fatalf("accepted invalid config %s", entry.key)
		}
		if strings.Contains(err.Error(), "must-not-appear-in-error") {
			t.Fatal("config value leaked")
		}
	}
}

func TestConfigFileRejectsAmbiguousSyntax(t *testing.T) {
	for _, content := range []string{"KVHTTP_ADDR=first\nKVHTTP_ADDR=second\n", "KVHTTP_ADDR=\"unterminated\n", "KVHTTP_UNKNOWN=secret\n"} {
		path := filepath.Join(t.TempDir(), "config.env")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFromFile(path); err == nil {
			t.Fatal("ambiguous config accepted")
		}
	}
}

func TestExampleDefinesAllSupportedConfigurationKeys(t *testing.T) {
	values, err := readEnvFile(filepath.Join("..", "..", "configs", "kvhttpd.env.example"))
	if err != nil {
		t.Fatal(err)
	}
	for key := range configFields(&Config{}) {
		if _, ok := values[key]; !ok {
			t.Fatalf("example missing %s", key)
		}
	}
	if _, err := loadValues(values); err != nil {
		t.Fatal("documented configuration is invalid", err)
	}
}
