package importexport

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"time"

	"httpkvdb/internal/model"
	"httpkvdb/internal/storage"
)

func Decode(data []byte, maxKey int, maxValue int64) ([]model.KVRecord, error) {
	if len(data) < 8+4+8+8+32 {
		return nil, ErrInvalidFormat
	}
	payload := data[:len(data)-32]
	footer := data[len(data)-32:]
	sum := sha256.Sum256(payload)
	if !bytes.Equal(sum[:], footer) {
		return nil, ErrChecksum
	}
	r := bytes.NewReader(payload)
	var magic [8]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil || magic != Magic {
		return nil, ErrInvalidFormat
	}
	var version uint32
	var created int64
	var count uint64
	if binary.Read(r, binary.BigEndian, &version) != nil || version != FormatVersion {
		return nil, ErrInvalidFormat
	}
	if binary.Read(r, binary.BigEndian, &created) != nil || binary.Read(r, binary.BigEndian, &count) != nil {
		return nil, ErrInvalidFormat
	}
	records := make([]model.KVRecord, 0, count)
	for i := uint64(0); i < count; i++ {
		var keyLen uint32
		if binary.Read(r, binary.BigEndian, &keyLen) != nil || int(keyLen) > maxKey {
			return nil, ErrInvalidFormat
		}
		key := make([]byte, keyLen)
		if _, err := io.ReadFull(r, key); err != nil {
			return nil, ErrInvalidFormat
		}
		if err := storage.ValidateKey(string(key), maxKey); err != nil {
			return nil, err
		}
		var ctLen uint16
		if binary.Read(r, binary.BigEndian, &ctLen) != nil {
			return nil, ErrInvalidFormat
		}
		ct := make([]byte, ctLen)
		if _, err := io.ReadFull(r, ct); err != nil {
			return nil, ErrInvalidFormat
		}
		vt, err := r.ReadByte()
		if err != nil {
			return nil, ErrInvalidFormat
		}
		var valueLen uint64
		if binary.Read(r, binary.BigEndian, &valueLen) != nil || valueLen > uint64(maxValue) {
			return nil, ErrInvalidFormat
		}
		value := make([]byte, valueLen)
		if _, err := io.ReadFull(r, value); err != nil {
			return nil, ErrInvalidFormat
		}
		var rec model.KVRecord
		rec.Key = string(key)
		rec.ContentType = string(ct)
		rec.ValueType = valueTypeStringFromByte(vt)
		rec.Value = value
		var createdMS, updatedMS int64
		if binary.Read(r, binary.BigEndian, &rec.Version) != nil ||
			binary.Read(r, binary.BigEndian, &createdMS) != nil ||
			binary.Read(r, binary.BigEndian, &updatedMS) != nil {
			return nil, ErrInvalidFormat
		}
		var valueSum [32]byte
		if _, err := io.ReadFull(r, valueSum[:]); err != nil {
			return nil, ErrInvalidFormat
		}
		actual := storage.RawSHA256(value)
		if actual != valueSum {
			return nil, ErrChecksum
		}
		if err := storage.ValidateValue(rec.ContentType, rec.Value); err != nil {
			return nil, err
		}
		rec.CreatedAt = time.UnixMilli(createdMS).UTC()
		rec.UpdatedAt = time.UnixMilli(updatedMS).UTC()
		rec.Checksum = storage.Checksum(value)
		records = append(records, rec)
	}
	if r.Len() != 0 {
		return nil, ErrInvalidFormat
	}
	return records, nil
}

func valueTypeStringFromByte(v uint8) string {
	switch v {
	case valueTypeString:
		return "string"
	case valueTypeJSON:
		return "json"
	default:
		return "binary"
	}
}
