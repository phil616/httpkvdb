package tx

import (
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"httpkvdb/internal/model"
	"httpkvdb/internal/observe"
)

func TestBodyLimitCountsNewFragmentsOnly(t *testing.T) {
	c, s, p := newTestCoordinator(t)
	c.maxBodySize = 6
	if _, err := c.CreateTx(p, 2, time.Minute, "limit"); err != nil {
		t.Fatal(err)
	}
	first := model.TxOperation{Seq: 1, OpID: "one", OpType: "PUT", Key: "a", ContentType: "text/plain", Body: []byte("123")}
	if _, _, err := c.AddOp(p, "limit", first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.AddOp(p, "limit", first); err != nil {
		t.Fatal("replay counted twice", err)
	}
	second := model.TxOperation{Seq: 2, OpID: "two", OpType: "PUT", Key: "b", ContentType: "text/plain", Body: []byte("4567")}
	if _, _, err := c.AddOp(p, "limit", second); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("body limit = %v", err)
	}
	stored, err := s.GetTx("limit")
	if err != nil || len(stored.Ops) != 1 {
		t.Fatal("rejected operation persisted", err)
	}
	second.Body = []byte("456")
	if _, _, err := c.AddOp(p, "limit", second); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Commit(p, "limit", 2, ""); err != nil {
		t.Fatal(err)
	}
}

func TestPruningRetainsResultsAndCommittedReplay(t *testing.T) {
	c, s, p := newTestCoordinator(t)
	op := model.TxOperation{Seq: 1, OpID: "one", OpType: "PUT", Key: "a", ContentType: "text/plain", Body: []byte("value")}
	if _, err := c.CreateTx(p, 1, time.Minute, "prune"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.AddOp(p, "prune", op); err != nil {
		t.Fatal(err)
	}
	_, result, err := c.Commit(p, "prune", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetTx("prune")
	if err != nil {
		t.Fatal(err)
	}
	stored.FinishedAt = time.Now().Add(-2 * time.Hour)
	if err := s.SaveTx(stored); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExpireDue(); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetTx("prune")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Ops[1].Body) != 0 || stored.Ops[1].BodyHash != BodyHash(op.Body) {
		t.Fatal("pruning lost operation identity")
	}
	if !reflect.DeepEqual(stored.Result, result) {
		t.Fatal("pruning changed result")
	}
	status, replayed, err := c.AddOp(p, "prune", op)
	if err != nil || status.Status != model.TxCommitted || !reflect.DeepEqual(result, replayed) {
		t.Fatal("committed replay changed", err)
	}
	op.Body = []byte("different")
	if _, _, err := c.AddOp(p, "prune", op); !errors.Is(err, ErrAlreadyCommitted) {
		t.Fatal("committed transaction accepted a different fragment", err)
	}
	record, err := s.Get(p.UserspaceID, "a")
	if err != nil || record.Version != 1 {
		t.Fatal("replayed operation executed twice", err)
	}
}

func TestWaitingCommitCannotClearEstablishedDigest(t *testing.T) {
	c, _, p := newTestCoordinator(t)
	op := model.TxOperation{Seq: 1, OpID: "op", OpType: "PUT", Key: "a", ContentType: "text/plain", Body: []byte("x"), BodyHash: BodyHash([]byte("x"))}
	digest := Digest(model.Transaction{Ops: map[int]model.TxOperation{1: op}})
	if _, err := c.CreateTx(p, 1, time.Minute, "digest"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Commit(p, "digest", 1, digest); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Commit(p, "digest", 1, "different"); !errors.Is(err, ErrInvalidTx) {
		t.Fatal("digest changed after commit", err)
	}
	if _, _, err := c.Commit(p, "digest", 1, ""); err != nil {
		t.Fatal(err)
	}
	op.Body = []byte("changed")
	if _, _, err := c.AddOp(p, "digest", op); !errors.Is(err, ErrAborted) {
		t.Fatal("empty retry cleared digest", err)
	}
}

func TestTransactionCountersMeasureTransitions(t *testing.T) {
	c, s, p := newTestCoordinator(t)
	c.metrics = &observe.Metrics{}
	op := model.TxOperation{Seq: 1, OpID: "op", OpType: "PUT", Key: "a", Body: []byte("x")}
	for i := 0; i < 2; i++ {
		if _, err := c.CreateTx(p, 1, time.Minute, "metrics"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := c.Commit(p, "metrics", 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, result, err := c.AddOp(p, "metrics", op); err != nil || result.Status != model.TxCommitted {
		t.Fatal("auto commit failed", err)
	}
	if _, _, err := c.Commit(p, "metrics", 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTx(p, 1, time.Minute, "abort"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.Abort(p, "abort"); err != nil {
			t.Fatal(err)
		}
	}
	transaction, err := c.CreateTx(p, 1, time.Minute, "expire")
	if err != nil {
		t.Fatal(err)
	}
	transaction.Deadline = time.Now().Add(-2 * time.Hour)
	transaction.Ops = map[int]model.TxOperation{1: op}
	if err := s.SaveTx(transaction); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExpireDue(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExpireDue(); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadUint64(&c.metrics.TxCreated) != 3 || atomic.LoadUint64(&c.metrics.TxCommitted) != 1 || atomic.LoadUint64(&c.metrics.TxAborted) != 1 || atomic.LoadUint64(&c.metrics.TxExpired) != 1 {
		t.Fatal("transaction retries counted as transitions")
	}
	stored, err := s.GetTx("expire")
	if err != nil || len(stored.Ops[1].Body) != 0 {
		t.Fatal("expired bodies not pruned", err)
	}
	if _, err := c.Abort(p, "expire"); !errors.Is(err, ErrExpired) {
		t.Fatal("expired transaction changed to aborted", err)
	}
}

func TestExistsDoesNotAssertAbsenceOrSkipFollowingWrites(t *testing.T) {
	c, s, p := newTestCoordinator(t)
	if _, err := c.CreateTx(p, 2, time.Minute, "exists"); err != nil {
		t.Fatal(err)
	}
	for _, op := range []model.TxOperation{
		{Seq: 1, OpID: "check", OpType: "EXISTS", Key: "key"},
		{Seq: 2, OpID: "put", OpType: "PUT", Key: "key", ContentType: "text/plain", Body: []byte("value")},
	} {
		if _, _, err := c.AddOp(p, "exists", op); err != nil {
			t.Fatal(err)
		}
	}
	_, result, err := c.Commit(p, "exists", 2, "")
	if err != nil || result.Status != model.TxCommitted || result.Results[0].Status != 404 || result.Results[1].Status != 200 {
		t.Fatal("EXISTS changed transaction control flow", err)
	}
	if _, err := s.Get(p.UserspaceID, "key"); err != nil {
		t.Fatal("PUT after EXISTS did not execute", err)
	}
}
