package importexport

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"time"

	"httpkvdb/internal/model"
	"httpkvdb/internal/storage"
)

func TestEncodeDecodeChecksum(t *testing.T) {
	rec := model.KVRecord{Key: "k", Value: []byte(`{"a":1}`), ContentType: "application/json", ValueType: "json", Version: 7, CreatedAt: time.Now(), UpdatedAt: time.Now(), Checksum: storage.Checksum([]byte(`{"a":1}`))}
	data, err := Encode([]model.KVRecord{rec})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(data, 4096, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "k" || string(got[0].Value) != `{"a":1}` {
		t.Fatalf("bad decode: %+v", got)
	}
	data[len(data)-1] ^= 0xff
	if _, err := Decode(data, 4096, 1<<20); err != ErrChecksum {
		t.Fatalf("expected checksum error, got %v", err)
	}
}

func TestDecodeRejectsExcessiveRecordCount(t *testing.T) {
	var payload bytes.Buffer
	payload.Write(Magic[:])
	_ = binary.Write(&payload, binary.BigEndian, FormatVersion)
	_ = binary.Write(&payload, binary.BigEndian, time.Now().UnixMilli())
	_ = binary.Write(&payload, binary.BigEndian, uint64(1<<62))
	data := append([]byte(nil), payload.Bytes()...)
	sum := sha256.Sum256(data)
	data = append(data, sum[:]...)
	if _, err := Decode(data, 4096, 1<<20); err != ErrInvalidFormat {
		t.Fatalf("expected invalid format for excessive record count, got %v", err)
	}
}

func TestDecodeRejectsOversizedContentType(t *testing.T) {
	var payload bytes.Buffer
	payload.Write(Magic[:])
	_ = binary.Write(&payload, binary.BigEndian, FormatVersion)
	_ = binary.Write(&payload, binary.BigEndian, time.Now().UnixMilli())
	_ = binary.Write(&payload, binary.BigEndian, uint64(1))
	_ = binary.Write(&payload, binary.BigEndian, uint32(1))
	payload.WriteByte('k')
	_ = binary.Write(&payload, binary.BigEndian, uint16(300))
	payload.Write(bytes.Repeat([]byte{'a'}, 300))
	data := append([]byte(nil), payload.Bytes()...)
	sum := sha256.Sum256(data)
	data = append(data, sum[:]...)
	if _, err := Decode(data, 4096, 1<<20); err != ErrInvalidFormat {
		t.Fatalf("expected invalid format for oversized content type, got %v", err)
	}
}
