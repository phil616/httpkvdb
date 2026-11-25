package tx

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync/atomic"
	"time"

	"httpkvdb/internal/model"
	"httpkvdb/internal/observe"
	"httpkvdb/internal/storage"
)

var (
	ErrInvalidTx        = errors.New("invalid tx")
	ErrForbidden        = errors.New("forbidden")
	ErrSeqConflict      = errors.New("seq conflict")
	ErrAlreadyCommitted = errors.New("tx already committed")
	ErrAborted          = errors.New("tx aborted")
	ErrExpired          = errors.New("tx expired")
	ErrBodyTooLarge     = errors.New("transaction body limit exceeded")
)

type Store interface {
	SaveTx(model.Transaction) error
	GetTx(string) (model.Transaction, error)
	ListTxs() ([]model.Transaction, error)
	BeginAtomic() (*storage.AtomicTx, error)
}

type Locker interface {
	Lock()
	Unlock()
}

type Coordinator struct {
	store          Store
	lock           Locker
	maxOps         int
	defaultTimeout time.Duration
	maxTimeout     time.Duration
	maxBodySize    int64
	retention      time.Duration
	metrics        *observe.Metrics
}

// Options controls payload retention and accounting without changing transaction semantics.
type Options struct {
	MaxBodySize     int64
	ResultRetention time.Duration
	Metrics         *observe.Metrics
}

func NewCoordinator(store Store, lock Locker, maxOps int, defaultTimeout, maxTimeout time.Duration, options ...Options) *Coordinator {
	c := &Coordinator{store: store, lock: lock, maxOps: maxOps, defaultTimeout: defaultTimeout, maxTimeout: maxTimeout, maxBodySize: 256 << 20, retention: time.Hour}
	if len(options) > 0 {
		if options[0].MaxBodySize > 0 {
			c.maxBodySize = options[0].MaxBodySize
		}
		if options[0].ResultRetention > 0 {
			c.retention = options[0].ResultRetention
		}
		c.metrics = options[0].Metrics
	}
	return c
}

func (c *Coordinator) recordTransition(status model.TxStatus) {
	if c.metrics == nil {
		return
	}
	switch status {
	case model.TxPending:
		atomic.AddUint64(&c.metrics.TxCreated, 1)
	case model.TxCommitted:
		atomic.AddUint64(&c.metrics.TxCommitted, 1)
	case model.TxAborted:
		atomic.AddUint64(&c.metrics.TxAborted, 1)
	case model.TxExpired:
		atomic.AddUint64(&c.metrics.TxExpired, 1)
	}
}

type Status struct {
	TxID        string         `json:"tx_id"`
	Status      model.TxStatus `json:"status"`
	TotalOps    int            `json:"total_ops,omitempty"`
	Deadline    time.Time      `json:"deadline,omitempty"`
	ReceivedSeq []int          `json:"received_seq,omitempty"`
	MissingSeq  []int          `json:"missing_seq,omitempty"`
}

func (c *Coordinator) CreateTx(p model.Principal, totalOps int, timeout time.Duration, requestedID string) (model.Transaction, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if totalOps <= 0 || totalOps > c.maxOps {
		return model.Transaction{}, ErrInvalidTx
	}
	if timeout < 0 {
		return model.Transaction{}, ErrInvalidTx
	}
	if timeout == 0 {
		timeout = c.defaultTimeout
	}
	if timeout > c.maxTimeout {
		return model.Transaction{}, ErrInvalidTx
	}
	txID := requestedID
	if txID == "" {
		var err error
		txID, err = newTxID()
		if err != nil {
			return model.Transaction{}, err
		}
	} else if !validTxID(txID) {
		return model.Transaction{}, ErrInvalidTx
	}
	if existing, err := c.store.GetTx(txID); err == nil {
		if existing.UserID == p.UserID && existing.UserspaceID == p.UserspaceID && existing.TotalOps == totalOps {
			return existing, nil
		}
		return model.Transaction{}, ErrInvalidTx
	} else if !errors.Is(err, storage.ErrTxNotFound) {
		return model.Transaction{}, err
	}
	now := time.Now().UTC()
	t := model.Transaction{
		TxID:        txID,
		UserID:      p.UserID,
		UserspaceID: p.UserspaceID,
		TotalOps:    totalOps,
		Status:      model.TxPending,
		CreatedAt:   now,
		Deadline:    now.Add(timeout),
		Ops:         map[int]model.TxOperation{},
	}
	if err := c.store.SaveTx(t); err != nil {
		return model.Transaction{}, err
	}
	c.recordTransition(model.TxPending)
	return t, nil
}

func (c *Coordinator) AddOp(p model.Principal, txID string, op model.TxOperation) (Status, *model.TxResult, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	t, err := c.loadOwned(p, txID)
	if err != nil {
		return Status{}, nil, err
	}
	op.BodyHash = BodyHash(op.Body)
	if t.Status == model.TxCommitted {
		if old, ok := t.Ops[op.Seq]; ok && sameOp(old, op) {
			return c.status(t), t.Result, nil
		}
		return Status{}, nil, ErrAlreadyCommitted
	}
	if err := c.mutable(&t); err != nil {
		return Status{}, nil, err
	}
	if op.Seq < 1 || op.Seq > t.TotalOps || op.OpID == "" {
		return Status{}, nil, ErrInvalidTx
	}
	op.TxID = txID
	op.ReceivedAt = time.Now().UTC()
	if old, ok := t.Ops[op.Seq]; ok {
		if sameOp(old, op) {
			return c.status(t), t.Result, nil
		}
		if err := c.abort(&t, "SEQ_CONFLICT"); err != nil {
			return Status{}, nil, err
		}
		return Status{}, nil, ErrSeqConflict
	}
	remaining := c.maxBodySize - int64(len(op.Body))
	for _, existing := range t.Ops {
		remaining -= int64(len(existing.Body))
		if remaining < 0 {
			return Status{}, nil, ErrBodyTooLarge
		}
	}
	if remaining < 0 {
		return Status{}, nil, ErrBodyTooLarge
	}
	if t.Ops == nil {
		t.Ops = map[int]model.TxOperation{}
	}
	t.Ops[op.Seq] = op
	if t.CommitReceived && len(t.Ops) == t.TotalOps {
		res, err := c.execute(&t)
		return c.status(t), res, err
	}
	if t.CommitReceived {
		t.Status = model.TxWaitingForOps
	} else {
		t.Status = model.TxPending
	}
	return c.status(t), nil, c.store.SaveTx(t)
}

func (c *Coordinator) Commit(p model.Principal, txID string, totalOps int, digest string) (Status, *model.TxResult, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	t, err := c.loadOwned(p, txID)
	if err != nil {
		return Status{}, nil, err
	}
	if t.Status == model.TxCommitted {
		return c.status(t), t.Result, nil
	}
	if err := c.mutable(&t); err != nil {
		return Status{}, nil, err
	}
	if totalOps != 0 && totalOps != t.TotalOps {
		return Status{}, nil, ErrInvalidTx
	}
	if t.CommitReceived && t.TxDigest != "" && digest != "" && digest != t.TxDigest {
		return Status{}, nil, ErrInvalidTx
	}
	t.CommitReceived = true
	if digest != "" {
		t.TxDigest = digest
	}
	if len(t.Ops) < t.TotalOps {
		t.Status = model.TxWaitingForOps
		return c.status(t), nil, c.store.SaveTx(t)
	}
	res, err := c.execute(&t)
	return c.status(t), res, err
}

func (c *Coordinator) Abort(p model.Principal, txID string) (Status, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	t, err := c.loadOwned(p, txID)
	if err != nil {
		return Status{}, err
	}
	if t.Status == model.TxCommitted {
		return Status{}, ErrAlreadyCommitted
	}
	if t.Status == model.TxAborted {
		return c.status(t), nil
	}
	if err := c.mutable(&t); err != nil {
		return Status{}, err
	}
	if err := c.abort(&t, "client aborted"); err != nil {
		return Status{}, err
	}
	return c.status(t), nil
}

func (c *Coordinator) GetResult(p model.Principal, txID string) (Status, *model.TxResult, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	t, err := c.loadOwned(p, txID)
	if err != nil {
		return Status{}, nil, err
	}
	if _, err := c.expireIfNeeded(&t); err != nil {
		return Status{}, nil, err
	}
	return c.status(t), t.Result, nil
}

func (c *Coordinator) ExpireDue() (int, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	txs, err := c.store.ListTxs()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, t := range txs {
		expired, err := c.expireIfNeeded(&t)
		if err != nil {
			return count, err
		}
		if expired {
			count++
		}
		if err := c.pruneBodies(&t, time.Now()); err != nil {
			return count, err
		}
	}
	return count, nil
}

func (c *Coordinator) execute(t *model.Transaction) (*model.TxResult, error) {
	if t.TxDigest != "" && t.TxDigest != Digest(*t) {
		if err := c.abort(t, "tx digest mismatch"); err != nil {
			return nil, err
		}
		return nil, ErrAborted
	}
	atx, err := c.store.BeginAtomic()
	if err != nil {
		return nil, err
	}
	defer atx.Rollback()
	results := make([]model.TxOperationResult, 0, t.TotalOps)
	for seq := 1; seq <= t.TotalOps; seq++ {
		op, ok := t.Ops[seq]
		if !ok {
			return nil, ErrInvalidTx
		}
		item, opErr := executeOperation(atx, t.UserspaceID, op)
		if opErr != nil {
			atx.Rollback()
			item = fail(item, opErr)
			t.Result = &model.TxResult{TxID: t.TxID, Status: model.TxAborted, Results: append(results, item)}
			if err := c.abort(t, item.Error); err != nil {
				return nil, err
			}
			return t.Result, opErr
		}
		results = append(results, item)
	}
	t.Status = model.TxCommitted
	t.FinishedAt = time.Now().UTC()
	t.Result = &model.TxResult{TxID: t.TxID, Status: model.TxCommitted, Results: results}
	atx.SaveTx(*t)
	if err := atx.Commit(); err != nil {
		return nil, err
	}
	c.recordTransition(model.TxCommitted)
	return t.Result, nil
}

func executeOperation(atx *storage.AtomicTx, userspaceID string, op model.TxOperation) (model.TxOperationResult, error) {
	item := model.TxOperationResult{Seq: op.Seq, Op: op.OpType, Key: op.Key, Status: 200}
	switch op.OpType {
	case "PUT":
		record, err := atx.Put(userspaceID, op.Key, op.Body, op.ContentType)
		item.Version = record.Version
		return item, err
	case "GET", "HEAD", "EXISTS":
		record, err := atx.Get(userspaceID, op.Key)
		if op.OpType == "EXISTS" && errors.Is(err, storage.ErrNotFound) {
			return fail(item, err), nil
		}
		if err != nil {
			return item, err
		}
		if op.OpType != "EXISTS" {
			item.ContentType = record.ContentType
			item.Version = record.Version
		}
		if op.OpType == "GET" {
			item.ValueBase64 = base64.StdEncoding.EncodeToString(record.Value)
		}
		return item, nil
	case "DELETE":
		version, err := atx.Delete(userspaceID, op.Key)
		item.Version = version
		item.Status = 204
		return item, err
	default:
		return item, ErrInvalidTx
	}
}

func fail(item model.TxOperationResult, err error) model.TxOperationResult {
	item.Status = 500
	item.Error = "STORAGE_ERROR"
	if errors.Is(err, storage.ErrNotFound) {
		item.Status = 404
		item.Error = "KEY_NOT_FOUND"
	}
	if errors.Is(err, ErrInvalidTx) {
		item.Status = 400
		item.Error = "INVALID_TX"
	}
	return item
}

func (c *Coordinator) abort(t *model.Transaction, reason string) error {
	t.Status = model.TxAborted
	t.AbortReason = reason
	t.FinishedAt = time.Now().UTC()
	if err := c.store.SaveTx(*t); err != nil {
		return err
	}
	c.recordTransition(model.TxAborted)
	return nil
}

func (c *Coordinator) pruneBodies(t *model.Transaction, now time.Time) error {
	if t.Status != model.TxCommitted && t.Status != model.TxAborted && t.Status != model.TxExpired {
		return nil
	}
	finished := t.FinishedAt
	if finished.IsZero() {
		finished = t.Deadline
	} // Legacy transactions have no terminal timestamp.
	if now.Sub(finished) < c.retention {
		return nil
	}
	changed := false
	for seq, op := range t.Ops {
		if len(op.Body) > 0 {
			op.Body = nil
			t.Ops[seq] = op
			changed = true
		}
	}
	if changed {
		return c.store.SaveTx(*t)
	}
	return nil
}

func (c *Coordinator) loadOwned(p model.Principal, txID string) (model.Transaction, error) {
	t, err := c.store.GetTx(txID)
	if err != nil {
		return model.Transaction{}, err
	}
	if t.UserID != p.UserID || t.UserspaceID != p.UserspaceID {
		return model.Transaction{}, ErrForbidden
	}
	return t, nil
}

func (c *Coordinator) mutable(t *model.Transaction) error {
	if expired, err := c.expireIfNeeded(t); err != nil {
		return err
	} else if expired {
		return ErrExpired
	}
	switch t.Status {
	case model.TxAborted:
		return ErrAborted
	case model.TxExpired:
		return ErrExpired
	case model.TxCommitted:
		return ErrAlreadyCommitted
	}
	return nil
}

func (c *Coordinator) expireIfNeeded(t *model.Transaction) (bool, error) {
	if (t.Status == model.TxPending || t.Status == model.TxWaitingForOps) && time.Now().After(t.Deadline) {
		t.Status = model.TxExpired
		t.FinishedAt = t.Deadline
		if err := c.store.SaveTx(*t); err != nil {
			return false, err
		}
		c.recordTransition(model.TxExpired)
		return true, nil
	}
	return false, nil
}

func (c *Coordinator) status(t model.Transaction) Status {
	received := make([]int, 0, len(t.Ops))
	missing := make([]int, 0)
	for i := 1; i <= t.TotalOps; i++ {
		if _, ok := t.Ops[i]; ok {
			received = append(received, i)
		} else {
			missing = append(missing, i)
		}
	}
	return Status{TxID: t.TxID, Status: t.Status, TotalOps: t.TotalOps, Deadline: t.Deadline, ReceivedSeq: received, MissingSeq: missing}
}

func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func Digest(t model.Transaction) string {
	seqs := make([]int, 0, len(t.Ops))
	for seq := range t.Ops {
		seqs = append(seqs, seq)
	}
	sort.Ints(seqs)
	h := sha256.New()
	for _, seq := range seqs {
		op := t.Ops[seq]
		fmt.Fprintf(h, "%d\x00%s\x00%s\x00%s\x00%s\n", op.Seq, op.OpID, op.OpType, op.Key, op.BodyHash)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func sameOp(a, b model.TxOperation) bool {
	return a.OpID == b.OpID && a.OpType == b.OpType && a.Key == b.Key && a.ContentType == b.ContentType && a.BodyHash == b.BodyHash
}

var txIDRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func validTxID(id string) bool {
	return txIDRe.MatchString(id)
}

func newTxID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "tx_" + hex.EncodeToString(b[:]), nil
}
