package common

import (
	"time"

	"github.com/secsy/goftp"
)

func ConnectToFTP(host, username, password string) (*goftp.Client, error) {
	config := goftp.Config{
		User:     username,
		Password: password,
		Timeout:  10 * time.Second,
	}
	return goftp.DialConfig(config, host)
}
