package exporter

import (
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/stretchr/testify/require"
)

func TestNewExporterRejectsInvalidSkipPermissions(t *testing.T) {
	_, err := NewExporter(t.Context(), &connectors.Options{}, "ftp", map[string]string{
		"location":               "ftp://127.0.0.1:1",
		"tls_insecure_no_verify": "true",
		"skip_permissions":       "maybe",
	})
	require.ErrorContains(t, err, `invalid skip_permissions value "maybe"`)
}
