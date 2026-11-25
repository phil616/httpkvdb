package importexport

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"time"

	"httpkvdb/internal/model"
	"httpkvdb/internal/storage"
)

func Encode(records []model.KVRecord) ([]byte, error) {
	var body bytes.Buffer
	body.Write(Magic[:])
	_ = binary.Write(&body, binary.BigEndian, FormatVersion)
	_ = binary.Write(&body, binary.BigEndian, time.Now().UTC().UnixMilli())
	_ = binary.Write(&body, binary.BigEndian, uint64(len(records)))
	keys := map[string]struct{}{}
	for _, rec := range records {
		if _, exists := keys[rec.Key]; exists {
			return nil, ErrInvalidFormat
		}
		keys[rec.Key] = struct{}{}
		if storage.ValidateKey(rec.Key, len(rec.Key)) != nil || uint64(len(rec.Key)) > math.MaxUint32 || storage.ValidateContentType(rec.ContentType) != nil {
			return nil, ErrInvalidFormat
		}
		key := []byte(rec.Key)
		ct := []byte(rec.ContentType)
		_ = binary.Write(&body, binary.BigEndian, uint32(len(key)))
		body.Write(key)
		_ = binary.Write(&body, binary.BigEndian, uint16(len(ct)))
		body.Write(ct)
		body.WriteByte(valueTypeByte(rec.ValueType))
		_ = binary.Write(&body, binary.BigEndian, uint64(len(rec.Value)))
		body.Write(rec.Value)
		_ = binary.Write(&body, binary.BigEndian, rec.Version)
		_ = binary.Write(&body, binary.BigEndian, rec.CreatedAt.UnixMilli())
		_ = binary.Write(&body, binary.BigEndian, rec.UpdatedAt.UnixMilli())
		sum := storage.RawSHA256(rec.Value)
		body.Write(sum[:])
	}
	sum := sha256.Sum256(body.Bytes())
	body.Write(sum[:])
	return body.Bytes(), nil
}

func valueTypeByte(v string) uint8 {
	switch v {
	case "string":
		return valueTypeString
	case "json":
		return valueTypeJSON
	default:
		return valueTypeBinary
	}
}
