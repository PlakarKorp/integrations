package exporter

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/PlakarKorp/integration-imap/common"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/exporter"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type ImapExporter struct {
	ctx       context.Context
	connector common.ImapConnector
	client    *imapclient.Client
	buf       []byte
}

const MB = 1048576

func NewImapExporter(ctx context.Context, opts *exporter.Options, name string, config map[string]string) (exporter.Exporter, error) {
	exp := &ImapExporter{
		ctx: ctx,
		buf: make([]byte, 2*MB),
	}

	err := exp.connector.InitFromConfig(config)
	if err != nil {
		return nil, err
	}

	return exp, nil
}

func (exp *ImapExporter) Root() string {
	return "/"
}

func (exp *ImapExporter) CreateDirectory(pathname string) error {
	client, err := exp.getClient()
	if err != nil {
		return err
	}

	pathname, _ = strings.CutPrefix(pathname, "/")

	opts := imap.CreateOptions{}
	err = client.Create(pathname, &opts).Wait()
	if err != nil {
		e, ok := err.(*imap.Error)
		if ok && e.Code == imap.ResponseCodeAlreadyExists {
			return nil
		}
		return err
	}
	return nil
}

func (exp *ImapExporter) StoreFile(pathname string, fp io.Reader, size int64) error {
	client, err := exp.getClient()
	if err != nil {
		return err
	}

	pathname, _ = strings.CutPrefix(pathname, "/")
	// XXX
	path := strings.SplitN(pathname, "/", 2)

	opts := imap.AppendOptions{}
	appendCmd := client.Append(path[0], size, &opts)
	w, err := io.CopyBuffer(appendCmd, fp, exp.buf)
	if err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}
	if w != size {
		return fmt.Errorf("inconsistent number of bytes written")
	}
	err = appendCmd.Close()
	if err != nil {
		return fmt.Errorf("failed to close message: %w", err)
	}
	_, err = appendCmd.Wait()
	if err != nil {
		return fmt.Errorf("APPEND command failed: %w", err)
	}

	return nil
}

func (exp *ImapExporter) SetPermissions(pathname string, fileinfo *objects.FileInfo) error {
	return nil
}

func (exp *ImapExporter) Close() error {
	if exp.client != nil {
		return exp.client.Logout().Wait()
	}
	return nil
}

func (exp *ImapExporter) getClient() (*imapclient.Client, error) {
	if exp.client == nil {
		client, err := exp.connector.Connect()
		if err != nil {
			return nil, err
		}
		exp.client = client
	}
	return exp.client, nil
}
