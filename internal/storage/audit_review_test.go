package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
	"httpkvdb/internal/model"
)

func openTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestFailedAtomicCommitDoesNotPublishOrAdvanceVersion(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	version, err := s.Put("u", "key", []byte("old"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.BeginAtomic()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Put("u", "key", []byte("new"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	// A result-persistence failure must roll back the associated KV changes too.
	tx.SaveTx(model.Transaction{}) // Empty transaction IDs cannot be persisted.
	if err := tx.Commit(); !errors.Is(err, bolt.ErrKeyRequired) {
		t.Fatalf("commit error = %v", err)
	}
	record, err := s.Get("u", "key")
	if err != nil || string(record.Value) != "old" || record.Version != version {
		t.Fatalf("failed commit visible: %+v %v", record, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, dir)
	record, err = s.Get("u", "key")
	if err != nil || string(record.Value) != "old" {
		t.Fatalf("failed commit persisted: %+v %v", record, err)
	}
	next, err := s.Put("u", "next", nil, "application/octet-stream")
	if err != nil || next != version+1 {
		t.Fatalf("version advanced on failure: %d %v", next, err)
	}
}

func TestLongAndCollidingKeysPersistWithoutFilesystemNames(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	keys := []string{strings.Repeat("a", 4096), "a/b", "k_YS9i", ".", "..", "a.txt", "100%", "a%2Fb", "中文/路径"}
	for _, key := range keys {
		if _, err := s.Put("u", key, []byte(key), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put("other", "good", []byte("ok"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, dir)
	for _, key := range keys {
		record, err := s.Get("u", key)
		if err != nil || string(record.Value) != key {
			t.Fatalf("key round trip failed: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "userspaces")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected file mirror: %v", err)
	}
}

func TestAtomicCommitPreservesConcurrentTransactionState(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	tx, err := s.BeginAtomic()
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(started); done <- s.SaveTx(model.Transaction{TxID: "concurrent"}) }()
	<-started
	if _, err := tx.Put("u", "key", []byte("x"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTx("concurrent"); err != nil {
		t.Fatal("transaction state lost", err)
	}
}

func TestBootstrapDoesNotRestoreRotatedOrDeletedCredentials(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	if err := s.Bootstrap("admin", "admin_space", "oldhash"); err != nil {
		t.Fatal(err)
	}
	p, err := s.ReplaceUserspaceAPIKeyHash("admin_space", "newhash")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Roles) != 1 || p.Roles[0] != "admin" {
		t.Fatalf("admin role lost: %v", p.Roles)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, dir)
	if err := s.Bootstrap("admin", "admin_space", "oldhash"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ResolveAPIKeyHash("oldhash"); ok {
		t.Fatal("old key restored")
	}
	if _, ok := s.ResolveAPIKeyHash("newhash"); !ok {
		t.Fatal("new key missing")
	}
	if err := s.DeleteUserspace("admin_space"); err != nil {
		t.Fatal(err)
	}
	if err := s.Bootstrap("admin", "admin_space", "oldhash"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ResolveAPIKeyHash("oldhash"); ok {
		t.Fatal("deleted key restored")
	}
}

func TestMultipleOpenRejectedUntilClose(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	second, err := Open(dir)
	if second != nil {
		_ = second.Close()
		t.Fatal("second writer allowed")
	}
	if !errors.Is(err, bolt.ErrTimeout) {
		t.Fatalf("second open = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	openTestStore(t, dir)
}

func TestInvalidDatabasesRejected(t *testing.T) {
	for _, data := range []string{"", "null", "{}", "{", `{"userspaces":null}`} {
		t.Run("legacy_"+data, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "httpkvdb.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if s, err := Open(dir); err == nil {
					_ = s.Close()
					t.Fatal("invalid legacy database accepted")
				}
			}
		})
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "httpkvdb.db"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(dir); err == nil {
		_ = s.Close()
		t.Fatal("empty native database accepted")
	}
}

func TestLegacyMigrationPreservesDataAuthAndResultsOnce(t *testing.T) {
	dir := t.TempDir()
	value := []byte{0, 255, 1}
	now := time.Now().UTC().Truncate(time.Second)
	record := model.KVRecord{UserspaceID: "u", Key: "a/b", Value: value, ContentType: "application/octet-stream", ValueType: "binary", Version: 7, Checksum: Checksum(value), CreatedAt: now, UpdatedAt: now}
	p := model.Principal{UserID: "user", UserspaceID: "u", Roles: []string{"admin"}}
	result := &model.TxResult{TxID: "done", Status: model.TxCommitted, Results: []model.TxOperationResult{{Seq: 1, Op: "PUT", Key: "a/b", Status: 200, Version: 7}}}
	old := snapshot{Version: 7, Userspaces: map[string]map[string]model.KVRecord{"u": {"a/b": record}}, APIKeys: map[string]APIKeyRecord{"rotated": {Hash: "rotated", Principal: p, Active: true}}, JWTSubjects: map[string]JWTSubjectRecord{"user": {Subject: "user", Principal: p, Active: true}}, Txs: map[string]model.Transaction{"done": {TxID: "done", UserspaceID: "u", Status: model.TxCommitted, Result: result}, "pending": {TxID: "pending", UserspaceID: "u", Ops: map[int]model.TxOperation{1: {Seq: 1, Body: []byte("pending")}}}}}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "httpkvdb.json")
	if err := os.WriteFile(legacy, data, 0600); err != nil {
		t.Fatal(err)
	}
	s := openTestStore(t, dir)
	got, err := s.Get("u", "a/b")
	if err != nil || !bytes.Equal(got.Value, value) || got.Version != 7 || !got.CreatedAt.Equal(now) || got.Checksum != record.Checksum {
		t.Fatalf("migration mismatch: %+v %v", got, err)
	}
	if principal, ok := s.ResolveAPIKeyHash("rotated"); !ok || principal.UserID != "user" {
		t.Fatal("APIKey lost")
	}
	if _, ok := s.ResolveJWTSubject("user"); !ok {
		t.Fatal("JWT subject lost")
	}
	done, err := s.GetTx("done")
	if err != nil || done.Result.Results[0].Version != 7 {
		t.Fatal("result lost", err)
	}
	pending, err := s.GetTx("pending")
	if err != nil || string(pending.Ops[1].Body) != "pending" {
		t.Fatal("pending fragment lost", err)
	}
	if err := s.Bootstrap("admin", "admin_space", "old"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ResolveAPIKeyHash("old"); ok {
		t.Fatal("bootstrap replaced migrated auth")
	}
	next, err := s.Put("u", "next", nil, "application/octet-stream")
	if err != nil || next != 8 {
		t.Fatalf("version mismatch: %d %v", next, err)
	}
	if kept, err := os.ReadFile(legacy); err != nil || !bytes.Equal(kept, data) {
		t.Fatal("legacy backup changed", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("corrupt obsolete backup"), 0600); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, dir)
	if _, err := s.Get("u", "next"); err != nil {
		t.Fatal("migration re-ran", err)
	}
}

func TestCommittedKVAndResultSurviveReopenTogether(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	tx, err := s.BeginAtomic()
	if err != nil {
		t.Fatal(err)
	}
	record, err := tx.Put("u", "key", []byte("value"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	tx.SaveTx(model.Transaction{TxID: "committed", Status: model.TxCommitted, Result: &model.TxResult{TxID: "committed", Status: model.TxCommitted, Results: []model.TxOperationResult{{Seq: 1, Op: "PUT", Version: record.Version}}}})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, dir)
	got, err := s.Get("u", "key")
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := s.GetTx("committed")
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Result.Results[0].Version != got.Version || string(got.Value) != "value" {
		t.Fatal("KV and transaction result diverged")
	}
}

func TestUserspaceCredentialCollisionRollsBack(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	if err := s.CreateUserspace("first", "user1", "hash1"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUserspace("second", "user2", "hash1"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate credential accepted: %v", err)
	}
	spaces, err := s.ListUserspaces()
	if err != nil || len(spaces) != 1 {
		t.Fatal("failed create left userspace", err)
	}
	if err := s.CreateUserspace("second", "user2", "hash2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceUserspaceAPIKeyHash("first", "hash2"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("rotation stole another credential: %v", err)
	}
	for hash, user := range map[string]string{"hash1": "user1", "hash2": "user2"} {
		if p, ok := s.ResolveAPIKeyHash(hash); !ok || p.UserID != user {
			t.Fatal("rejected rotation changed auth")
		}
	}
}

func TestNativeDatabaseWithMissingBucketsIsRejected(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	if err := s.db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket(apiKeysBucket) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(); err == nil {
		t.Fatal("incomplete storage reported ready")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(dir); err == nil {
		_ = reopened.Close()
		t.Fatal("missing auth data silently recreated")
	}
}
