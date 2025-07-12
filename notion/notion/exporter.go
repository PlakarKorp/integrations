package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/exporter"
	"io"
	"log"
	"net/http"
	"os"
	"path"
)

func DebugResponse(resp *http.Response) {
	// debug
	log.Printf("failed to upload file: %d", resp.StatusCode)
	// Read the response body for more details
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	var prettyJSON bytes.Buffer
	err = json.Indent(&prettyJSON, b, "", "\t")
	if err != nil {
		return
	}
	log.Printf("Error response: %s\n", prettyJSON.String())
	// end
}

const tempDir = "/tmp/plakar-notion-restore"

type NotionExporter struct {
	token  string
	rootID string //TODO : change this to a user friendly name (e.g. "My Notion Page" instead of "1234567890abcdef")
}

func NewNotionExporter(ctx context.Context, options *exporter.Options, name string, config map[string]string) (exporter.Exporter, error) {
	token, ok := config["token"]
	if !ok {
		return nil, fmt.Errorf("missing token in config")
	}
	rootID, ok := config["rootID"]
	if !ok {
		return nil, fmt.Errorf("missing rootID in config")
	}

	return &NotionExporter{
		token:  token,
		rootID: rootID, //rootID must be an existing page ID, this is the page where the files will be exported
	}, nil
}

func (n *NotionExporter) Root() string {
	return ""
}

func (n *NotionExporter) CreateDirectory(pathname string) error {
	return os.MkdirAll(path.Join(tempDir, pathname), 0700)
}

func (n *NotionExporter) StoreFile(pathname string, fp io.Reader, size int64) error {
	dest := path.Join(tempDir, pathname)
	f, err := os.Create(dest)
	defer f.Close()

	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", dest, err)
	}
	if _, err := io.Copy(f, fp); err != nil {
		return fmt.Errorf("failed to copy data to file %s: %w", dest, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync file %s: %w", dest, err)
	}
	return nil
}

func (n *NotionExporter) SetPermissions(pathname string, fileinfo *objects.FileInfo) error {
	return nil
}

func (n *NotionExporter) Close() error {
	err := n.Export()
	if err != nil {
		log.Printf("failed to close exporter %v", err)
		return fmt.Errorf("failed to export: %w", err)
	}
	return os.RemoveAll(tempDir) // Clean up the temporary directory
}

func (n *NotionExporter) makeRequest(method, url string, payload []byte) (map[string]any, error) {
	req, err := http.NewRequest(method, url, io.NopCloser(bytes.NewReader(payload)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+n.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Notion-Version", NotionVersionHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		DebugResponse(resp)
		return nil, fmt.Errorf("request failed: status code %d", resp.StatusCode)
	}
	jsonData := map[string]any{}
	err = json.NewDecoder(resp.Body).Decode(&jsonData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return jsonData, nil
}

func (n *NotionExporter) CreatePage(payload []byte) (string, error) {
	url := fmt.Sprintf("%s/pages", NotionURL)
	jsonData, err := n.makeRequest("POST", url, payload)
	if err != nil {
		return "", err
	}
	return jsonData["id"].(string), nil
}

func (n *NotionExporter) CreateDatabase(payload []byte) (string, error) {
	url := fmt.Sprintf("%s/databases", NotionURL)
	jsonData, err := n.makeRequest("POST", url, payload)
	if err != nil {
		return "", fmt.Errorf("failed to create database: %w", err)
	}
	return jsonData["id"].(string), nil
}

func (n *NotionExporter) AddBlock(payload []byte, pageID string) (string, error) {
	url := fmt.Sprintf("%s/blocks/%s/children", NotionURL, pageID)
	jsonData, err := n.makeRequest("PATCH", url, payload)
	if err != nil {
		return "", err
	}
	blockID := jsonData["results"].([]any)[0].(map[string]any)["id"].(string) // considering blocks are added one by one
	//TODO: considering to handle multiple blocks in the future to avoid too many requests
	return blockID, nil
}

func loadJSONFromFile(filePath string) (map[string]any, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", filePath, err)
	}
	defer f.Close()

	var data map[string]any
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode JSON from %s: %w", filePath, err)
	}
	return data, nil
}

func preparePayload(payload map[string]any, parentType, parentID string) (cleanedPayload map[string]any, children []map[string]any, err error) {
	childrenRaw, ok := payload["children"].([]any)
	if !ok {
		childrenRaw = []any{}
	}
	children = make([]map[string]any, len(childrenRaw))
	for i, child := range childrenRaw {
		children[i] = child.(map[string]any)
	}

	// Remove ID and replace parent + children
	delete(payload, "id")
	payload["children"] = []any{}
	payload["parent"] = map[string]any{
		"type":     parentType,
		parentType: parentID,
	}
	return payload, children, nil
}

// AddAllBlocks adds all blocks to the page with the given ID
func (n *NotionExporter) AddAllBlocks(jsonData []map[string]any, newID, pathTo string) error {
	for _, block := range jsonData { //PATCH each block to the page
		dir := path.Join(pathTo, block["id"].(string))

		if block["type"] == "image" {
			continue
		}

		if block["type"] == "child_page" {
			payload, err := loadJSONFromFile(path.Join(dir, "page.json"))
			if err != nil {
				return err
			}

			payload, children, err := preparePayload(payload, "page_id", newID)

			data, err := json.Marshal(payload)

			log.Printf("Creating page with data: %s", string(data))

			if err != nil {
				return fmt.Errorf("failed to marshal JSON: %w", err)
			}
			newPageID, err := n.CreatePage(data)
			if err != nil {
				return fmt.Errorf("failed to create page: %w", err)
			}
			log.Printf("Created page with ID: %s", newPageID)

			err = n.AddAllBlocks(children, newPageID, dir)
			if err != nil {
				return fmt.Errorf("failed to add blocks to page %s: %w", newPageID, err)
			}

		} else if block["type"] == "child_database" {
			payload, err := loadJSONFromFile(path.Join(dir, "database.json"))
			if err != nil {
				return err
			}

			delete(payload, "id")
			payload["parent"] = map[string]any{
				"type":    "page_id",
				"page_id": newID,
			}

			data, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to marshal JSON: %w", err)
			}
			newDatabaseID, err := n.CreateDatabase(data)
			if err != nil {
				return fmt.Errorf("failed to create database: %w", err)
			}
			log.Printf("Created database with ID: %s", newDatabaseID)

			err = n.AddEntries(newDatabaseID, dir)
			if err != nil {
				return fmt.Errorf("failed to add entries: %w", err)
			}

		} else { //standard block
			payload := map[string]any{
				"children": []any{
					block,
				},
			}
			data, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to marshal JSON: %w", err)
			}
			_, err = n.AddBlock(data, newID)
			if err != nil {
				return fmt.Errorf("failed to patch block: %w", err)
			}
		}
	}
	return nil
}

func (n *NotionExporter) AddEntries(newID, pathTo string) error {
	entries, err := os.ReadDir(pathTo)
	if err != nil {
		return fmt.Errorf("failed to read entries from %s: %w", pathTo, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := path.Join(pathTo, entry.Name())
		payload, err := loadJSONFromFile(path.Join(dir, "page.json")) // entry of a database is a page type
		if err != nil {
			return err
		}

		payload, children, err := preparePayload(payload, "database_id", newID)

		data, err := json.Marshal(payload)

		log.Printf("Creating page with data: %s", string(data))

		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		newPageID, err := n.CreatePage(data)
		if err != nil {
			return fmt.Errorf("failed to create page: %w", err)
		}
		log.Printf("Created page with ID: %s", newPageID)

		err = n.AddAllBlocks(children, newPageID, dir)
		if err != nil {
			return fmt.Errorf("failed to add blocks to page %s: %w", newPageID, err)
		}

	}
	return nil
}

func (n *NotionExporter) Export() error {
	pathname := path.Join(tempDir, "content.json")
	file, err := os.Open(pathname)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", pathname, err)
	}
	defer file.Close()

	var jsonData []map[string]any
	if err := json.NewDecoder(file).Decode(&jsonData); err != nil {
		return fmt.Errorf("failed to decode JSON from file %s: %w", pathname, err)
	}

	for _, entry := range jsonData {
		dir := path.Join(tempDir, entry["id"].(string))

		if entry["object"] == "page" {
			payload, err := loadJSONFromFile(path.Join(dir, "page.json"))
			if err != nil {
				return err
			}

			payload, children, err := preparePayload(payload, "page_id", n.rootID) //TODO: look if we always want to use a page as parent here

			data, err := json.Marshal(payload)

			log.Printf("Creating page with data: %s", string(data))

			if err != nil {
				return fmt.Errorf("failed to marshal JSON: %w", err)
			}
			newPageID, err := n.CreatePage(data)
			if err != nil {
				return fmt.Errorf("failed to create page: %w", err)
			}
			log.Printf("Created page with ID: %s", newPageID)

			err = n.AddAllBlocks(children, newPageID, dir)
			if err != nil {
				return fmt.Errorf("failed to add blocks to page %s: %w", newPageID, err)
			}

		} else if entry["object"] == "database" {
			payload, err := loadJSONFromFile(path.Join(dir, "database.json"))
			if err != nil {
				return err
			}

			delete(payload, "id")
			payload["parent"] = map[string]any{
				"type":    "page_id",
				"page_id": n.rootID,
			}

			data, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to marshal JSON: %w", err)
			}
			newDatabaseID, err := n.CreateDatabase(data)
			if err != nil {
				return fmt.Errorf("failed to create database: %w", err)
			}
			log.Printf("Created database with ID: %s", newDatabaseID)

			err = n.AddEntries(newDatabaseID, dir)
			if err != nil {
				return fmt.Errorf("failed to add entries: %w", err)
			}

		} else {
			return fmt.Errorf("unsupported object type: %s", entry["object"])
		}
	}
	return nil
}
