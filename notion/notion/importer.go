package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sync"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

type NotionImporter struct {
	token  string
	rootID string // TODO: take a look at this

	notionChan chan notionRecord
	done       chan struct{}
	nReader    int

	nodeMapMtx sync.RWMutex
	nodeMap    map[string]*PageNode // PageID -> PageNode

	waitingChildrenMtx sync.Mutex
	waitingChildren    map[string][]*PageNode // ParentID -> []*PageNode

	topLevelPagesMtx sync.RWMutex
	topLevelPages    map[string]string // Top-level pages (id -> type)
}

func NewNotionImporter(ctx context.Context, options *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	token, ok := config["token"]
	if !ok {
		return nil, fmt.Errorf("missing token in config")
	}

	return &NotionImporter{
		token:           token,
		rootID:          "/",
		notionChan:      make(chan notionRecord, 1000),
		done:            make(chan struct{}, 1),
		nodeMap:         make(map[string]*PageNode),
		waitingChildren: make(map[string][]*PageNode),
		topLevelPages:   make(map[string]string),
	}, nil
}

func (p *NotionImporter) Root() string   { return p.rootID }
func (p *NotionImporter) Origin() string { return "notion.so" }
func (p *NotionImporter) Type() string   { return "notion" }

// This has a _lot_ of states, it can't be run twice at the same time.
func (p *NotionImporter) Flags() location.Flags { return location.FLAG_STREAM }

func (p *NotionImporter) Ping(ctx context.Context) error {
	return nil
}

func (p *NotionImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	//defer close(records)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		fInfo := objects.FileInfo{
			Lname:    "/",
			Lmode:    os.ModeDir | 0700,
			LmodTime: time.Time{}, // this seems dubious.. at best.
		}

		records <- connectors.NewRecord("/", "", fInfo, nil, nil)

		err := p.fetchAllPages("", records, &wg)
		if err != nil {
			records <- connectors.NewError("", err) // TODO: handle error more gracefully
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
	// 2. the routine below could create new readers too, repeating the problem.
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
				records <- connectors.NewError("", err)
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
					records <- connectors.NewError("", err)
					continue
				}

				imageURL := ib.Image.File.URL
				resp, err := http.Get(imageURL) // imageURL from Notion's response
				if err != nil {
					records <- connectors.NewError("", fmt.Errorf("failed to fetch image: %w", err))
					continue
				}

				if resp.StatusCode != http.StatusOK {
					records <- connectors.NewError("", fmt.Errorf("failed to fetch image, status code: %d", resp.StatusCode))
					continue
				}

				fInfo := objects.FileInfo{
					Lname:    b.ID + ".jpg",
					Lmode:    0700,
					LmodTime: time.Time{},
				}

				pathname := record.pathTo + "/" + b.ID + ".jpg"
				records <- connectors.NewRecord(pathname, "", fInfo, nil, func() (io.ReadCloser, error) {
					return resp.Body, nil
				})

			} else if b.HasChildren && b.Type != "child_page" {
				fInfo := objects.FileInfo{
					Lname:    b.ID,
					Lmode:    os.ModeDir | 0700,
					LmodTime: time.Time{},
				}

				pathname := record.pathTo + "/" + b.ID + "/blocks.json"
				records <- connectors.NewRecord(path.Dir(pathname), "", fInfo, nil, nil)
				fInfo.Lmode = 0700
				fInfo.Lname = path.Base(pathname)
				records <- connectors.NewRecord(pathname, "", fInfo, nil, func() (io.ReadCloser, error) {
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
				}}, records, &(p.nReader))
			}
		}
	}()

	go func() {
		wg2.Wait()

		fInfo := objects.FileInfo{
			Lname:    "content.json",
			Lmode:    0700,
			LmodTime: time.Time{},
		}
		records <- connectors.NewRecord("/content.json", "", fInfo, nil, func() (io.ReadCloser, error) {
			return p.NewReader("/content.json")
		})
		close(records)
	}()

	return nil
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

		p.topLevelPagesMtx.RLock()
		for id, typ := range p.topLevelPages {
			buff = append(buff, []byte("{\"parent\":{\"page_id\":\""+p.rootID+"\"},\"id\":\""+id+"\",\"object\":\""+typ+"\"}")...)
			if i == len(p.topLevelPages)-1 {
				buff = append(buff, []byte("]")...)
			} else {
				buff = append(buff, []byte(",")...)
			}
			i++
		}
		p.topLevelPagesMtx.RUnlock()
		rd = bytes.NewReader(buff)
	} else {
		return nil, fmt.Errorf("unsupported file type: %s", name)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create Notion reader: %w", err)
	}
	return io.NopCloser(rd), nil
}

func (p *NotionImporter) Close(ctx context.Context) error {
	ClearNodeTree()
	return nil
}
