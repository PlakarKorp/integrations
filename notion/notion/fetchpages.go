package notion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/importer"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const notionSearchURL = NotionURL + "/search"

type SearchResponse struct {
	Results    []Page `json:"results"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

type Page struct {
	Object string         `json:"object"`
	ID     string         `json:"id"`
	Parent map[string]any `json:"parent"` // Parent can be a page, block, or workspace (string, string, or boolean)
	//Properties struct {
	//	Title struct {
	//		Title []struct {
	//			Text struct {
	//				Content string `json:"content"` // The title text (later used to create the displayed name)
	//			} `json:"text"`
	//		} `json:"title"`
	//	} `json:"title"`
	//} `json:"properties"`
	//Other properties can be added here as needed
}

type PageInfo struct {
	ID    string
	Title string
}

func (p *NotionImporter) fetchAllPages(cursor string, results chan<- *importer.ScanResult, wg *sync.WaitGroup) error {
	bodyMap := map[string]interface{}{
		"page_size": PageSize,
	}
	if cursor != "" {
		bodyMap["start_cursor"] = cursor
	}
	bodyJSON, _ := json.Marshal(bodyMap)

	req, err := http.NewRequest("POST", notionSearchURL, bytes.NewBuffer(bodyJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Notion-Version", NotionVersionHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("notion returned status code %d", resp.StatusCode)
	}

	var response SearchResponse
	err = json.NewDecoder(resp.Body).Decode(&response)
	if err != nil {
		return err
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		p.AddPagesToTree(response.Results, results, &(p.nReader))
	}()

	if response.HasMore {
		return p.fetchAllPages(response.NextCursor, results, wg)
	}

	return nil
}

type PageNode struct {
	Page            Page
	Children        []*PageNode
	Parent          *PageNode
	ConnectedToRoot bool
}

// Global maps //TODO change global variables to struct fields
var nodeMap = make(map[string]*PageNode)           // PageID -> PageNode
var waitingChildren = make(map[string][]*PageNode) // ParentID -> []*PageNode
var topLevelPages = make(map[string]string)        // Top-level pages (id -> type)

func (p *NotionImporter) AddPagesToTree(pages []Page, results chan<- *importer.ScanResult, nReader *int) {
	for _, page := range pages {
		id := page.ID
		parentID, ok := page.Parent[page.Parent["type"].(string)].(string)
		if !ok {
			parentID = ""
		}

		// Get or create the node
		node, exists := nodeMap[id]
		if !exists {
			node = &PageNode{Page: page}
			nodeMap[id] = node
		} else {
			node.Page = page
		}

		// Determine if it's a root node
		if parentID == "" {
			// Top-level page
			topLevelPages[id] = page.Object // Store id -> type
			p.propagateConnectionToRoot(node, results, nReader)
		} else {
			if parent, ok := nodeMap[parentID]; ok {
				// Attach to parent
				node.Parent = parent
				parent.Children = append(parent.Children, node)

				// Propagate connection if parent is already connected to root
				if parent.ConnectedToRoot {
					p.propagateConnectionToRoot(node, results, nReader)
				}
			} else {
				// Parent not yet known; defer
				waitingChildren[parentID] = append(waitingChildren[parentID], node)
			}
		}

		// Check if this node has waiting children
		if children, ok := waitingChildren[id]; ok {
			for _, child := range children {
				child.Parent = node
				node.Children = append(node.Children, child)

				// Propagate root connection if current node is connected
				if node.ConnectedToRoot {
					p.propagateConnectionToRoot(child, results, nReader)
				}
			}
			delete(waitingChildren, id)
		}
	}
}

func (p *NotionImporter) propagateConnectionToRoot(node *PageNode, results chan<- *importer.ScanResult, nReader *int) {
	if node.ConnectedToRoot {
		return
	}
	node.ConnectedToRoot = true

	if node.Page.Object != "block" {
		pageName := node.Page.Object + ".json"
		results <- importer.NewScanRecord(GetPathToRoot(node), "", objects.NewFileInfo(node.Page.ID, 0, os.ModeDir|0755, time.Time{}, 0, 0, 0, 0, 0), nil, nil)
		results <- importer.NewScanRecord(GetPathToRoot(node)+"/"+pageName, "", objects.NewFileInfo(pageName, 0, 0, time.Time{}, 0, 0, 0, 0, 0), nil, func() (io.ReadCloser, error) {
			return p.NewReader(GetPathToRoot(node) + "/" + pageName)
		})
		*nReader++
	}

	for _, child := range node.Children {
		p.propagateConnectionToRoot(child, results, nReader)
	}
}

func GetPathToRoot(node *PageNode) string {
	var path []string
	current := node

	for current != nil {
		title := current.Page.ID
		path = append([]string{title}, path...)
		current = current.Parent
	}

	return "/" + strings.Join(path, "/")
}

func ClearNodeTree() {
	nodeMap = make(map[string]*PageNode)
	waitingChildren = make(map[string][]*PageNode)
	topLevelPages = make(map[string]string)
}
