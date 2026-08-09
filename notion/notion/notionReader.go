package notion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	NotionURL           = "https://api.notion.com/v1"
	PageSize            = 1 // Number of pages to fetch at once default is 100
	NotionVersionHeader = "2022-06-28"
)

type notionRecord struct {
	Block  json.RawMessage
	pathTo string
	EOF    bool
}

type BlockResponse struct {
	Results    []json.RawMessage `json:"results"`
	HasMore    bool              `json:"has_more"`
	NextCursor string            `json:"next_cursor"`
}

func fetchFromURL(url, token string) (map[string]any, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Notion-Version", NotionVersionHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to execute request, status code: %d", resp.StatusCode)
	}

	var result map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	delete(result, "request_id")

	return result, nil
}

//---------------
// lets break down the current NotionReader into two:
// 1. NotionReaderHeader: This will be used to read the header of the page
// 2. NotionReaderBlocks: This will be used to read the blocks of the page

type NotionReaderHeader struct {
	buf    *bytes.Buffer
	token  string
	pageID string
}

func NewNotionReaderHeader(token, pageID string) (*NotionReaderHeader, error) {
	nr := &NotionReaderHeader{
		buf:    new(bytes.Buffer),
		token:  token,
		pageID: pageID,
	}
	pageHeader, err := nr.fetchPageHeader()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch page header: %w", err)
	}

	data, err := json.Marshal(pageHeader)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal page header: %w", err)
	}
	nr.buf.Write(data)
	return nr, nil
}

func (nr *NotionReaderHeader) fetchPageHeader() (map[string]any, error) {
	url := fmt.Sprintf("%s/pages/%s", NotionURL, nr.pageID)
	return fetchFromURL(url, nr.token)
}

func (nr *NotionReaderHeader) Read(p []byte) (int, error) {
	n, err := nr.buf.Read(p)
	if n == 0 && err == io.EOF {
		return 0, io.EOF
	}
	return n, err
}

type NotionReaderBlocks struct {
	buf             *bytes.Buffer
	token           string
	pageID          string
	path            string
	cursor          string
	done            bool
	blockOpen       bool
	first           bool
	wroteFirstBlock bool
	recordChan      chan<- notionRecord // Channel to send records
}

func NewNotionReaderBlocks(token, pageID, path string, recordChan chan<- notionRecord) (*NotionReaderBlocks, error) {
	nRd := &NotionReaderBlocks{
		buf:             new(bytes.Buffer),
		token:           token,
		pageID:          pageID,
		path:            path,
		cursor:          "",
		done:            false,
		blockOpen:       false,
		first:           true,
		wroteFirstBlock: false,
		recordChan:      recordChan,
	}

	nRd.buf.WriteString("[")
	return nRd, nil
}

func (nr *NotionReaderBlocks) fetchBlocks() (*BlockResponse, error) {
	url := fmt.Sprintf("%s/blocks/%s/children?page_size=%d", NotionURL, nr.pageID, PageSize)
	if nr.cursor != "" {
		url += fmt.Sprintf("&start_cursor=%s", nr.cursor)
	}

	rawResponse, err := fetchFromURL(url, nr.token)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch blocks: %w", err)
	}

	// Marshal the map back to JSON
	rawJSON, err := json.Marshal(rawResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal raw response: %w", err)
	}

	// Decode the JSON into a BlockResponse
	var blockResp BlockResponse
	if err := json.Unmarshal(rawJSON, &blockResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block response: %w", err)
	}

	return &blockResp, nil
}

func (nr *NotionReaderBlocks) Read(p []byte) (int, error) {
	for nr.buf.Len() < len(p) && !nr.done {
		if nr.first {
			nr.openJSONArray()
		}

		blockResp, err := nr.fetchBlocks()
		if err != nil {
			return 0, fmt.Errorf("failed to fetch blocks: %w", err)
		}

		nr.writeBlocksToBuffer(blockResp.Results)

		// Send each block as a notionRecord to the channel
		for _, block := range blockResp.Results {
			nr.recordChan <- notionRecord{Block: block, EOF: false, pathTo: nr.path}
		}

		if !blockResp.HasMore {
			nr.done = true
			// Send EOF record to indicate completion
			nr.recordChan <- notionRecord{EOF: true}
		} else {
			nr.cursor = blockResp.NextCursor
		}
	}

	if nr.done && nr.blockOpen {
		nr.closeJSONArray()
	}

	n, err := nr.buf.Read(p)
	if n == 0 && nr.done {
		return 0, io.EOF
	}
	return n, err
}

func (nr *NotionReaderBlocks) openJSONArray() {
	nr.blockOpen = true
	nr.first = false
}

func (nr *NotionReaderBlocks) closeJSONArray() {
	// Close the array and add the last brace that was removed
	nr.buf.Write([]byte("]"))
	nr.blockOpen = false
}

func (nr *NotionReaderBlocks) writeBlocksToBuffer(blocks []json.RawMessage) {
	for _, block := range blocks {
		if nr.wroteFirstBlock {
			nr.buf.WriteByte(',')
		}
		nr.buf.Write(block)
		nr.wroteFirstBlock = true
	}
}

type NotionReaderFile struct {
	headerReader *NotionReaderHeader
	blockReader  *NotionReaderBlocks
}

// NewNotionReaderFile creates a new notionReaderFile instance
func NewNotionReaderFile(token, pageID, path string, recordChan chan<- notionRecord) (*NotionReaderFile, error) {
	// Create the header reader
	headerReader, err := NewNotionReaderHeader(token, pageID)
	if err != nil {
		return nil, fmt.Errorf("failed to create NotionReaderHeader: %w", err)
	}

	//remove the closing bracket from the header and add the 'children' key
	headerReader.buf.Truncate(headerReader.buf.Len() - 1)
	headerReader.buf.WriteString(",\"children\":")

	// Create the block reader
	blockReader, err := NewNotionReaderBlocks(token, pageID, path, recordChan)
	if err != nil {
		return nil, fmt.Errorf("failed to create NotionReaderBlocks: %w", err)
	}

	return &NotionReaderFile{
		headerReader: headerReader,
		blockReader:  blockReader,
	}, nil
}

// Read reads from the header first, then falls back to the block reader
func (nrf *NotionReaderFile) Read(p []byte) (int, error) {
	// Attempt to read from the header reader
	n, err := nrf.headerReader.Read(p)
	if n > 0 || err != io.EOF {
		return n, err
	}

	// Fallback to the block reader
	n, err = nrf.blockReader.Read(p)
	if n > 0 || err != io.EOF {
		return n, err
	}

	if err == io.EOF {
		p[0] = '}'
		return 1, io.EOF
	}

	return 0, err
}

type DatabaseNotionReader struct {
	buf        *bytes.Buffer
	token      string
	databaseID string
}

func NewNotionReaderDatabase(token, databaseID string) (*DatabaseNotionReader, error) {
	dr := &DatabaseNotionReader{
		buf:        new(bytes.Buffer),
		token:      token,
		databaseID: databaseID,
	}

	// Fetch and write database properties
	if err := dr.fetchAndWriteDatabaseProperties(); err != nil {
		return nil, fmt.Errorf("failed to fetch database properties: %w", err)
	}

	return dr, nil
}

func (dr *DatabaseNotionReader) fetchAndWriteDatabaseProperties() error {
	url := fmt.Sprintf("%s/databases/%s", NotionURL, dr.databaseID)

	properties, err := fetchFromURL(url, dr.token)
	if err != nil {
		return fmt.Errorf("failed to fetch database properties: %w", err)
	}

	// Marshal the map to JSON and write to the buffer
	data, err := json.Marshal(properties)
	if err != nil {
		return fmt.Errorf("failed to marshal database properties: %w", err)
	}
	dr.buf.Write(data)

	return nil
}

func (dr *DatabaseNotionReader) Read(p []byte) (int, error) {
	n, err := dr.buf.Read(p)
	if n == 0 {
		return 0, io.EOF
	}
	return n, err
}
