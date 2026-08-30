package notion

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Client is the HTTP client every Notion request goes through.
//
// http.DefaultClient has no timeout at all, so a slow or hung endpoint stalled
// a backup indefinitely with nothing to cancel it.
var Client = &http.Client{
	Timeout: 2 * time.Minute,
}

// fetchAttachment retrieves a file Notion pointed us at.
//
// The URL comes out of an API response, which means it is chosen by whoever
// can add a block to the workspace being backed up, not by us.  It is
// restricted to https and re-checked on every redirect so it cannot be walked
// towards an internal address.
func fetchAttachment(rawurl string) (*http.Response, error) {
	if err := checkAttachmentURL(rawurl); err != nil {
		return nil, err
	}

	client := &http.Client{
		Timeout: Client.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return checkAttachmentURL(req.URL.String())
		},
	}

	return client.Get(rawurl)
}

func checkAttachmentURL(rawurl string) error {
	u, err := url.Parse(rawurl)
	if err != nil {
		return fmt.Errorf("invalid attachment URL: %w", err)
	}

	if u.Scheme != "https" {
		return fmt.Errorf("refusing to fetch attachment over %q, https required", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("attachment URL has no host")
	}

	// Only literal addresses can be checked without resolving; a name that
	// resolves to a private address is not caught here, which is why this is
	// a guard rail rather than a boundary.
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsGlobalUnicast() || ip.IsPrivate() {
			return fmt.Errorf("refusing to fetch attachment from non-public address %q", host)
		}
	}

	return nil
}
