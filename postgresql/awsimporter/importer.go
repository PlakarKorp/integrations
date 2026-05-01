package awsimporter

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	pqimporter "github.com/PlakarKorp/integration-postgresql/importer"
	"github.com/PlakarKorp/integration-postgresql/pgconn"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
)

func init() {
	importer.Register("postgresql+aws", 0, NewAWSImporter)
}

func NewAWSImporter(appCtx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	conn, dbPath, err := pgconn.ParseConnConfig(config)
	if err != nil {
		return nil, err
	}

	region := config["region"]
	if region == "" {
		return nil, fmt.Errorf("postgres+aws: region is required")
	}

	if conn.Username == "" {
		return nil, fmt.Errorf("postgres+aws: username is required for IAM authentication")
	}

	awsCLI := config["aws_cli_path"]
	if awsCLI == "" {
		awsCLI = "aws"
	}

	token, err := generateDBAuthToken(appCtx, awsCLI, conn.Host, conn.Port, conn.Username, region)
	if err != nil {
		return nil, err
	}
	conn.Password = token

	return pqimporter.NewImporterFromConfigMap(conn, dbPath, "postgresql+aws", config)
}

// generateDBAuthToken calls `aws rds generate-db-auth-token` and returns the
// IAM authentication token to use as the PostgreSQL password.
func generateDBAuthToken(ctx context.Context, awsCLI, host, port, username, region string) (string, error) {
	args := []string{
		"rds", "generate-db-auth-token",
		"--hostname", host,
		"--port", port,
		"--username", username,
		"--region", region,
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, awsCLI, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return "", fmt.Errorf("aws rds generate-db-auth-token: %w: %s", err, s)
		}
		return "", fmt.Errorf("aws rds generate-db-auth-token: %w", err)
	}

	token := strings.TrimSpace(stdout.String())
	if token == "" {
		return "", fmt.Errorf("aws rds generate-db-auth-token returned an empty token")
	}
	return token, nil
}
