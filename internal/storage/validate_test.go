package storage

import "testing"

func TestValidateKeyAndValueType(t *testing.T) {
	if err := ValidateKey("abc/def", 10); err != nil {
		t.Fatalf("expected key valid: %v", err)
	}
	if err := ValidateKey("", 10); err == nil {
		t.Fatalf("expected empty key invalid")
	}
	if got := ValueType("application/json; charset=utf-8"); got != "json" {
		t.Fatalf("value type = %s", got)
	}
	if got := ValueType("image/png"); got != "binary" {
		t.Fatalf("unknown content type should be binary, got %s", got)
	}
	if err := ValidateValue("application/json", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("valid json rejected: %v", err)
	}
	if err := ValidateValue("application/json", []byte(`{"bad"`)); err != ErrInvalidJSON {
		t.Fatalf("expected invalid json, got %v", err)
	}
	if err := ValidateUserspaceID("alice"); err != nil {
		t.Fatalf("expected userspace valid: %v", err)
	}
	if err := ValidateUserspaceID("../alice"); err == nil {
		t.Fatalf("expected path-like userspace invalid")
	}
}

func TestStorePersistsUserspaceIsolation(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("a", "same", []byte("one"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("b", "same", []byte("two"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ra, err := reopened.Get("a", "same")
	if err != nil || string(ra.Value) != "one" {
		t.Fatalf("userspace a mismatch: %q %v", string(ra.Value), err)
	}
	rb, err := reopened.Get("b", "same")
	if err != nil || string(rb.Value) != "two" {
		t.Fatalf("userspace b mismatch: %q %v", string(rb.Value), err)
	}
}
