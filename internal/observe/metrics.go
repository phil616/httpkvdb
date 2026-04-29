package observe

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

type Metrics struct {
	Requests    uint64
	KVGet       uint64
	KVPut       uint64
	KVDelete    uint64
	TxCreated   uint64
	TxCommitted uint64
	TxAborted   uint64
	TxExpired   uint64
	Import      uint64
	Export      uint64
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "http_requests_total %d\n", atomic.LoadUint64(&m.Requests))
		fmt.Fprintf(w, "kv_get_total %d\n", atomic.LoadUint64(&m.KVGet))
		fmt.Fprintf(w, "kv_put_total %d\n", atomic.LoadUint64(&m.KVPut))
		fmt.Fprintf(w, "kv_delete_total %d\n", atomic.LoadUint64(&m.KVDelete))
		fmt.Fprintf(w, "tx_created_total %d\n", atomic.LoadUint64(&m.TxCreated))
		fmt.Fprintf(w, "tx_committed_total %d\n", atomic.LoadUint64(&m.TxCommitted))
		fmt.Fprintf(w, "tx_aborted_total %d\n", atomic.LoadUint64(&m.TxAborted))
		fmt.Fprintf(w, "tx_expired_total %d\n", atomic.LoadUint64(&m.TxExpired))
		fmt.Fprintf(w, "import_total %d\n", atomic.LoadUint64(&m.Import))
		fmt.Fprintf(w, "export_total %d\n", atomic.LoadUint64(&m.Export))
	})
}
