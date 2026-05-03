package awsexporter

import (
	"context"
	"fmt"

	"github.com/PlakarKorp/integration-postgresql/awsauth"
	pqexporter "github.com/PlakarKorp/integration-postgresql/exporter"
	"github.com/PlakarKorp/integration-postgresql/pgconn"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
)

func init() {
	exporter.Register("postgres+aws", 0, NewAWSExporter)
}

func NewAWSExporter(appCtx context.Context, opts *connectors.Options, name string, cfg map[string]string) (exporter.Exporter, error) {
	conn, dbPath, err := pgconn.ParseConnConfig(cfg)
	if err != nil {
		return nil, err
	}

	region := cfg["region"]
	if region == "" {
		return nil, fmt.Errorf("postgres+aws: region is required")
	}

	if conn.Username == "" {
		return nil, fmt.Errorf("postgres+aws: username is required for IAM authentication")
	}

	// Generate an initial token for the Ping call that happens at construction time.
	token, err := awsauth.GenerateDBAuthToken(appCtx, conn.Host, conn.Port, conn.Username, region)
	if err != nil {
		return nil, err
	}
	conn.Password = token

	exp, err := pqexporter.NewExporterFromConfigMap(conn, dbPath, "postgresql+aws", cfg)
	if err != nil {
		return nil, err
	}

	// Refresh the token before each pg_restore / psql subprocess so that
	// slow restores are not affected by the token's short lifetime (~15 min).
	exp.TokenProvider = func(ctx context.Context) (string, error) {
		return awsauth.GenerateDBAuthToken(ctx, conn.Host, conn.Port, conn.Username, region)
	}
	return exp, nil
}
