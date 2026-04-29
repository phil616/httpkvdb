package importexport

import (
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
