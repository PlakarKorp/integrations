/*
 * Copyright (c) 2025 Gilles Chehade <gilles@poolp.org>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package sftp

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestSetupKnownHosts_EmptyHostKey(t *testing.T) {
	params := map[string]string{
		"host_key": "",
	}

	path, err := setupKnownHosts(params)
	require.NoError(t, err)
	assert.Equal(t, "", path)
}

func TestSetupKnownHosts_CreatesFile(t *testing.T) {
	hostKey := "example.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC..."
	params := map[string]string{
		"host_key": hostKey,
	}

	path, err := setupKnownHosts(params)
	require.NoError(t, err)
	require.NotEmpty(t, path)

	// Verify file exists
	_, err = os.Stat(path)
	require.NoError(t, err)

	// Verify file contains the host key with newline
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, hostKey+"\n", string(content))

	// Cleanup
	os.Remove(path)
}

func TestSetupKnownHosts_TemporaryFileLocation(t *testing.T) {
	hostKey := "example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHKS..."
	params := map[string]string{
		"host_key": hostKey,
	}

	path, err := setupKnownHosts(params)
	require.NoError(t, err)

	// Verify file is in temp directory
	tempDir := os.TempDir()
	assert.True(t, strings.HasPrefix(path, tempDir))

	// Verify filename contains known_hosts
	filename := path[len(tempDir):]
	fmt.Printf("Temporary known_hosts file created: %s\n", path)
	assert.Contains(t, filename, "known_hosts")

	// Cleanup
	os.Remove(path)
}

func TestSshArgs_WithHostKey(t *testing.T) {
	endpoint, _ := url.Parse("sftp://user@example.com/path")
	params := map[string]string{
		"host_key": "example.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC...",
	}

	args := sshArgs(endpoint, params)

	// Verify the arguments contain the necessary flags
	foundUserKnownHostsFile := false
	foundStrictHostKeyChecking := false

	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-o" && strings.HasPrefix(args[i+1], "UserKnownHostsFile=") {
			foundUserKnownHostsFile = true
		}
		if args[i] == "-o" && args[i+1] == "StrictHostKeyChecking=yes" {
			foundStrictHostKeyChecking = true
		}
	}

	assert.True(t, foundUserKnownHostsFile, "sshArgs should include UserKnownHostsFile when host_key is provided")
	assert.True(t, foundStrictHostKeyChecking, "sshArgs should include StrictHostKeyChecking=yes when host_key is provided")

	// Cleanup any created files
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-o" && strings.HasPrefix(args[i+1], "UserKnownHostsFile=") {
			parts := strings.Split(args[i+1], "=")
			if len(parts) == 2 {
				os.Remove(parts[1])
			}
		}
	}
}

func TestSshArgs_WithoutHostKey_IgnoresHostKeyChecking(t *testing.T) {
	endpoint, _ := url.Parse("sftp://user@example.com/path")
	params := map[string]string{}

	args := sshArgs(endpoint, params)

	// Verify UserKnownHostsFile is NOT in args
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-o" {
			assert.False(t, strings.HasPrefix(args[i+1], "UserKnownHostsFile="))
		}

		if args[i] == "-o" {
			assert.False(t, strings.HasPrefix(args[i+1], "StrictHostKeyChecking="))
		}
	}
}

// precedence (StrictHostKeyChecking=yes rather than =no).
func TestSshArgs_HostKeyTakesPrecedenceOverInsecure(t *testing.T) {
	endpoint, _ := url.Parse("sftp://user@example.com/path")
	params := map[string]string{
		"host_key":                 "example.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC...",
		"insecure_ignore_host_key": "true",
	}

	args := sshArgs(endpoint, params)

	// Find StrictHostKeyChecking setting
	var strictHostKeyValue string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-o" && strings.HasPrefix(args[i+1], "StrictHostKeyChecking=") {
			parts := strings.Split(args[i+1], "=")
			if len(parts) == 2 {
				strictHostKeyValue = parts[1]
			}
		}
	}

	// When host_key is provided, it should be "yes", not "no"
	assert.Equal(t, "yes", strictHostKeyValue, "host_key should take precedence and set StrictHostKeyChecking=yes")

	// Cleanup
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-o" && strings.HasPrefix(args[i+1], "UserKnownHostsFile=") {
			parts := strings.Split(args[i+1], "=")
			if len(parts) == 2 {
				os.Remove(parts[1])
			}
		}
	}
}

func TestSshArgs_InsecureMode(t *testing.T) {
	endpoint, _ := url.Parse("sftp://user@example.com/path")
	params := map[string]string{
		"insecure_ignore_host_key": "true",
	}

	args := sshArgs(endpoint, params)

	// Verify StrictHostKeyChecking=no is present
	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-o" && args[i+1] == "StrictHostKeyChecking=no" {
			found = true
			break
		}
	}

	assert.True(t, found, "sshArgs should include StrictHostKeyChecking=no in insecure mode")
}

func TestSshArgs_BatchMode(t *testing.T) {
	endpoint, _ := url.Parse("sftp://user@example.com/path")
	params := map[string]string{}

	args := sshArgs(endpoint, params)

	// Verify BatchMode=yes is present
	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-o" && args[i+1] == "BatchMode=yes" {
			found = true
			break
		}
	}

	assert.True(t, found, "sshArgs should always include BatchMode=yes")
}

// host_key along with other parameters like identity and username.
func TestSshArgs_WithMultipleParams(t *testing.T) {
	endpoint, _ := url.Parse("sftp://example.com:2222/path")
	params := map[string]string{
		"host_key": "example.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC...",
		"identity": "/home/user/.ssh/id_rsa",
		"username": "testuser",
	}

	args := sshArgs(endpoint, params)

	// Verify key elements are present
	hasHostKeyFile := false
	hasIdentity := false
	hasUsername := false
	hasPort := false

	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-o" && strings.HasPrefix(args[i+1], "UserKnownHostsFile=") {
			hasHostKeyFile = true
		}
		if args[i] == "-i" && args[i+1] == "/home/user/.ssh/id_rsa" {
			hasIdentity = true
		}
		if args[i] == "-l" && args[i+1] == "testuser" {
			hasUsername = true
		}
		if args[i] == "-p" && args[i+1] == "2222" {
			hasPort = true
		}
	}

	assert.True(t, hasHostKeyFile, "should include UserKnownHostsFile")
	assert.True(t, hasIdentity, "should include identity")
	assert.True(t, hasUsername, "should include username")
	assert.True(t, hasPort, "should include port")

	// Cleanup
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-o" && strings.HasPrefix(args[i+1], "UserKnownHostsFile=") {
			parts := strings.Split(args[i+1], "=")
			if len(parts) == 2 {
				os.Remove(parts[1])
			}
		}
	}
}
