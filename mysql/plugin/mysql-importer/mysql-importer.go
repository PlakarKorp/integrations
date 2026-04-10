package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-mysql/importer"
	"github.com/PlakarKorp/integration-mysql/manifest"
	"github.com/PlakarKorp/integration-mysql/mysqlconn"
	"github.com/PlakarKorp/kloset/connectors"
	iimporter "github.com/PlakarKorp/kloset/connectors/importer"
)

type mysqlImporter struct {
	importer.Importer
	columnStatistics bool
	setGTIDPurged    string
}

func newMySQL(_ context.Context, _ *connectors.Options, proto string, config map[string]string) (iimporter.Importer, error) {
	conn, err := mysqlconn.ParseConnConfig(config)
	if err != nil {
		return nil, err
	}
	conn.ClientBin = "mysql"
	conn.DumpBin = "mysqldump"
	conn.ExpectedFlavor = "mysql"

	base, err := importer.New(proto, conn, config)
	if err != nil {
		return nil, err
	}

	m := &mysqlImporter{Importer: *base, columnStatistics: true}

	if v, ok := config["column_statistics"]; ok && v != "" {
		cs, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid value for column_statistics: %w", err)
		}
		m.columnStatistics = cs
	}

	if v, ok := config["set_gtid_purged"]; ok && v != "" {
		v = strings.ToUpper(v)
		switch v {
		case "AUTO", "ON", "OFF":
		default:
			return nil, fmt.Errorf("invalid value for set_gtid_purged: %q", v)
		}
		m.setGTIDPurged = v
	}

	return m, nil
}

func (m *mysqlImporter) Import(ctx context.Context, records chan<- *connectors.Record, _ <-chan *connectors.Result) error {
	if err := m.Conn.CheckFlavor(ctx, "mysql"); err != nil {
		return err
	}
	cs := m.columnStatistics
	opts := m.CommonManifestOptions()
	opts.ColumnStatistics = &cs
	opts.SetGTIDPurged = m.setGTIDPurged

	cfg := manifest.Config{
		Conn:     m.Conn,
		Flavor:   "mysql",
		Database: m.Database,
		Options:  opts,
	}

	var extraFlags []string
	if !m.columnStatistics {
		extraFlags = append(extraFlags, "--column-statistics=0")
	}
	if m.setGTIDPurged != "" {
		extraFlags = append(extraFlags, "--set-gtid-purged="+m.setGTIDPurged)
	}

	return m.Run(ctx, records, cfg, extraFlags)
}

func main() {
	sdk.EntrypointImporter(os.Args, newMySQL)
}
