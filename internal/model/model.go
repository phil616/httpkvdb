package model

import "time"

type Principal struct {
	UserID      string   `json:"user_id"`
	UserspaceID string   `json:"userspace_id"`
	Roles       []string `json:"roles"`
	AuthMethod  string   `json:"auth_method"`
}

type KVRecord struct {
	UserspaceID string    `json:"userspace_id"`
	Key         string    `json:"key"`
	Value       []byte    `json:"value"`
	ContentType string    `json:"content_type"`
	ValueType   string    `json:"value_type"`
	Version     uint64    `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Checksum    string    `json:"checksum"`
}

type TxStatus string

const (
	TxPending       TxStatus = "pending"
	TxWaitingForOps TxStatus = "waiting_for_ops"
	TxReady         TxStatus = "ready"
	TxCommitting    TxStatus = "committing"
	TxCommitted     TxStatus = "committed"
	TxAborted       TxStatus = "aborted"
	TxExpired       TxStatus = "expired"
)

type TxOperation struct {
	TxID        string    `json:"tx_id"`
	Seq         int       `json:"seq"`
	OpID        string    `json:"op_id"`
	OpType      string    `json:"op_type"`
	Key         string    `json:"key"`
	ContentType string    `json:"content_type,omitempty"`
	Body        []byte    `json:"body,omitempty"`
	BodyHash    string    `json:"body_hash"`
	ReceivedAt  time.Time `json:"received_at"`
}

type TxOperationResult struct {
	Seq         int    `json:"seq"`
	Op          string `json:"op"`
	Status      int    `json:"status"`
	Key         string `json:"key"`
	ContentType string `json:"content_type,omitempty"`
	ValueBase64 string `json:"value_base64,omitempty"`
	Version     uint64 `json:"version,omitempty"`
	Error       string `json:"error,omitempty"`
}

type TxResult struct {
	TxID    string              `json:"tx_id"`
	Status  TxStatus            `json:"status"`
	Results []TxOperationResult `json:"results,omitempty"`
}

type Transaction struct {
	TxID           string              `json:"tx_id"`
	UserID         string              `json:"user_id"`
	UserspaceID    string              `json:"userspace_id"`
	TotalOps       int                 `json:"total_ops"`
	Status         TxStatus            `json:"status"`
	CreatedAt      time.Time           `json:"created_at"`
	Deadline       time.Time           `json:"deadline"`
	CommitReceived bool                `json:"commit_received"`
	TxDigest       string              `json:"tx_digest,omitempty"`
	Ops            map[int]TxOperation `json:"ops"`
	Result         *TxResult           `json:"result,omitempty"`
	AbortReason    string              `json:"abort_reason,omitempty"`
}

type ImportMode string

const (
	ImportReplace        ImportMode = "replace"
	ImportMergeOverwrite ImportMode = "merge-overwrite"
	ImportMergeSkip      ImportMode = "merge-skip"
)

type ImportResult struct {
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
	Replaced int `json:"replaced"`
}
