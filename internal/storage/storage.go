package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
	"httpkvdb/internal/model"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrTxNotFound    = errors.New("tx not found")
	ErrAlreadyExists = errors.New("already exists")
	metaBucket       = []byte("meta")
	spacesBucket     = []byte("kv")
	apiKeysBucket    = []byte("system:api_keys")
	subjectsBucket   = []byte("system:jwt_subjects")
	txsBucket        = []byte("tx:states")
	schemaKey        = []byte("schema")
	bootstrapKey     = []byte("bootstrapped")
)

type APIKeyRecord struct {
	Hash      string          `json:"hash"`
	Principal model.Principal `json:"principal"`
	Active    bool            `json:"active"`
}
type JWTSubjectRecord struct {
	Subject   string          `json:"subject"`
	Principal model.Principal `json:"principal"`
	Active    bool            `json:"active"`
}
type UserspaceInfo struct {
	UserspaceID string `json:"userspace_id"`
	UserID      string `json:"user_id"`
	KeyCount    int    `json:"key_count"`
	APIKeyCount int    `json:"api_key_count"`
}

// snapshot is the legacy JSON format, read only during the first migration.
type snapshot struct {
	Version     uint64                               `json:"version"`
	Userspaces  map[string]map[string]model.KVRecord `json:"userspaces"`
	APIKeys     map[string]APIKeyRecord              `json:"api_keys"`
	JWTSubjects map[string]JWTSubjectRecord          `json:"jwt_subjects"`
	Txs         map[string]model.Transaction         `json:"txs"`
}

type Store struct{ db *bolt.DB }

// Open takes an exclusive process lock. Keys and userspaces are database keys,
// never filesystem paths. Default bbolt commits synchronously flush to disk.
func Open(path string) (*Store, error) {
	dir, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "httpkvdb.db")
	if info, err := os.Stat(dbPath); err == nil && info.Size() == 0 {
		return nil, errors.New("empty database file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := db.Update(func(tx *bolt.Tx) error { return initialize(tx, dir) }); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Flush the directory entry of the database as well as its contents.
	// Windows does not support syncing directories through os.File.Sync.
	if runtime.GOOS != "windows" {
		directory, err := os.Open(dir)
		if err == nil {
			err = directory.Sync()
			closeErr := directory.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func initialize(tx *bolt.Tx, dir string) error {
	names := [][]byte{metaBucket, spacesBucket, apiKeysBucket, subjectsBucket, txsBucket}
	if meta := tx.Bucket(metaBucket); meta != nil && meta.Get(schemaKey) != nil {
		if string(meta.Get(schemaKey)) != "1" {
			return errors.New("unsupported storage schema")
		}
		for _, name := range names {
			if tx.Bucket(name) == nil {
				return errors.New("incomplete database")
			}
		}
		return nil
	}
	// A prior failed initialization leaves an empty root, since initialization is
	// atomic. Nonempty unmarked files cannot safely be treated as a new store.
	cursor := tx.Cursor()
	if first, _ := cursor.First(); first != nil {
		return errors.New("unrecognized database schema")
	}
	for _, name := range names {
		if _, err := tx.CreateBucket(name); err != nil {
			return err
		}
	}
	meta := tx.Bucket(metaBucket)
	data, err := os.ReadFile(filepath.Join(dir, "httpkvdb.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		var old snapshot
		// Empty/truncated legacy files must not silently become an empty database.
		if err := json.Unmarshal(data, &old); err != nil {
			return fmt.Errorf("invalid legacy database: %w", err)
		}
		if len(data) == 0 || string(data) == "null" || old.Userspaces == nil || old.APIKeys == nil || old.JWTSubjects == nil || old.Txs == nil {
			return errors.New("incomplete legacy database")
		}
		for space, records := range old.Userspaces {
			b, err := tx.Bucket(spacesBucket).CreateBucketIfNotExists([]byte(space))
			if err != nil {
				return err
			}
			for key, record := range records {
				if err := putJSON(b, key, record); err != nil {
					return err
				}
			}
		}
		for key, record := range old.APIKeys {
			if err := putJSON(tx.Bucket(apiKeysBucket), key, record); err != nil {
				return err
			}
		}
		for key, record := range old.JWTSubjects {
			if err := putJSON(tx.Bucket(subjectsBucket), key, record); err != nil {
				return err
			}
		}
		for key, record := range old.Txs {
			if err := putJSON(tx.Bucket(txsBucket), key, record); err != nil {
				return err
			}
		}
		if err := meta.SetSequence(old.Version); err != nil {
			return err
		}
	}
	// Existing credentials are authoritative, including keys rotated before migration.
	if tx.Bucket(apiKeysBucket).Stats().KeyN > 0 || tx.Bucket(subjectsBucket).Stats().KeyN > 0 {
		if err := meta.Put(bootstrapKey, []byte{1}); err != nil {
			return err
		}
	}
	return meta.Put(schemaKey, []byte("1"))
}

func putJSON(b *bolt.Bucket, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return b.Put([]byte(key), data)
}

func (s *Store) Bootstrap(userID, userspaceID, apiKeyHash string) error {
	if err := ValidateUserspaceID(userspaceID); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		if meta.Get(bootstrapKey) != nil {
			return nil
		}
		if tx.Bucket(apiKeysBucket).Stats().KeyN == 0 && tx.Bucket(subjectsBucket).Stats().KeyN == 0 {
			if _, err := tx.Bucket(spacesBucket).CreateBucketIfNotExists([]byte(userspaceID)); err != nil {
				return err
			}
			p := model.Principal{UserID: userID, UserspaceID: userspaceID, Roles: []string{"admin"}, AuthMethod: "apikey"}
			if apiKeyHash != "" {
				if err := putJSON(tx.Bucket(apiKeysBucket), apiKeyHash, APIKeyRecord{Hash: apiKeyHash, Principal: p, Active: true}); err != nil {
					return err
				}
			}
			p.AuthMethod = "jwt"
			if err := putJSON(tx.Bucket(subjectsBucket), userID, JWTSubjectRecord{Subject: userID, Principal: p, Active: true}); err != nil {
				return err
			}
		}
		return meta.Put(bootstrapKey, []byte{1})
	})
}

func (s *Store) UpsertAPIKeyHash(hash string, p model.Principal) error {
	p.AuthMethod = "apikey"
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(apiKeysBucket), hash, APIKeyRecord{Hash: hash, Principal: p, Active: true})
	})
}
func (s *Store) UpsertJWTSubject(subject string, p model.Principal) error {
	p.AuthMethod = "jwt"
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(subjectsBucket), subject, JWTSubjectRecord{Subject: subject, Principal: p, Active: true})
	})
}

func userspaceInfos(tx *bolt.Tx) (map[string]UserspaceInfo, error) {
	infos := map[string]UserspaceInfo{}
	err := tx.Bucket(spacesBucket).ForEach(func(k, v []byte) error {
		if v == nil {
			infos[string(k)] = UserspaceInfo{UserspaceID: string(k), UserID: string(k), KeyCount: tx.Bucket(spacesBucket).Bucket(k).Stats().KeyN}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = tx.Bucket(subjectsBucket).ForEach(func(_, v []byte) error {
		var record JWTSubjectRecord
		if err := json.Unmarshal(v, &record); err != nil {
			return err
		}
		p := record.Principal
		info := infos[p.UserspaceID]
		info.UserspaceID = p.UserspaceID
		if info.UserID == "" || info.UserID == info.UserspaceID {
			info.UserID = p.UserID
		}
		infos[p.UserspaceID] = info
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = tx.Bucket(apiKeysBucket).ForEach(func(_, v []byte) error {
		var record APIKeyRecord
		if err := json.Unmarshal(v, &record); err != nil {
			return err
		}
		p := record.Principal
		info := infos[p.UserspaceID]
		info.UserspaceID = p.UserspaceID
		if info.UserID == "" || info.UserID == info.UserspaceID {
			info.UserID = p.UserID
		}
		if record.Active {
			info.APIKeyCount++
		}
		infos[p.UserspaceID] = info
		return nil
	})
	return infos, err
}
func userspaceExists(tx *bolt.Tx, id string) (bool, error) {
	if tx.Bucket(spacesBucket).Bucket([]byte(id)) != nil {
		return true, nil
	}
	infos, err := userspaceInfos(tx)
	_, ok := infos[id]
	return ok, err
}
func (s *Store) CreateUserspace(userspaceID, userID, apiKeyHash string) error {
	if err := ValidateUserspaceID(userspaceID); err != nil {
		return err
	}
	if userID == "" {
		userID = userspaceID
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		exists, err := userspaceExists(tx, userspaceID)
		if err != nil {
			return err
		}
		if exists {
			return ErrAlreadyExists
		}
		if _, err := tx.Bucket(spacesBucket).CreateBucket([]byte(userspaceID)); err != nil {
			return err
		}
		if apiKeyHash == "" {
			return nil
		}
		if tx.Bucket(apiKeysBucket).Get([]byte(apiKeyHash)) != nil {
			return ErrAlreadyExists
		}
		p := model.Principal{UserID: userID, UserspaceID: userspaceID, Roles: []string{"user"}, AuthMethod: "apikey"}
		return putJSON(tx.Bucket(apiKeysBucket), apiKeyHash, APIKeyRecord{Hash: apiKeyHash, Principal: p, Active: true})
	})
}
func (s *Store) ListUserspaces() ([]UserspaceInfo, error) {
	out := []UserspaceInfo{}
	err := s.db.View(func(tx *bolt.Tx) error {
		infos, err := userspaceInfos(tx)
		if err != nil {
			return err
		}
		for _, info := range infos {
			out = append(out, info)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].UserspaceID < out[j].UserspaceID })
	return out, err
}
func deleteMatching(b *bolt.Bucket, match func([]byte) (bool, error)) error {
	// Collect keys before modifying the bucket during iteration.
	var keys [][]byte
	if err := b.ForEach(func(k, v []byte) error {
		ok, err := match(v)
		if ok {
			keys = append(keys, append([]byte(nil), k...))
		}
		return err
	}); err != nil {
		return err
	}
	for _, k := range keys {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) DeleteUserspace(userspaceID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		exists, err := userspaceExists(tx, userspaceID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		spaces := tx.Bucket(spacesBucket)
		if spaces.Bucket([]byte(userspaceID)) != nil {
			if err := spaces.DeleteBucket([]byte(userspaceID)); err != nil {
				return err
			}
		}
		// All persisted objects expose their ownership through userspace_id.
		for _, name := range [][]byte{apiKeysBucket, subjectsBucket} {
			if err := deleteMatching(tx.Bucket(name), func(v []byte) (bool, error) {
				var record struct {
					Principal model.Principal `json:"principal"`
				}
				err := json.Unmarshal(v, &record)
				return record.Principal.UserspaceID == userspaceID, err
			}); err != nil {
				return err
			}
		}
		return deleteMatching(tx.Bucket(txsBucket), func(v []byte) (bool, error) {
			var record model.Transaction
			err := json.Unmarshal(v, &record)
			return record.UserspaceID == userspaceID, err
		})
	})
}
func (s *Store) ReplaceUserspaceAPIKeyHash(userspaceID, hash string) (model.Principal, error) {
	var p model.Principal
	err := s.db.Update(func(tx *bolt.Tx) error {
		exists, err := userspaceExists(tx, userspaceID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if tx.Bucket(apiKeysBucket).Get([]byte(hash)) != nil {
			return ErrAlreadyExists
		}
		p = model.Principal{UserID: userspaceID, UserspaceID: userspaceID, Roles: []string{"user"}, AuthMethod: "apikey"}
		found := false
		if err := deleteMatching(tx.Bucket(apiKeysBucket), func(v []byte) (bool, error) {
			var record APIKeyRecord
			err := json.Unmarshal(v, &record)
			match := record.Principal.UserspaceID == userspaceID
			if match && !found {
				p = record.Principal
				found = true
			}
			return match, err
		}); err != nil {
			return err
		}
		if !found {
			if err := tx.Bucket(subjectsBucket).ForEach(func(_, v []byte) error {
				var record JWTSubjectRecord
				if err := json.Unmarshal(v, &record); err != nil {
					return err
				}
				if record.Principal.UserspaceID == userspaceID && !found {
					p = record.Principal
					found = true
				}
				return nil
			}); err != nil {
				return err
			}
		}
		p.AuthMethod = "apikey"
		return putJSON(tx.Bucket(apiKeysBucket), hash, APIKeyRecord{Hash: hash, Principal: p, Active: true})
	})
	if err != nil {
		return model.Principal{}, err
	}
	return p, nil
}

func getRecord(tx *bolt.Tx, userspaceID, key string) (model.KVRecord, error) {
	b := tx.Bucket(spacesBucket).Bucket([]byte(userspaceID))
	if b == nil {
		return model.KVRecord{}, ErrNotFound
	}
	data := b.Get([]byte(key))
	if data == nil {
		return model.KVRecord{}, ErrNotFound
	}
	var record model.KVRecord
	err := json.Unmarshal(data, &record)
	return record, err
}
func (s *Store) Get(userspaceID, key string) (model.KVRecord, error) {
	var record model.KVRecord
	err := s.db.View(func(tx *bolt.Tx) error { var err error; record, err = getRecord(tx, userspaceID, key); return err })
	return record, err
}
func (s *Store) Put(userspaceID, key string, value []byte, contentType string) (uint64, error) {
	var version uint64
	err := s.db.Update(func(tx *bolt.Tx) error {
		record, err := putRecord(tx, userspaceID, key, value, contentType)
		version = record.Version
		return err
	})
	if err != nil {
		return 0, err
	}
	return version, nil
}
func putRecord(tx *bolt.Tx, userspaceID, key string, value []byte, contentType string) (model.KVRecord, error) {
	b, err := tx.Bucket(spacesBucket).CreateBucketIfNotExists([]byte(userspaceID))
	if err != nil {
		return model.KVRecord{}, err
	}
	now := time.Now().UTC()
	created := now
	old, err := getRecord(tx, userspaceID, key)
	if err == nil {
		created = old.CreatedAt
	} else if !errors.Is(err, ErrNotFound) {
		return model.KVRecord{}, err
	}
	version, err := tx.Bucket(metaBucket).NextSequence()
	if err != nil {
		return model.KVRecord{}, err
	}
	record := model.KVRecord{UserspaceID: userspaceID, Key: key, Value: value, ContentType: contentType, ValueType: ValueType(contentType), Version: version, CreatedAt: created, UpdatedAt: now, Checksum: Checksum(value)}
	if err := putJSON(b, key, record); err != nil {
		return model.KVRecord{}, err
	}
	record.Value = append([]byte(nil), value...)
	return record, nil
}
func deleteRecord(tx *bolt.Tx, userspaceID, key string) (uint64, error) {
	if _, err := getRecord(tx, userspaceID, key); err != nil {
		return 0, err
	}
	version, err := tx.Bucket(metaBucket).NextSequence()
	if err != nil {
		return 0, err
	}
	return version, tx.Bucket(spacesBucket).Bucket([]byte(userspaceID)).Delete([]byte(key))
}
func (s *Store) Delete(userspaceID, key string) (uint64, error) {
	var version uint64
	err := s.db.Update(func(tx *bolt.Tx) error { var err error; version, err = deleteRecord(tx, userspaceID, key); return err })
	if err != nil {
		return 0, err
	}
	return version, nil
}
func (s *Store) Exists(userspaceID, key string) bool {
	_, err := s.Get(userspaceID, key)
	return err == nil
}
func recordsIn(tx *bolt.Tx, userspaceID string, metadataOnly bool) ([]model.KVRecord, error) {
	records := []model.KVRecord{}
	b := tx.Bucket(spacesBucket).Bucket([]byte(userspaceID))
	if b == nil {
		return records, nil
	}
	err := b.ForEach(func(_, v []byte) error {
		var record model.KVRecord
		if err := json.Unmarshal(v, &record); err != nil {
			return err
		}
		if metadataOnly {
			record.Value = nil
		}
		records = append(records, record)
		return nil
	})
	return records, err
}
func (s *Store) ExportUserspace(userspaceID string) ([]model.KVRecord, error) {
	var records []model.KVRecord
	err := s.db.View(func(tx *bolt.Tx) error { var err error; records, err = recordsIn(tx, userspaceID, false); return err })
	return records, err
}
func (s *Store) ListUserspaceKeys(userspaceID string) ([]model.KVRecord, error) {
	var records []model.KVRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		exists, err := userspaceExists(tx, userspaceID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		records, err = recordsIn(tx, userspaceID, true)
		return err
	})
	return records, err
}
func (s *Store) ResolveAPIKeyHash(hash string) (model.Principal, bool) {
	var record APIKeyRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(apiKeysBucket).Get([]byte(hash))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &record)
	})
	record.Principal.AuthMethod = "apikey"
	return record.Principal, err == nil && record.Active
}
func (s *Store) ResolveJWTSubject(subject string) (model.Principal, bool) {
	var record JWTSubjectRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(subjectsBucket).Get([]byte(subject))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &record)
	})
	record.Principal.AuthMethod = "jwt"
	return record.Principal, err == nil && record.Active
}
func (s *Store) SaveTx(record model.Transaction) error {
	return s.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(txsBucket), record.TxID, record) })
}
func (s *Store) GetTx(id string) (model.Transaction, error) {
	var record model.Transaction
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(txsBucket).Get([]byte(id))
		if data == nil {
			return ErrTxNotFound
		}
		return json.Unmarshal(data, &record)
	})
	return record, err
}
func (s *Store) ListTxs() ([]model.Transaction, error) {
	records := []model.Transaction{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(txsBucket).ForEach(func(_, v []byte) error {
			var record model.Transaction
			if err := json.Unmarshal(v, &record); err != nil {
				return err
			}
			records = append(records, record)
			return nil
		})
	})
	return records, err
}
func (s *Store) Ready() error {
	return s.db.View(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{metaBucket, spacesBucket, apiKeysBucket, subjectsBucket, txsBucket} {
			if tx.Bucket(name) == nil {
				return errors.New("storage unavailable")
			}
		}
		if string(tx.Bucket(metaBucket).Get(schemaKey)) != "1" {
			return errors.New("unsupported storage schema")
		}
		return nil
	})
}

// AtomicTx is one native writable transaction; KV changes and the final result
// are committed together. Callers must commit or roll back exactly once.
type AtomicTx struct {
	tx   *bolt.Tx
	err  error
	done bool
}

func (s *Store) BeginAtomic() (*AtomicTx, error) {
	tx, err := s.db.Begin(true)
	if err != nil {
		return nil, err
	}
	return &AtomicTx{tx: tx}, nil
}
func (tx *AtomicTx) Get(userspaceID, key string) (model.KVRecord, error) {
	return getRecord(tx.tx, userspaceID, key)
}
func (tx *AtomicTx) Put(userspaceID, key string, value []byte, contentType string) (model.KVRecord, error) {
	return putRecord(tx.tx, userspaceID, key, value, contentType)
}
func (tx *AtomicTx) Delete(userspaceID, key string) (uint64, error) {
	return deleteRecord(tx.tx, userspaceID, key)
}
func (tx *AtomicTx) Exists(userspaceID, key string) bool {
	_, err := tx.Get(userspaceID, key)
	return err == nil
}
func (tx *AtomicTx) SaveTx(record model.Transaction) {
	if tx.err == nil {
		tx.err = putJSON(tx.tx.Bucket(txsBucket), record.TxID, record)
	}
}
func (tx *AtomicTx) Commit() error {
	if tx.done {
		return bolt.ErrTxClosed
	}
	tx.done = true
	if tx.err != nil {
		_ = tx.tx.Rollback()
		return tx.err
	}
	return tx.tx.Commit()
}
func (tx *AtomicTx) Rollback() {
	if !tx.done {
		tx.done = true
		_ = tx.tx.Rollback()
	}
}
func (tx *AtomicTx) ImportUserspace(userspaceID string, records []model.KVRecord, mode model.ImportMode) model.ImportResult {
	result, err := tx.importRecords(userspaceID, records, mode)
	if tx.err == nil {
		tx.err = err
	}
	return result
}
func (tx *AtomicTx) importRecords(userspaceID string, records []model.KVRecord, mode model.ImportMode) (model.ImportResult, error) {
	result := model.ImportResult{}
	if mode != model.ImportReplace && mode != model.ImportMergeOverwrite && mode != model.ImportMergeSkip {
		return result, errors.New("invalid import mode")
	}
	spaces := tx.tx.Bucket(spacesBucket)
	if mode == model.ImportReplace && spaces.Bucket([]byte(userspaceID)) != nil {
		if err := spaces.DeleteBucket([]byte(userspaceID)); err != nil {
			return result, err
		}
	}
	b, err := spaces.CreateBucketIfNotExists([]byte(userspaceID))
	if err != nil {
		return result, err
	}
	for _, record := range records {
		exists := b.Get([]byte(record.Key)) != nil
		if exists && mode == model.ImportMergeSkip {
			result.Skipped++
			continue
		}
		if exists {
			result.Replaced++
		}
		version, err := tx.tx.Bucket(metaBucket).NextSequence()
		if err != nil {
			return result, err
		}
		now := time.Now().UTC()
		record.UserspaceID = userspaceID
		record.Version = version
		if record.CreatedAt.IsZero() {
			record.CreatedAt = now
		}
		record.UpdatedAt = now
		record.Checksum = Checksum(record.Value)
		record.ValueType = ValueType(record.ContentType)
		if err := putJSON(b, record.Key, record); err != nil {
			return result, err
		}
		result.Imported++
	}
	return result, nil
}
func (s *Store) ImportUserspace(userspaceID string, records []model.KVRecord, mode model.ImportMode) (model.ImportResult, error) {
	tx, err := s.BeginAtomic()
	if err != nil {
		return model.ImportResult{}, err
	}
	defer tx.Rollback()
	result := tx.ImportUserspace(userspaceID, records, mode)
	if err := tx.Commit(); err != nil {
		return model.ImportResult{}, err
	}
	return result, nil
}
