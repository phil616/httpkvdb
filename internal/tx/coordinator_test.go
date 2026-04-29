package tx

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"httpkvdb/internal/lock"
	"httpkvdb/internal/model"
	"httpkvdb/internal/storage"
)

func newTestCoordinator(t *testing.T) (*Coordinator, *storage.Store, model.Principal) {
	t.Helper()
	s, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := model.Principal{UserID: "u1", UserspaceID: "space1"}
	return NewCoordinator(s, &lock.Serializable{}, 1000, time.Second, time.Minute), s, p
}

func TestTransactionReordersAndReadsPriorPut(t *testing.T) {
	c, _, p := newTestCoordinator(t)
	tr, err := c.CreateTx(p, 2, time.Second, "tx-order")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.AddOp(p, tr.TxID, model.TxOperation{Seq: 2, OpID: "op2", OpType: "GET", Key: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.AddOp(p, tr.TxID, model.TxOperation{Seq: 1, OpID: "op1", OpType: "PUT", Key: "a", ContentType: "text/plain", Body: []byte("one")}); err != nil {
		t.Fatal(err)
	}
	_, res, err := c.Commit(p, tr.TxID, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != model.TxCommitted || len(res.Results) != 2 {
		t.Fatalf("bad result: %+v", res)
	}
	want := base64.StdEncoding.EncodeToString([]byte("one"))
	if res.Results[1].ValueBase64 != want {
		t.Fatalf("tx GET did not see prior PUT: %+v", res.Results[1])
	}
}

func TestDuplicateOpIdempotentAndConflictAborts(t *testing.T) {
	c, _, p := newTestCoordinator(t)
	tr, err := c.CreateTx(p, 1, time.Second, "tx-dupe")
	if err != nil {
		t.Fatal(err)
	}
	op := model.TxOperation{Seq: 1, OpID: "op1", OpType: "PUT", Key: "a", ContentType: "text/plain", Body: []byte("one")}
	if _, _, err := c.AddOp(p, tr.TxID, op); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.AddOp(p, tr.TxID, op); err != nil {
		t.Fatalf("same op should be idempotent: %v", err)
	}
	op.Body = []byte("two")
	if _, _, err := c.AddOp(p, tr.TxID, op); !errors.Is(err, ErrSeqConflict) {
		t.Fatalf("expected seq conflict, got %v", err)
	}
	_, _, err = c.Commit(p, tr.TxID, 1, "")
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("conflicted tx should be aborted, got %v", err)
	}
}

func TestCommitBeforeOpsWaitsThenExecutes(t *testing.T) {
	c, _, p := newTestCoordinator(t)
	tr, err := c.CreateTx(p, 1, time.Second, "tx-wait")
	if err != nil {
		t.Fatal(err)
	}
	status, res, err := c.Commit(p, tr.TxID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if res != nil || status.Status != model.TxWaitingForOps || len(status.MissingSeq) != 1 {
		t.Fatalf("expected waiting, got status=%+v result=%+v", status, res)
	}
	_, res, err = c.AddOp(p, tr.TxID, model.TxOperation{Seq: 1, OpID: "op1", OpType: "PUT", Key: "a", ContentType: "text/plain", Body: []byte("one")})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.Status != model.TxCommitted {
		t.Fatalf("expected auto commit after missing op arrived, got %+v", res)
	}
}

func TestExpiredTransaction(t *testing.T) {
	c, _, p := newTestCoordinator(t)
	tr, err := c.CreateTx(p, 1, time.Millisecond, "tx-expire")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	_, _, err = c.Commit(p, tr.TxID, 1, "")
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expected expired, got %v", err)
	}
}

func TestTransactionFailureRollsBack(t *testing.T) {
	c, s, p := newTestCoordinator(t)
	if _, err := s.Put(p.UserspaceID, "before", []byte("old"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	tr, err := c.CreateTx(p, 2, time.Second, "tx-rollback")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = c.AddOp(p, tr.TxID, model.TxOperation{Seq: 1, OpID: "op1", OpType: "PUT", Key: "before", ContentType: "text/plain", Body: []byte("new")})
	_, _, _ = c.AddOp(p, tr.TxID, model.TxOperation{Seq: 2, OpID: "op2", OpType: "GET", Key: "missing"})
	_, _, err = c.Commit(p, tr.TxID, 2, "")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected missing key failure, got %v", err)
	}
	rec, err := s.Get(p.UserspaceID, "before")
	if err != nil || string(rec.Value) != "old" {
		t.Fatalf("rollback failed: %q %v", string(rec.Value), err)
	}
}

func TestForbiddenOtherUserTransaction(t *testing.T) {
	c, _, p := newTestCoordinator(t)
	tr, err := c.CreateTx(p, 1, time.Second, "tx-owner")
	if err != nil {
		t.Fatal(err)
	}
	other := model.Principal{UserID: "u2", UserspaceID: "space2"}
	_, _, err = c.Commit(other, tr.TxID, 1, "")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden, got %v", err)
	}
}
