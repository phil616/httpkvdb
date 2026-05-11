package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidKey  = errors.New("invalid key")
	ErrInvalidJSON = errors.New("invalid json")
)

var userspaceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func ValidateKey(key string, max int) error {
	if key == "" || len(key) > max || !utf8.ValidString(key) {
		return ErrInvalidKey
	}
	return nil
}

func ValidateUserspaceID(userspaceID string) error {
	if userspaceID == "." || userspaceID == ".." || !userspaceIDPattern.MatchString(userspaceID) {
		return ErrInvalidKey
	}
	return nil
}

func ValueType(contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch ct {
	case "text/plain":
		return "string"
	case "application/json":
		return "json"
	case "application/octet-stream":
		return "binary"
	default:
		return "binary"
	}
}

func ValidateValue(contentType string, body []byte) error {
	if ValueType(contentType) == "json" && !json.Valid(body) {
		return ErrInvalidJSON
	}
	return nil
}

func Checksum(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func RawSHA256(value []byte) [32]byte {
	return sha256.Sum256(value)
}
