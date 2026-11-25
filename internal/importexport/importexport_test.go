package importexport

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"strings"
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

func TestContentTypeAcceptedByExportCanBeReimported(t *testing.T) {
	for _, size := range []int{300, storage.MaxContentTypeSize} {
		record := model.KVRecord{Key: "k", Value: []byte("x"), ContentType: strings.Repeat("a", size), ValueType: "binary"}
		data, err := Encode([]model.KVRecord{record})
		if err != nil {
			t.Fatal(err)
		}
		records, err := Decode(data, 4096, 1<<20)
		if err != nil || len(records) != 1 || records[0].ContentType != record.ContentType {
			t.Fatal("valid metadata cannot round trip", err)
		}
	}
	record := model.KVRecord{Key: "k", ContentType: strings.Repeat("a", storage.MaxContentTypeSize+1)}
	if _, err := Encode([]model.KVRecord{record}); err != ErrInvalidFormat {
		t.Fatal("metadata length overflow accepted")
	}
}

func TestDecodeRejectsDuplicateKeysAndUnknownValueTypes(t *testing.T) {
	records := []model.KVRecord{{Key: "a", Value: []byte("one"), ContentType: "text/plain", ValueType: "string"}, {Key: "b", Value: []byte("two"), ContentType: "text/plain", ValueType: "string"}}
	data, err := Encode(records)
	if err != nil {
		t.Fatal(err)
	}
	// First record begins after the fixed 28-byte header. Its type byte follows
	// the uint32 key length, key, uint16 content type length, and content type.
	firstType := 28 + 4 + 1 + 2 + len("text/plain")
	unknown := append([]byte(nil), data...)
	unknown[firstType] = 255
	checksum := sha256.Sum256(unknown[:len(unknown)-32])
	copy(unknown[len(unknown)-32:], checksum[:])
	if _, err := Decode(unknown, 4096, 1<<20); err != ErrInvalidFormat {
		t.Fatal("unknown value type accepted")
	}
	duplicate := append([]byte(nil), data...)
	secondKey := 28 + (4 + 1 + 2 + len("text/plain") + 1 + 8 + 3 + 8 + 8 + 8 + 32) + 4
	duplicate[secondKey] = 'a'
	checksum = sha256.Sum256(duplicate[:len(duplicate)-32])
	copy(duplicate[len(duplicate)-32:], checksum[:])
	if _, err := Decode(duplicate, 4096, 1<<20); err != ErrInvalidFormat {
		t.Fatal("duplicate import key accepted")
	}
	records[1].Key = "a"
	if _, err := Encode(records); err != ErrInvalidFormat {
		t.Fatal("duplicate export key accepted")
	}
}
