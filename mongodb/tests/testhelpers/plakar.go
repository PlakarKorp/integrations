package testhelpers

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

// plakarSHA is the plakar commit installed in the test image.
const plakarSHA = "6574a2c153c0b35db2ed98a2490aa9dda86bddbe"

// repoRoot returns the integration's root directory, wherever `go test` runs.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	// file is tests/testhelpers/plakar.go.
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// StartPlakarContainer starts a container from the plakar test image, which
// has plakar and the mongodb plugin installed, attached to net. files are
// copied into the container before it starts. The container is terminated
// when the test ends.
func StartPlakarContainer(ctx context.Context, t *testing.T, net *testcontainers.DockerNetwork, files []testcontainers.ContainerFile) testcontainers.Container {
	t.Helper()
	sha := plakarSHA
	image := MongoImage

	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    repoRoot(),
			Dockerfile: "tests/plakar.Dockerfile",
			BuildArgs: map[string]*string{
				"PLAKAR_SHA":  &sha,
				"MONGO_IMAGE": &image,
			},
			// A fixed name, so each run replaces the image instead of
			// leaving another one behind.
			Repo:      "plakar-mongodb-test",
			Tag:       plakarSHA,
			KeepImage: true,
		},
		Cmd:      []string{"sleep", "infinity"},
		Networks: []string{net.Name},
		Files:    files,
		// The tests use a -plaintext store, which plakar refuses without it.
		Env: map[string]string{"PLAKAR_INSECURE_PLAINTEXT": "1"},
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start plakar container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	return container
}
