package oauth2utils

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"golang.org/x/oauth2/endpoints"

	"golang.org/x/oauth2"

	"github.com/studio-b12/gowebdav"
)

type OAuthProvider struct {
	Name   string
	Config *oauth2.Config
}

func (p *OAuthProvider) GetClient(url string) (*gowebdav.Client, error) {
	tokenFile, err := tokenCacheFile(p.Name)
	if err != nil {
		return nil, err
	}

	tok, err := tokenFromFile(tokenFile)
	if err != nil {
		tok, err = getTokenFromWeb(p.Config)
		if err != nil {
			return nil, err
		}
		if err := saveToken(tokenFile, tok); err != nil {
			return nil, err
		}
	}
	httpClient := p.Config.Client(context.Background(), tok)

	c := gowebdav.NewClient(url, "", "")
	c.SetTransport(httpClient.Transport)
	return c, nil
}

func getTokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) { //TODO: look if this CLI can be automatised
	// The state parameter has to be unpredictable and checked on return;
	// it used to be the constant "state-token", which authenticates nothing.
	state, err := randomState()
	if err != nil {
		return nil, err
	}

	authURL := config.AuthCodeURL(state, oauth2.AccessTypeOffline)
	fmt.Printf("Open this URL:\n%v\n\n", authURL)

	var authCode string
	fmt.Print("Enter the authorization code: ")
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, fmt.Errorf("reading authorization code: %w", err)
	}

	tok, err := config.Exchange(context.Background(), authCode)
	if err != nil {
		// This used to be log.Fatalf, which killed the host process from
		// inside a library.
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}
	return tok, nil
}

func randomState() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating oauth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func tokenCacheFile(service string) (string, error) {
	usr, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("cannot determine the current user: %w", err)
	}
	return filepath.Join(usr.HomeDir, fmt.Sprintf(".oauth2_token_%s.json", service)), nil //TODO: use a convenient location (plakar cache dir)
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// saveToken writes the token 0600.
//
// It used to use os.Create, which is 0666 before umask and so typically 0644:
// a long-lived refresh token readable by every user on the machine.  The
// errors from creating, encoding and closing were all discarded too, so a
// failure here was silent and the next run just repeated the browser flow.
func saveToken(path string, token *oauth2.Token) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}

	if err := json.NewEncoder(f).Encode(token); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}

	// A pre-existing file keeps its old mode through O_CREATE, so state it.
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}

	return nil
}

func GetOAuth2Endpoint(provider string) (oauth2.Endpoint, error) {
	switch provider {
	case "google":
		return endpoints.Google, nil
	//case "microsoft": //TODO: test it
	//	return endpoints.Microsoft, nil
	//case "apple": //TODO: test it
	//	return endpoints.Apple, nil
	//TODO: add more providers as needed
	default:
		return oauth2.Endpoint{}, fmt.Errorf("unknown provider: %s", provider)
	}
}

func GetOAuth2Scopes(provider string) ([]string, error) {
	switch provider {
	case "google":
		return []string{"https://www.googleapis.com/auth/calendar"}, nil
	//case "microsoft": //TODO: test it
	//	return []string{"https://graph.microsoft.com/Calendars.ReadWrite"}, nil
	//case "apple": //TODO: test it
	//	return []string{"https://p12.plakar.app/calendars.readwrite"}, nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

func GetOAuth2Url(provider, username string) string {
	switch provider {
	case "google":
		return fmt.Sprintf("https://apidata.googleusercontent.com/caldav/v2/%s/events", username)
	//case "microsoft": //TODO: test it
	//	return fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/calendars", username)
	//case "apple": //TODO: test it
	//	return fmt.Sprintf("https://p12.plakar.app/calendars/%s/events", username)
	default:
		return ""
	}
}
