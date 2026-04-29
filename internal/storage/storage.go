package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"httpkvdb/internal/model"
)

var (
	ErrNotFound   = errors.New("not found")
	ErrTxNotFound = errors.New("tx not found")
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

type snapshot struct {
	Version     uint64                               `json:"version"`
	Userspaces  map[string]map[string]model.KVRecord `json:"userspaces"`
	APIKeys     map[string]APIKeyRecord              `json:"api_keys"`
	JWTSubjects map[string]JWTSubjectRecord          `json:"jwt_subjects"`
	Txs         map[string]model.Transaction         `json:"txs"`
}

type Store struct {
	mu   sync.Mutex
	path string
	data snapshot
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(path, "httpkvdb.json")}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Bootstrap(userID, userspaceID, apiKeyHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	if apiKeyHash != "" {
		if _, ok := s.data.APIKeys[apiKeyHash]; !ok {
			p := model.Principal{UserID: userID, UserspaceID: userspaceID, Roles: []string{"admin"}, AuthMethod: "apikey"}
			s.data.APIKeys[apiKeyHash] = APIKeyRecord{Hash: apiKeyHash, Principal: p, Active: true}
			changed = true
		}
	}
	if _, ok := s.data.JWTSubjects[userID]; !ok {
		p := model.Principal{UserID: userID, UserspaceID: userspaceID, Roles: []string{"admin"}, AuthMethod: "jwt"}
		s.data.JWTSubjects[userID] = JWTSubjectRecord{Subject: userID, Principal: p, Active: true}
		changed = true
	}
	if changed {
		return s.persistLocked()
	}
	return nil
}

func (s *Store) UpsertAPIKeyHash(hash string, p model.Principal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.AuthMethod = "apikey"
	s.data.APIKeys[hash] = APIKeyRecord{Hash: hash, Principal: p, Active: true}
	return s.persistLocked()
}

func (s *Store) UpsertJWTSubject(subject string, p model.Principal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.AuthMethod = "jwt"
	s.data.JWTSubjects[subject] = JWTSubjectRecord{Subject: subject, Principal: p, Active: true}
	return s.persistLocked()
}

func (s *Store) Get(userspaceID, key string) (model.KVRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return getFrom(&s.data, userspaceID, key)
}

func (s *Store) Put(userspaceID, key string, value []byte, contentType string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := putInto(&s.data, userspaceID, key, value, contentType)
	return rec.Version, s.persistLocked()
}

func (s *Store) Delete(userspaceID, key string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := getFrom(&s.data, userspaceID, key); err != nil {
		return 0, err
	}
	s.data.Version++
	delete(s.data.Userspaces[userspaceID], key)
	return s.data.Version, s.persistLocked()
}

func (s *Store) Exists(userspaceID, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := getFrom(&s.data, userspaceID, key)
	return err == nil
}

func (s *Store) ExportUserspace(userspaceID string) ([]model.KVRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []model.KVRecord
	for _, rec := range s.data.Userspaces[userspaceID] {
		records = append(records, cloneRecord(rec))
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Key < records[j].Key })
	return records, nil
}

func (s *Store) ImportUserspace(userspaceID string, records []model.KVRecord, mode model.ImportMode) (model.ImportResult, error) {
	tx, err := s.BeginAtomic()
	if err != nil {
		return model.ImportResult{}, err
	}
	res := tx.ImportUserspace(userspaceID, records, mode)
	if err := tx.Commit(); err != nil {
		return model.ImportResult{}, err
	}
	return res, nil
}

func (s *Store) ResolveAPIKeyHash(hash string) (model.Principal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data.APIKeys[hash]
	if !ok || !rec.Active {
		return model.Principal{}, false
	}
	p := rec.Principal
	p.AuthMethod = "apikey"
	return p, true
}

func (s *Store) ResolveJWTSubject(subject string) (model.Principal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data.JWTSubjects[subject]
	if !ok || !rec.Active {
		return model.Principal{}, false
	}
	p := rec.Principal
	p.AuthMethod = "jwt"
	return p, true
}

func (s *Store) SaveTx(tx model.Transaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Txs[tx.TxID] = cloneTx(tx)
	return s.persistLocked()
}

func (s *Store) GetTx(txID string) (model.Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, ok := s.data.Txs[txID]
	if !ok {
		return model.Transaction{}, ErrTxNotFound
	}
	return cloneTx(tx), nil
}

func (s *Store) ListTxs() ([]model.Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Transaction, 0, len(s.data.Txs))
	for _, tx := range s.data.Txs {
		out = append(out, cloneTx(tx))
	}
	return out, nil
}

func (s *Store) BeginAtomic() (*AtomicTx, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp, err := cloneSnapshot(s.data)
	if err != nil {
		return nil, err
	}
	return &AtomicTx{store: s, data: cp}, nil
}

type AtomicTx struct {
	store *Store
	data  snapshot
	done  bool
}

func (tx *AtomicTx) Get(userspaceID, key string) (model.KVRecord, error) {
	return getFrom(&tx.data, userspaceID, key)
}

func (tx *AtomicTx) Put(userspaceID, key string, value []byte, contentType string) (model.KVRecord, error) {
	return putInto(&tx.data, userspaceID, key, value, contentType), nil
}

func (tx *AtomicTx) Delete(userspaceID, key string) (uint64, error) {
	if _, err := getFrom(&tx.data, userspaceID, key); err != nil {
		return 0, err
	}
	tx.data.Version++
	delete(tx.data.Userspaces[userspaceID], key)
	return tx.data.Version, nil
}

func (tx *AtomicTx) Exists(userspaceID, key string) bool {
	_, err := getFrom(&tx.data, userspaceID, key)
	return err == nil
}

func (tx *AtomicTx) ImportUserspace(userspaceID string, records []model.KVRecord, mode model.ImportMode) model.ImportResult {
	if tx.data.Userspaces == nil {
		tx.data.Userspaces = map[string]map[string]model.KVRecord{}
	}
	if mode == model.ImportReplace {
		tx.data.Userspaces[userspaceID] = map[string]model.KVRecord{}
	}
	if tx.data.Userspaces[userspaceID] == nil {
		tx.data.Userspaces[userspaceID] = map[string]model.KVRecord{}
	}
	res := model.ImportResult{}
	for _, in := range records {
		_, exists := tx.data.Userspaces[userspaceID][in.Key]
		if exists && mode == model.ImportMergeSkip {
			res.Skipped++
			continue
		}
		if exists {
			res.Replaced++
		}
		tx.data.Version++
		now := time.Now().UTC()
		rec := cloneRecord(in)
		rec.UserspaceID = userspaceID
		rec.Version = tx.data.Version
		if rec.CreatedAt.IsZero() {
			rec.CreatedAt = now
		}
		rec.UpdatedAt = now
		rec.Checksum = Checksum(rec.Value)
		rec.ValueType = ValueType(rec.ContentType)
		tx.data.Userspaces[userspaceID][rec.Key] = rec
		res.Imported++
	}
	return res
}

func (tx *AtomicTx) SaveTx(t model.Transaction) {
	tx.data.Txs[t.TxID] = cloneTx(t)
}

func (tx *AtomicTx) Commit() error {
	if tx.done {
		return errors.New("atomic tx already closed")
	}
	tx.store.mu.Lock()
	defer tx.store.mu.Unlock()
	tx.store.data = tx.data
	tx.done = true
	return tx.store.persistLocked()
}

func (tx *AtomicTx) Rollback() {
	tx.done = true
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = snapshot{
		Userspaces:  map[string]map[string]model.KVRecord{},
		APIKeys:     map[string]APIKeyRecord{},
		JWTSubjects: map[string]JWTSubjectRecord{},
		Txs:         map[string]model.Transaction{},
	}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.persistLocked()
	}
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return err
	}
	ensureMaps(&s.data)
	return nil
}

func (s *Store) persistLocked() error {
	ensureMaps(&s.data)
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func ensureMaps(d *snapshot) {
	if d.Userspaces == nil {
		d.Userspaces = map[string]map[string]model.KVRecord{}
	}
	if d.APIKeys == nil {
		d.APIKeys = map[string]APIKeyRecord{}
	}
	if d.JWTSubjects == nil {
		d.JWTSubjects = map[string]JWTSubjectRecord{}
	}
	if d.Txs == nil {
		d.Txs = map[string]model.Transaction{}
	}
}

func getFrom(d *snapshot, userspaceID, key string) (model.KVRecord, error) {
	space := d.Userspaces[userspaceID]
	if space == nil {
		return model.KVRecord{}, ErrNotFound
	}
	rec, ok := space[key]
	if !ok {
		return model.KVRecord{}, ErrNotFound
	}
	return cloneRecord(rec), nil
}

func putInto(d *snapshot, userspaceID, key string, value []byte, contentType string) model.KVRecord {
	ensureMaps(d)
	if d.Userspaces[userspaceID] == nil {
		d.Userspaces[userspaceID] = map[string]model.KVRecord{}
	}
	now := time.Now().UTC()
	created := now
	if old, ok := d.Userspaces[userspaceID][key]; ok {
		created = old.CreatedAt
	}
	d.Version++
	rec := model.KVRecord{
		UserspaceID: userspaceID,
		Key:         key,
		Value:       append([]byte(nil), value...),
		ContentType: contentType,
		ValueType:   ValueType(contentType),
		Version:     d.Version,
		CreatedAt:   created,
		UpdatedAt:   now,
		Checksum:    Checksum(value),
	}
	d.Userspaces[userspaceID][key] = rec
	return cloneRecord(rec)
}

func cloneSnapshot(in snapshot) (snapshot, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return snapshot{}, err
	}
	var out snapshot
	if err := json.Unmarshal(b, &out); err != nil {
		return snapshot{}, err
	}
	ensureMaps(&out)
	return out, nil
}

func cloneRecord(rec model.KVRecord) model.KVRecord {
	rec.Value = append([]byte(nil), rec.Value...)
	return rec
}

func cloneTx(tx model.Transaction) model.Transaction {
	if tx.Ops == nil {
		tx.Ops = map[int]model.TxOperation{}
	}
	cp := tx
	cp.Ops = make(map[int]model.TxOperation, len(tx.Ops))
	for k, op := range tx.Ops {
		op.Body = append([]byte(nil), op.Body...)
		cp.Ops[k] = op
	}
	if tx.Result != nil {
		r := *tx.Result
		r.Results = append([]model.TxOperationResult(nil), tx.Result.Results...)
		cp.Result = &r
	}
	return cp
}

func (s *Store) Ready() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return fmt.Errorf("storage path unavailable")
	}
	return nil
}
