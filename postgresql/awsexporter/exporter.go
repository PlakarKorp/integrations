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

	token, err := awsauth.GenerateDBAuthToken(appCtx, conn.Host, conn.Port, conn.Username, region)
	if err != nil {
		return nil, err
	}
	conn.Password = token

	return pqexporter.NewExporterFromConfigMap(conn, dbPath, "postgresql+aws", cfg)
}
