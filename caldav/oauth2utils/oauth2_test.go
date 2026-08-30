package oauth2utils

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/oauth2"
)

// The token file holds a long-lived refresh token; os.Create used to leave it
// at 0644.
func TestSaveTokenIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")

	if err := saveToken(path, &oauth2.Token{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %o, want 600", perm)
	}

	tok, err := tokenFromFile(path)
	if err != nil {
		t.Fatalf("token did not round-trip: %v", err)
	}
	if tok.RefreshToken != "r" {
		t.Errorf("RefreshToken = %q, want %q", tok.RefreshToken, "r")
	}
}

// A file left behind by an older version keeps its mode through O_CREATE.
func TestSaveTokenTightensAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := saveToken(path, &oauth2.Token{AccessToken: "a"}); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

func TestRandomStateIsNotConstant(t *testing.T) {
	a, err := randomState()
	if err != nil {
		t.Fatal(err)
	}
	b, err := randomState()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("randomState returned the same value twice")
	}
	if a == "state-token" {
		t.Error("randomState returned the old constant")
	}
}
