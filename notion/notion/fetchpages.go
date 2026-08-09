package notion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
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

func (p *NotionImporter) fetchAllPages(cursor string, results chan<- *connectors.Record, wg *sync.WaitGroup) error {
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

func (p *NotionImporter) AddPagesToTree(pages []Page, results chan<- *connectors.Record, nReader *int) {
	for _, page := range pages {
		id := page.ID
		parentID, ok := page.Parent[page.Parent["type"].(string)].(string)
		if !ok {
			parentID = ""
		}

		// Get or create the node
		p.nodeMapMtx.Lock()
		node, exists := p.nodeMap[id]
		if !exists {
			node = &PageNode{Page: page}
			p.nodeMap[id] = node
		} else {
			node.Page = page
		}
		p.nodeMapMtx.Unlock()

		// Determine if it's a root node
		if parentID == "" {
			// Top-level page
			p.topLevelPagesMtx.Lock()
			p.topLevelPages[id] = page.Object
			p.topLevelPagesMtx.Unlock()

			p.propagateConnectionToRoot(node, results, nReader)
		} else {
			p.nodeMapMtx.RLock()
			if parent, ok := p.nodeMap[parentID]; ok {
				p.nodeMapMtx.RUnlock()
				// Attach to parent
				node.Parent = parent
				parent.Children = append(parent.Children, node)

				// Propagate connection if parent is already connected to root
				if parent.ConnectedToRoot {
					p.propagateConnectionToRoot(node, results, nReader)
				}
			} else {
				p.nodeMapMtx.RUnlock()
				// Parent not yet known; defer
				p.waitingChildrenMtx.Lock()
				p.waitingChildren[parentID] = append(p.waitingChildren[parentID], node)
				p.waitingChildrenMtx.Unlock()
			}
		}

		// Check if this node has waiting children
		p.waitingChildrenMtx.Lock()
		if children, ok := p.waitingChildren[id]; ok {
			for _, child := range children {
				child.Parent = node
				node.Children = append(node.Children, child)

				// Propagate root connection if current node is connected
				if node.ConnectedToRoot {
					p.propagateConnectionToRoot(child, results, nReader)
				}
			}
			delete(p.waitingChildren, id)
		}
		p.waitingChildrenMtx.Unlock()
	}
}

func (p *NotionImporter) propagateConnectionToRoot(node *PageNode, results chan<- *connectors.Record, nReader *int) {
	if node.ConnectedToRoot {
		return
	}
	node.ConnectedToRoot = true

	if node.Page.Object != "block" {
		pageName := node.Page.Object + ".json"
		results <- connectors.NewRecord(GetPathToRoot(node), "", objects.FileInfo{Lname: node.Page.ID, Lmode: os.ModeDir | 0700, LmodTime: time.Time{}}, nil, nil)
		results <- connectors.NewRecord(GetPathToRoot(node)+"/"+pageName, "", objects.FileInfo{Lname: pageName}, nil, func() (io.ReadCloser, error) {
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
}
