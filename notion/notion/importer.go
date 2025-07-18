package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"sync"
	"time"

	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/importer"
)

type NotionImporter struct {
	token  string
	rootID string // TODO: take a look at this

	notionChan chan notionRecord
	done       chan struct{}
	nReader    int
}

func NewNotionImporter(ctx context.Context, options *importer.Options, name string, config map[string]string) (importer.Importer, error) {
	token, ok := config["token"]
	if !ok {
		return nil, fmt.Errorf("missing token in config")
	}

	log.Printf("versionning, v42")

	return &NotionImporter{
		token:      token,
		rootID:     "/",
		notionChan: make(chan notionRecord, 1000),
		done:       make(chan struct{}, 1),
	}, nil
}

func (p *NotionImporter) Scan() (<-chan *importer.ScanResult, error) {
	results := make(chan *importer.ScanResult, 1000)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		fInfo := objects.NewFileInfo(
			"/",
			0,
			os.ModeDir|0700,
			time.Time{},
			0,
			0,
			0,
			0,
			0,
		)

		results <- importer.NewScanRecord("/", "", fInfo, nil, nil)

		err := p.fetchAllPages("", results, &wg)
		if err != nil {
			results <- importer.NewScanError("", err) // TODO: handle error more gracefully
			return
		}
	}()

	// WIP:
	// the goal of this second go routine is to keep track of the number of readers,
	// and process the records from the readers as they are being read.
	// the main problem is that we need to be sure that all readers are done
	// before we close the results channel.
	// this is a bit tricky because we don't know how many readers there will be,
	// because:
	// 1. the scan records above should finish to be processed first.
	// 2. the routine below coould create new readers too, reapeating the problem.
	// main question is:
	// 1. how do we know when all readers are done? (p.nReader == 0 is not enough,
	//	  is the last reader done, or not even started?)
	// 2. how do we know when all records are processed?
	go func() {
		wg.Wait()
		p.done <- struct{}{}
		close(p.done)
	}()

	var wg2 sync.WaitGroup
	wg2.Add(1)
	go func() {
		defer wg2.Done()

		// TODO: add a timeout here, to avoid blocking forever, or a way to break the loop in case of error
		for {
			if len(p.done) == 1 {
				// all scan are done, check if there are any readers left
				if p.nReader == 0 && len(results) == 0 && len(p.notionChan) == 0 {
					return
				}
			}

			var record notionRecord
			select {
			case record = <-p.notionChan:
				// process the record
				if record.EOF == true {
					p.nReader--
					continue
				}
			default:
				// no record available, continue
				continue
			}

			// do something with the record
			type block struct {
				ID          string            `json:"id"`
				HasChildren bool              `json:"has_children"`
				Type        string            `json:"type"`
				Parent      map[string]string `json:"parent"`
			}
			var b block
			if err := json.Unmarshal(record.Block, &b); err != nil {
				results <- importer.NewScanError("", err)
				continue
			}

			if b.Type == "image" {
				type imageBlock struct {
					Image struct {
						File struct {
							URL string `json:"url"`
						} `json:"file"`
					} `json:"image"`
				}

				var ib imageBlock
				if err := json.Unmarshal(record.Block, &ib); err != nil {
					results <- importer.NewScanError("", err)
					continue
				}

				imageURL := ib.Image.File.URL
				resp, err := http.Get(imageURL) // imageURL from Notion's response
				if err != nil {
					results <- importer.NewScanError("", fmt.Errorf("failed to fetch image: %w", err))
					continue
				}

				if resp.StatusCode != http.StatusOK {
					results <- importer.NewScanError("", fmt.Errorf("failed to fetch image, status code: %d", resp.StatusCode))
					continue
				}

				fInfo := objects.NewFileInfo(
					b.ID+".jpg",
					0,
					0700,
					time.Time{},
					0,
					0,
					0,
					0,
					0,
				)

				pathname := record.pathTo + "/" + b.ID + ".jpg"
				results <- importer.NewScanRecord(pathname, "", fInfo, nil, func() (io.ReadCloser, error) {
					return resp.Body, nil
				})

			} else if b.HasChildren && b.Type != "child_page" {
				fInfo := objects.NewFileInfo(
					b.ID,
					0,
					os.ModeDir|0700,
					time.Time{},
					0,
					0,
					0,
					0,
					0,
				)
				pathname := record.pathTo + "/" + b.ID + "/blocks.json"
				results <- importer.NewScanRecord(path.Dir(pathname), "", fInfo, nil, nil)
				fInfo.Lmode = 0700
				fInfo.Lname = path.Base(pathname)
				results <- importer.NewScanRecord(pathname, "", fInfo, nil, func() (io.ReadCloser, error) {
					return p.NewReader(pathname)
				})
				p.nReader++

				p.AddPagesToTree([]Page{{
					ID:     b.ID,
					Object: "block",
					Parent: map[string]any{
						"type":           b.Parent["type"],
						b.Parent["type"]: b.Parent[b.Parent["type"]],
					},
				}}, results, &(p.nReader))
			}
		}
	}()

	go func() {
		wg2.Wait()

		fInfo := objects.NewFileInfo(
			"content.json",
			0,
			0700,
			time.Time{},
			0,
			0,
			0,
			0,
			0,
		)
		results <- importer.NewScanRecord("/content.json", "", fInfo, nil, func() (io.ReadCloser, error) {
			return p.NewReader("/content.json")
		})

		close(results)
	}()
	return results, nil
}

func (p *NotionImporter) NewReader(pathname string) (io.ReadCloser, error) {
	id := path.Base(path.Dir(pathname))
	name := path.Base(pathname)
	var rd io.Reader
	var err error

	if name == "page.json" {
		rd, err = NewNotionReaderFile(p.token, id, path.Dir(pathname), p.notionChan)
	} else if name == "blocks.json" {
		rd, err = NewNotionReaderBlocks(p.token, id, path.Dir(pathname), p.notionChan)
	} else if name == "database.json" {
		rd, err = NewNotionReaderDatabase(p.token, id)
		p.nReader-- // This counter is used to track the number of readers that can produce records, databases can't.
	} else if name == "content.json" {
		for {
			if len(p.done) == 1 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		buff := make([]byte, 0)
		buff = append(buff, []byte("[")...)
		i := 0
		for id, typ := range topLevelPages {
			buff = append(buff, []byte("{\"parent\":{\"page_id\":\""+p.rootID+"\"},\"id\":\""+id+"\",\"object\":\""+typ+"\"}")...)
			if i == len(topLevelPages)-1 {
				buff = append(buff, []byte("]")...)
			} else {
				buff = append(buff, []byte(",")...)
			}
			i++
		}
		rd = bytes.NewReader(buff)
	} else {
		return nil, fmt.Errorf("unsupported file type: %s", name)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create Notion reader: %w", err)
	}
	return io.NopCloser(rd), nil
}

func (p *NotionImporter) NewExtendedAttributeReader(pathname string, attribute string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("extended attributes are not supported on Notion")
}

func (p *NotionImporter) GetExtendedAttributes(pathname string) ([]importer.ExtendedAttributes, error) {
	return nil, fmt.Errorf("extended attributes are not supported on Notion")
}

func (p *NotionImporter) Close() error {
	ClearNodeTree()
	return nil
}

func (p *NotionImporter) Root() string {
	return p.rootID
}

func (p *NotionImporter) Origin() string {
	return "notion.so"
}

func (p *NotionImporter) Type() string {
	return "notion"
}
