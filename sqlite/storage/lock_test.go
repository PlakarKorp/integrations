package storage

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
)

func TestPutLockRenewalUpdatesData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "repo.db")

	st, err := NewStore(ctx, "sqlite", map[string]string{"location": "sqlite://" + path})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, []byte("config")); err != nil {
		t.Fatal(err)
	}
	defer st.Close(ctx)

	id := objects.MAC{1}
	for _, data := range []string{"lock-v1", "lock-v2"} {
		if _, err := st.Put(ctx, storage.StorageResourceLock, id, bytes.NewReader([]byte(data))); err != nil {
			t.Fatal(err)
		}
	}

	rd, err := st.Get(ctx, storage.StorageResourceLock, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "lock-v2" {
		t.Fatalf("got lock data %q, want %q", got, "lock-v2")
	}
}
