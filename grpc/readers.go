package integration_grpc

import (
	"io"
	"sync"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

type HoldingReaders struct {
	mu      sync.Mutex
	readers map[string]io.ReadCloser
}

func NewHoldingReaders() *HoldingReaders {
	return &HoldingReaders{
		readers: make(map[string]io.ReadCloser),
	}
}

func recordToPath(record *connectors.Record) string {
	pathname := record.Pathname

	if record.IsXattr {
		sep := ":"
		if record.XattrType == objects.AttributeADS {
			sep = "@"
		}
		pathname = "xattr:" + pathname + sep + record.XattrName
	}

	return pathname
}

func (h *HoldingReaders) Track(record *connectors.Record) {
	pathname := recordToPath(record)
	h.mu.Lock()
	h.readers[pathname] = record.Reader
	h.mu.Unlock()
}

func (h *HoldingReaders) Get(record *connectors.Record) io.ReadCloser {
	pathname := recordToPath(record)

	h.mu.Lock()
	rd, ok := h.readers[pathname]
	if ok {
		delete(h.readers, pathname)
	}
	h.mu.Unlock()
	return rd
}

func (h *HoldingReaders) Close() (err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, rd := range h.readers {
		if cerr := rd.Close(); err == nil {
			err = cerr
		}
	}

	return err
}
