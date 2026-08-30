package common

import "testing"

func TestParseOptionsDefaultsToTLS(t *testing.T) {
	opts, err := ParseOptions(map[string]string{"username": "u", "password": "p"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.TLS != "explicit" {
		t.Errorf("TLS = %q, want explicit", opts.TLS)
	}
	if opts.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should default to false")
	}
}

// Plain FTP sends credentials in the clear, so it has to be asked for twice.
func TestParseOptionsRequiresAcknowledgingCleartext(t *testing.T) {
	if _, err := ParseOptions(map[string]string{"tls": "none"}); err == nil {
		t.Fatal("tls=none was accepted without acknowledgement")
	}

	opts, err := ParseOptions(map[string]string{"tls": "none", "tls_insecure_no_verify": "true"})
	if err != nil {
		t.Fatalf("explicit opt-in rejected: %v", err)
	}
	if opts.TLS != "none" {
		t.Errorf("TLS = %q, want none", opts.TLS)
	}
}

func TestParseOptionsRejectsGarbage(t *testing.T) {
	if _, err := ParseOptions(map[string]string{"tls": "maybe"}); err == nil {
		t.Error("invalid tls value accepted")
	}
	if _, err := ParseOptions(map[string]string{"tls_insecure_no_verify": "yes-please"}); err == nil {
		t.Error("invalid tls_insecure_no_verify value accepted")
	}
}
