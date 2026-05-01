package awsimporter

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"

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

	token, err := generateDBAuthToken(appCtx, conn.Host, conn.Port, conn.Username, region)
	if err != nil {
		return nil, err
	}
	conn.Password = token

	return pqimporter.NewImporterFromConfigMap(conn, dbPath, "postgresql+aws", cfg)
}

// generateDBAuthToken uses the AWS SDK to build a short-lived RDS IAM
// authentication token.  Credentials are resolved via the standard SDK
// chain: environment variables, ~/.aws/credentials, EC2/ECS instance
// metadata, etc.
//
// auth.BuildAuthToken has a bug (https://github.com/aws/aws-sdk-go-v2/issues/3365)
// where it produces a token without a "/" path component (host:port?Action=...
// instead of host:port/?Action=...).  The SigV4 canonical URI is therefore ""
// instead of "/", which does not match what RDS expects, causing PAM
// authentication failure.  We insert the missing slash after the fact.
func generateDBAuthToken(ctx context.Context, host, port, username, region string) (string, error) {
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return "", fmt.Errorf("postgres+aws: loading AWS config: %w", err)
	}

	token, err := auth.BuildAuthToken(ctx, host+":"+port, region, username, awsCfg.Credentials)
	if err != nil {
		return "", fmt.Errorf("postgres+aws: generating RDS auth token: %w", err)
	}

	// Fix for https://github.com/aws/aws-sdk-go-v2/issues/3365: insert the
	// missing "/" before the query string if BuildAuthToken omitted it.
	token = strings.Replace(token, ":"+port+"?", ":"+port+"/?", 1)

	return token, nil
}
