package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWritePasswordConfig(t *testing.T) {
	passwords := []string{
		"plain",
		`with"quote`,
		`with\backslash`,
		`\n`,
		"with\nnewline",
		"x\"\nuri: \"mongodb://elsewhere",
		"<&>",
		"unicode é ✓",
	}

	for _, password := range passwords {
		f, err := writePasswordConfig(password)
		require.NoError(t, err, password)
		defer cleanupTempFile(f)

		data, err := os.ReadFile(f.Name())
		require.NoError(t, err)

		// A single line holding a JSON string, which YAML parses as a
		// double-quoted scalar.
		line, ok := strings.CutSuffix(string(data), "\n")
		require.True(t, ok, "config does not end with a newline: %q", data)
		require.NotContains(t, line, "\n", "config is not a single line")

		value, ok := strings.CutPrefix(line, "password: ")
		require.True(t, ok, "config has no password key: %q", data)

		var got string
		require.NoError(t, json.Unmarshal([]byte(value), &got), "value is not a quoted string: %q", value)
		require.Equal(t, password, got)
	}
}
