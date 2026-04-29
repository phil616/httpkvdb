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
	"time"

	"httpkvdb/internal/model"
	"httpkvdb/internal/storage"
)

var (
	ErrInvalidTx        = errors.New("invalid tx")
	ErrForbidden        = errors.New("forbidden")
	ErrSeqConflict      = errors.New("seq conflict")
	ErrAlreadyCommitted = errors.New("tx already committed")
	ErrAborted          = errors.New("tx aborted")
	ErrExpired          = errors.New("tx expired")
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
}

func NewCoordinator(store Store, lock Locker, maxOps int, defaultTimeout, maxTimeout time.Duration) *Coordinator {
	return &Coordinator{store: store, lock: lock, maxOps: maxOps, defaultTimeout: defaultTimeout, maxTimeout: maxTimeout}
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
	if totalOps <= 0 || totalOps > c.maxOps {
		return model.Transaction{}, ErrInvalidTx
	}
	if timeout <= 0 {
		timeout = c.defaultTimeout
	}
	if timeout > c.maxTimeout {
		return model.Transaction{}, ErrInvalidTx
	}
	txID := requestedID
	if txID == "" {
		txID = newTxID()
	} else if !validTxID(txID) {
		return model.Transaction{}, ErrInvalidTx
	}
	if existing, err := c.store.GetTx(txID); err == nil {
		if existing.UserID == p.UserID && existing.UserspaceID == p.UserspaceID && existing.TotalOps == totalOps {
			return existing, nil
		}
		return model.Transaction{}, ErrInvalidTx
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
	return t, c.store.SaveTx(t)
}

func (c *Coordinator) AddOp(p model.Principal, txID string, op model.TxOperation) (Status, *model.TxResult, error) {
	t, err := c.loadOwned(p, txID)
	if err != nil {
		return Status{}, nil, err
	}
	if err := c.mutable(&t); err != nil {
		return Status{}, nil, err
	}
	if op.Seq < 1 || op.Seq > t.TotalOps || op.OpID == "" {
		return Status{}, nil, ErrInvalidTx
	}
	op.TxID = txID
	op.ReceivedAt = time.Now().UTC()
	op.BodyHash = BodyHash(op.Body)
	if old, ok := t.Ops[op.Seq]; ok {
		if sameOp(old, op) {
			return c.status(t), t.Result, nil
		}
		t.Status = model.TxAborted
		t.AbortReason = "SEQ_CONFLICT"
		_ = c.store.SaveTx(t)
		return Status{}, nil, ErrSeqConflict
	}
	t.Ops[op.Seq] = op
	if t.CommitReceived && len(t.Ops) == t.TotalOps {
		res, err := c.execute(t)
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
	t.CommitReceived = true
	t.TxDigest = digest
	if len(t.Ops) < t.TotalOps {
		t.Status = model.TxWaitingForOps
		return c.status(t), nil, c.store.SaveTx(t)
	}
	res, err := c.execute(t)
	return c.status(t), res, err
}

func (c *Coordinator) Abort(p model.Principal, txID string) (Status, error) {
	t, err := c.loadOwned(p, txID)
	if err != nil {
		return Status{}, err
	}
	if t.Status == model.TxCommitted {
		return Status{}, ErrAlreadyCommitted
	}
	t.Status = model.TxAborted
	t.AbortReason = "client aborted"
	return c.status(t), c.store.SaveTx(t)
}

func (c *Coordinator) GetResult(p model.Principal, txID string) (Status, *model.TxResult, error) {
	t, err := c.loadOwned(p, txID)
	if err != nil {
		return Status{}, nil, err
	}
	_ = c.expireIfNeeded(&t)
	return c.status(t), t.Result, nil
}

func (c *Coordinator) ExpireDue() (int, error) {
	txs, err := c.store.ListTxs()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, t := range txs {
		if c.expireIfNeeded(&t) {
			count++
		}
	}
	return count, nil
}

func (c *Coordinator) execute(t model.Transaction) (*model.TxResult, error) {
	if t.TxDigest != "" && t.TxDigest != Digest(t) {
		t.Status = model.TxAborted
		t.AbortReason = "tx digest mismatch"
		_ = c.store.SaveTx(t)
		return nil, ErrAborted
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	atx, err := c.store.BeginAtomic()
	if err != nil {
		return nil, err
	}
	t.Status = model.TxCommitting
	results := make([]model.TxOperationResult, 0, t.TotalOps)
	for seq := 1; seq <= t.TotalOps; seq++ {
		op := t.Ops[seq]
		item := model.TxOperationResult{Seq: seq, Op: op.OpType, Key: op.Key}
		switch op.OpType {
		case "PUT":
			rec, err := atx.Put(t.UserspaceID, op.Key, op.Body, op.ContentType)
			if err != nil {
				atx.Rollback()
				t.Status = model.TxAborted
				t.AbortReason = err.Error()
				t.Result = &model.TxResult{TxID: t.TxID, Status: model.TxAborted, Results: append(results, fail(item, err).(model.TxOperationResult))}
				_ = c.store.SaveTx(t)
				return t.Result, err
			}
			item.Status = 200
			item.Version = rec.Version
		case "GET":
			rec, err := atx.Get(t.UserspaceID, op.Key)
			if err != nil {
				atx.Rollback()
				t.Status = model.TxAborted
				t.AbortReason = "KEY_NOT_FOUND"
				item = fail(item, err).(model.TxOperationResult)
				t.Result = &model.TxResult{TxID: t.TxID, Status: model.TxAborted, Results: append(results, item)}
				_ = c.store.SaveTx(t)
				return t.Result, err
			}
			item.Status = 200
			item.ContentType = rec.ContentType
			item.ValueBase64 = base64.StdEncoding.EncodeToString(rec.Value)
			item.Version = rec.Version
		case "HEAD":
			rec, err := atx.Get(t.UserspaceID, op.Key)
			if err != nil {
				atx.Rollback()
				t.Status = model.TxAborted
				t.AbortReason = "KEY_NOT_FOUND"
				item = fail(item, err).(model.TxOperationResult)
				t.Result = &model.TxResult{TxID: t.TxID, Status: model.TxAborted, Results: append(results, item)}
				_ = c.store.SaveTx(t)
				return t.Result, err
			}
			item.Status = 200
			item.ContentType = rec.ContentType
			item.Version = rec.Version
		case "EXISTS":
			if atx.Exists(t.UserspaceID, op.Key) {
				item.Status = 200
			} else {
				item.Status = 404
				item.Error = "KEY_NOT_FOUND"
			}
		case "DELETE":
			v, err := atx.Delete(t.UserspaceID, op.Key)
			if err != nil {
				atx.Rollback()
				t.Status = model.TxAborted
				t.AbortReason = "KEY_NOT_FOUND"
				item = fail(item, err).(model.TxOperationResult)
				t.Result = &model.TxResult{TxID: t.TxID, Status: model.TxAborted, Results: append(results, item)}
				_ = c.store.SaveTx(t)
				return t.Result, err
			}
			item.Status = 204
			item.Version = v
		default:
			atx.Rollback()
			t.Status = model.TxAborted
			t.AbortReason = "invalid op"
			_ = c.store.SaveTx(t)
			return nil, ErrInvalidTx
		}
		results = append(results, item)
	}
	t.Status = model.TxCommitted
	t.Result = &model.TxResult{TxID: t.TxID, Status: model.TxCommitted, Results: results}
	atx.SaveTx(t)
	if err := atx.Commit(); err != nil {
		return nil, err
	}
	return t.Result, nil
}

func fail(item model.TxOperationResult, err error) any {
	item.Status = 404
	item.Error = "KEY_NOT_FOUND"
	if err != storage.ErrNotFound {
		item.Status = 500
		item.Error = "STORAGE_ERROR"
	}
	return item
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
	if c.expireIfNeeded(t) {
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

func (c *Coordinator) expireIfNeeded(t *model.Transaction) bool {
	if (t.Status == model.TxPending || t.Status == model.TxWaitingForOps) && time.Now().After(t.Deadline) {
		t.Status = model.TxExpired
		_ = c.store.SaveTx(*t)
		return true
	}
	return false
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

func newTxID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "tx_" + hex.EncodeToString(b[:])
}
