package main

import (
	"fmt"
	"context"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/importer"
	"os"
	stdpath "path"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"io"
	"github.com/PlakarKorp/integration-rclone/importer/rclone"
)

type GooglePhotoImporter struct {
	*rclone.RcloneImporter
}

func NewGooglePhotoImporter(ctx context.Context, opts *importer.Options, name string, config map[string]string) (importer.Importer, error) {
	imp, err := rclone.NewRcloneImporter(ctx, opts, name, config)
	if err != nil {
		return nil, err
	}
	return &GooglePhotoImporter{RcloneImporter: imp.(*rclone.RcloneImporter)}, nil
}

func (p *GooglePhotoImporter) Scan() (<-chan *importer.ScanResult, error) {
	results := make(chan *importer.ScanResult, 1000)
	var wg sync.WaitGroup

	go func() {
		p.GenerateBaseDirectories(results)
		p.scanRecursive(results, "", &wg)
		wg.Wait()
		close(results)
	}()

	return results, nil
}

func (p *GooglePhotoImporter) scanRecursive(results chan *importer.ScanResult, path string, wg *sync.WaitGroup) {
	results, response, err := p.ListFolder(results, path)
	if err {
		return
	}
	p.scanFolder(results, path, response, wg)
}

func ggdPhotoSpeCase(filename string) error {
	if strings.HasPrefix(filename, "media/") {
		if strings.HasPrefix(filename, "media/all") {
			return nil
		}
		return fmt.Errorf("skipping %s", filename)
	}
	if filename == "upload" {
		return fmt.Errorf("skipping %s", filename)
	}
	return nil
}

func (p *GooglePhotoImporter) scanFolder(results chan *importer.ScanResult, path string, response rclone.Response, wg *sync.WaitGroup) {
	for _, file := range response.List {
		wg.Add(1)
		go func() {
			defer wg.Done()

			if ggdPhotoSpeCase(file.Path) != nil {
				return
			}

			parsedTime, err := time.Parse(time.RFC3339, file.ModTime)
			if err != nil {
				parsedTime = time.Unix(0, 0).UTC()
			}

			if file.IsDir {
				wg.Add(1)
				go func() {
					defer wg.Done()
					p.scanRecursive(results, file.Path, wg)
				}()

				results <- importer.NewScanRecord(
					p.GetPathInBackup(file.Path),
					"",
					objects.NewFileInfo(
						stdpath.Base(file.Name),
						0,
						0700|os.ModeDir,
						parsedTime,
						0,
						atomic.AddUint64(&p.Ino, 1),
						0,
						0,
						0,
					),
					nil,
					func() (io.ReadCloser, error) {
						return nil, nil
					},
				)
			} else {
				filesize := file.Size

				if file.Size < 0 {
					handle, err := p.NewReader(p.GetPathInBackup(file.Path))
					if err != nil {
						results <- importer.NewScanError(p.GetPathInBackup(path), err)
						return
					}
					name := handle.(*rclone.AutoremoveTmpFile).Name()
					size, err := os.Stat(name)
					if err != nil {
						results <- importer.NewScanError(p.GetPathInBackup(path), err)
						return
					}

					handle.Close()

					filesize = size.Size()
				}

				fi := objects.NewFileInfo(
					stdpath.Base(file.Path),
					filesize,
					0600,
					parsedTime,
					1,
					atomic.AddUint64(&p.Ino, 1),
					0,
					0,
					0,
				)

				results <- importer.NewScanRecord(
					p.GetPathInBackup(file.Path),
					"",
					fi,
					nil,
					func() (io.ReadCloser, error) {
						return p.NewReader(p.GetPathInBackup(file.Path))
					},
				)
			}

		}()
	}
}
