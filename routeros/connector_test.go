package routeros

import (
	"context"
	"strings"
	"testing"
)

func TestNewRedactsCredentials(t *testing.T) {
	loc := "routeros+export://user:secret@host/%zz"
	_, err := NewImporter(context.Background(), nil, "routeros+export",
		map[string]string{"location": loc})
	if err == nil {
		t.Fatalf("NewImporter(%q): expected error", loc)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("NewImporter(%q): error leaks password: %v", loc, err)
	}
}
