package main

import (
	"context"
	"io"
	"os"
	"path"

	"github.com/PlakarKorp/integrations/openwrt/openwrtclient"
)

func saveToFile(f *openwrtclient.BackupFile) {
	out, err := os.Create(path.Base(f.Filename))
	if err != nil {
		panic(err)
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, f)
	if err != nil {
		panic(err)
	}
}

func main() {
	login := os.Getenv("OPENWRT_LOGIN")
	pwd := os.Getenv("OPENWRT_PASSWORD")
	url := os.Getenv("OPENWRT_URL")
	oclt := openwrtclient.NewBackupClient(url, login, pwd)
	bfile, err := oclt.GetBackupArchive(context.Background())
	if err != nil {
		panic(err)
	}
	defer func() { _ = bfile.Close() }()
	saveToFile(bfile)
}
