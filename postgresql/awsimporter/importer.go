package awsimporter

import (
	"context"
	"fmt"

	"github.com/PlakarKorp/integration-postgresql/awsauth"
	pqimporter "github.com/PlakarKorp/integration-postgresql/importer"
	"github.com/PlakarKorp/integration-postgresql/pgconn"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
)

func init() {
	importer.Register("postgres+aws", location.FLAG_STREAM, NewAWSImporter)
}

func NewAWSImporter(appCtx context.Context, opts *connectors.Options, name string, cfg map[string]string) (importer.Importer, error) {
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

	imp, err := pqimporter.NewImporterFromConfigMap(conn, dbPath, "postgresql+aws", cfg)
	if err != nil {
		return nil, err
	}

	// The token is generated just before each connection or subprocess so that
	// short-lived IAM tokens (~15 min) are always fresh when they are used.
	imp.TokenProvider = func(ctx context.Context) (string, error) {
		return awsauth.GenerateDBAuthToken(ctx, conn.Host, conn.Port, conn.Username, region)
	}
	return imp, nil
}
