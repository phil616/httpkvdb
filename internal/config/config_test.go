package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUsesEnvironment(t *testing.T) {
	t.Setenv("KVHTTP_ADDR", "127.0.0.1:9999")
	t.Setenv("KVHTTP_MAX_KEY_SIZE", "123")
	cfg := Load()
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
