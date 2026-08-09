package oauth2utils

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/oauth2/endpoints"
	"log"
	"os"
	"os/user"
	"path/filepath"

	"golang.org/x/oauth2"

	"github.com/studio-b12/gowebdav"
)

type OAuthProvider struct {
	Name   string
	Config *oauth2.Config
}

func (p *OAuthProvider) GetClient(url string) *gowebdav.Client {
	tokenFile := tokenCacheFile(p.Name)
	tok, err := tokenFromFile(tokenFile)
	if err != nil {
		tok = getTokenFromWeb(p.Config)
		saveToken(tokenFile, tok)
	}
	httpClient := p.Config.Client(context.Background(), tok)

	c := gowebdav.NewClient(url, "", "")
	c.SetTransport(httpClient.Transport)
	return c
}

func getTokenFromWeb(config *oauth2.Config) *oauth2.Token { //TODO: look if this CLI can be automatised
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Open this URL:\n%v\n\n", authURL)

	var authCode string
	fmt.Print("Enter the authorization code: ")
	fmt.Scan(&authCode)

	tok, err := config.Exchange(context.Background(), authCode)
	if err != nil {
		log.Fatalf("Token exchange failed: %v", err)
	}
	return tok
}

func tokenCacheFile(service string) string {
	usr, _ := user.Current()
	return filepath.Join(usr.HomeDir, fmt.Sprintf(".oauth2_token_%s.json", service)) //TODO: use a convenient location (plakar cache dir)
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

func saveToken(path string, token *oauth2.Token) {
	f, _ := os.Create(path)
	defer f.Close()
	json.NewEncoder(f).Encode(token)
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
