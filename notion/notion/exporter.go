package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"

	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/exporter"

	"regexp"
	"strings"
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

func normalizeUUID(id string) string {
	// if it already a valid dashed UUID, return it
	matched, _ := regexp.MatchString(`^[0-9a-fA-F\-]{36}$`, id)
	if matched {
		return id
	}

	id = strings.ReplaceAll(id, "-", "")
	if len(id) != 32 {
		return id
	}
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
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
	rootID = normalizeUUID(rootID)

	return &NotionExporter{
		token:  token,
		rootID: rootID, //rootID must be an existing page ID, this is the page where the files will be exported
	}, nil
}

func (n *NotionExporter) Root(ctx context.Context) (string, error) {
	return "", nil
}

func (n *NotionExporter) CreateDirectory(ctx context.Context, pathname string) error {
	return os.MkdirAll(path.Join(tempDir, pathname), 0700)
}

func (n *NotionExporter) StoreFile(ctx context.Context, pathname string, fp io.Reader, size int64) error {
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

func (n *NotionExporter) SetPermissions(ctx context.Context, pathname string, fileinfo *objects.FileInfo) error {
	return nil
}

func (n *NotionExporter) CreateLink(ctx context.Context, oldname string, newname string, ltype exporter.LinkType) error {
	return errors.ErrUnsupported
}

func (n *NotionExporter) Close(ctx context.Context) error {
	err := n.export()
	if err != nil {
		log.Printf("failed to close exporter %v", err)
		return fmt.Errorf("failed to export: %w", err)
	}
	return os.RemoveAll(tempDir)
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

func (n *NotionExporter) createPage(payload []byte) (string, error) {
	url := fmt.Sprintf("%s/pages", NotionURL)
	jsonData, err := n.makeRequest("POST", url, payload)
	if err != nil {
		return "", err
	}
	return jsonData["id"].(string), nil
}

func (n *NotionExporter) createDatabase(payload []byte) (string, error) {
	url := fmt.Sprintf("%s/databases", NotionURL)
	jsonData, err := n.makeRequest("POST", url, payload)
	if err != nil {
		return "", fmt.Errorf("failed to create database: %w", err)
	}
	return jsonData["id"].(string), nil
}

func (n *NotionExporter) addBlock(payload []byte, pageID string) (string, error) {
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

func (n *NotionExporter) createPageWithBlocks(payload map[string]any, children []map[string]any, pathTo string) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	log.Printf("Creating page with data: %s", string(data))

	newPageID, err := n.createPage(data)
	if err != nil {
		return fmt.Errorf("failed to create page: %w", err)
	}
	log.Printf("Created page with ID: %s", newPageID)

	return n.addAllBlocks(children, newPageID, pathTo)
}

func (n *NotionExporter) createDatabaseWithEntries(payload map[string]any, dbPath string) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	newDatabaseID, err := n.createDatabase(data)
	if err != nil {
		return fmt.Errorf("failed to create database: %w", err)
	}
	log.Printf("Created database with ID: %s", newDatabaseID)
	return n.addEntries(newDatabaseID, dbPath)
}

func (n *NotionExporter) exportPageFromFile(pathname, parentType, parentID string) error {
	payload, err := loadJSONFromFile(pathname)
	if err != nil {
		return err
	}
	payload, children, err := preparePayload(payload, parentType, parentID)
	if err != nil {
		return err
	}
	return n.createPageWithBlocks(payload, children, path.Dir(pathname))
}

func (n *NotionExporter) exportDatabaseFromFile(pathname, parentType, parentID string) error {
	payload, err := loadJSONFromFile(pathname)
	if err != nil {
		return err
	}

	delete(payload, "id")
	payload["parent"] = map[string]any{
		"type":     parentType,
		parentType: parentID,
	}

	return n.createDatabaseWithEntries(payload, path.Dir(pathname))
}

func (n *NotionExporter) addAllBlocks(jsonData []map[string]any, newID, pathTo string) error {
	for _, block := range jsonData {
		dir := path.Join(pathTo, block["id"].(string))

		if block["type"] == "image" { //TODO: handle images, and other more block types
			continue
		}

		if block["type"] == "child_page" {
			err := n.exportPageFromFile(path.Join(dir, "page.json"), "page_id", newID)
			if err != nil {
				return fmt.Errorf("failed to export child page: %w", err)
			}
		} else if block["type"] == "child_database" {
			err := n.exportDatabaseFromFile(path.Join(dir, "database.json"), "page_id", newID)
			if err != nil {
				return fmt.Errorf("failed to export child database: %w", err)
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
			newBlockId, err := n.addBlock(data, newID)
			if err != nil {
				return fmt.Errorf("failed to patch block: %w", err)
			}

			if block["type"] == "toggle" {
				pathname := path.Join(dir, "blocks.json")
				f, err := os.Open(pathname)
				if err != nil {
					return fmt.Errorf("failed to open %s: %w", pathname, err)
				}
				defer f.Close()

				var data []map[string]any
				if err := json.NewDecoder(f).Decode(&data); err != nil {
					return fmt.Errorf("failed to decode JSON from %s: %w", pathname, err)
				}
				if len(data) > 0 {
					err = n.addAllBlocks(data, newBlockId, path.Dir(pathname))
					if err != nil {
						return fmt.Errorf("failed to add toggle children: %w", err)
					}
				}
			}
		}
	}
	return nil
}

func (n *NotionExporter) addEntries(newID, pathTo string) error {
	entries, err := os.ReadDir(pathTo)
	if err != nil {
		return fmt.Errorf("failed to read entries from %s: %w", pathTo, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := path.Join(pathTo, entry.Name())

		err := n.exportPageFromFile(path.Join(dir, "page.json"), "database_id", newID)
		if err != nil {
			return fmt.Errorf("failed to export page: %w", err)
		}
	}
	return nil
}

func (n *NotionExporter) export() error {
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
			err := n.exportPageFromFile(path.Join(dir, "page.json"), "page_id", n.rootID)
			if err != nil {
				return fmt.Errorf("failed to export page: %w", err)
			}

		} else if entry["object"] == "database" {
			err := n.exportDatabaseFromFile(path.Join(dir, "database.json"), "page_id", n.rootID)
			if err != nil {
				return fmt.Errorf("failed to export database: %w", err)
			}
		} else {
			return fmt.Errorf("unsupported object type: %s", entry["object"])
		}
	}
	return nil
}
