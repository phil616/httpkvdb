package tx

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"httpkvdb/internal/lock"
	"httpkvdb/internal/model"
	"httpkvdb/internal/storage"
)

func TestConcurrentOpsAllPersist(t *testing.T) {
	c, s, p := newTestCoordinator(t)
	const n = 20
	if _, err := c.CreateTx(p, n, time.Minute, "parallel"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, n)
	for i := 1; i <= n; i++ {
		go func(seq int) {
			<-start
			_, _, err := c.AddOp(p, "parallel", model.TxOperation{Seq: seq, OpID: "op", OpType: "GET", Key: "k"})
			errs <- err
		}(i)
	}
	close(start)
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	tr, err := s.GetTx("parallel")
	if err != nil || len(tr.Ops) != n {
		t.Fatalf("accepted operations lost: %d %v", len(tr.Ops), err)
	}
}
func TestConcurrentCommitExecutesOnce(t *testing.T) {
	c, s, p := newTestCoordinator(t)
	if _, err := c.CreateTx(p, 1, time.Minute, "double"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.AddOp(p, "double", model.TxOperation{Seq: 1, OpID: "op", OpType: "PUT", Key: "k", ContentType: "text/plain", Body: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan *model.TxResult, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; _, r, err := c.Commit(p, "double", 1, ""); results <- r; errs <- err }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	a, b := <-results, <-results
	if !reflect.DeepEqual(a, b) {
		t.Fatal("concurrent retries returned different results")
	}
	rec, err := s.Get(p.UserspaceID, "k")
	if err != nil || rec.Version != 1 {
		t.Fatalf("transaction executed more than once: version %d %v", rec.Version, err)
	}
}

type signalLock struct {
	lock.Serializable
	entered chan struct{}
	once    sync.Once
}

func (l *signalLock) Lock() { l.once.Do(func() { close(l.entered) }); l.Serializable.Lock() }
func TestQueuedCommitRechecksDeadline(t *testing.T) {
	c, s, p := newTestCoordinator(t)
	if _, err := c.CreateTx(p, 1, 100*time.Millisecond, "deadline"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.AddOp(p, "deadline", model.TxOperation{Seq: 1, OpID: "op", OpType: "PUT", Key: "k", Body: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	l := &signalLock{entered: make(chan struct{})}
	l.Serializable.Lock()
	c.lock = l
	done := make(chan error, 1)
	go func() { _, _, err := c.Commit(p, "deadline", 1, ""); done <- err }()
	<-l.entered
	time.Sleep(150 * time.Millisecond)
	l.Serializable.Unlock()
	if err := <-done; !errors.Is(err, ErrExpired) {
		t.Fatalf("queued commit = %v", err)
	}
	if _, err := s.Get(p.UserspaceID, "k"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expired transaction wrote KV: %v", err)
	}
}

type failingStateStore struct {
	*storage.Store
	err error
}

func (s *failingStateStore) SaveTx(model.Transaction) error { return s.err }

func TestTransactionStatePersistenceErrorsPropagate(t *testing.T) {
	for _, operation := range []string{"conflict", "expire-result", "expire-cleaner", "expire-commit", "digest", "rollback"} {
		t.Run(operation, func(t *testing.T) {
			c, s, p := newTestCoordinator(t)
			transaction, err := c.CreateTx(p, 1, time.Minute, "failed-state")
			if err != nil {
				t.Fatal(err)
			}
			op := model.TxOperation{Seq: 1, OpID: "op", OpType: "GET", Key: "missing"}
			if _, _, err := c.AddOp(p, transaction.TxID, op); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(operation, "expire-") {
				transaction, err = s.GetTx(transaction.TxID)
				if err != nil {
					t.Fatal(err)
				}
				transaction.Deadline = time.Now().Add(-time.Second)
				if err := s.SaveTx(transaction); err != nil {
					t.Fatal(err)
				}
			}
			diskErr := errors.New("state persistence failure")
			c.store = &failingStateStore{Store: s, err: diskErr}
			switch operation {
			case "conflict":
				op.OpID = "different"
				_, _, err = c.AddOp(p, transaction.TxID, op)
			case "expire-result":
				_, _, err = c.GetResult(p, transaction.TxID)
			case "expire-cleaner":
				_, err = c.ExpireDue()
			case "expire-commit":
				_, _, err = c.Commit(p, transaction.TxID, 1, "")
			case "digest":
				_, _, err = c.Commit(p, transaction.TxID, 1, "wrong digest")
			case "rollback":
				_, _, err = c.Commit(p, transaction.TxID, 1, "")
			}
			if !errors.Is(err, diskErr) {
				t.Fatalf("persistence error swallowed: %v", err)
			}
		})
	}
}
