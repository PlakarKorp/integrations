package exporter

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
)

// fakeInstanceServer stubs the two InstanceServer methods Restore touches.
// Every other method panics via the embedded nil interface, which is exactly
// what we want: the test fails loudly if Restore starts calling anything new.
type fakeInstanceServer struct {
	incus.InstanceServer

	getInstanceErr error // returned by GetInstance (nil = instance exists)
	createErr      error // returned by CreateInstanceFromBackup
	createCalled   bool
	targetUsed     string // member passed to UseTarget, if any
}

func (f *fakeInstanceServer) UseTarget(name string) incus.InstanceServer {
	f.targetUsed = name
	return f
}

func (f *fakeInstanceServer) GetInstance(name string) (*api.Instance, string, error) {
	if f.getInstanceErr != nil {
		return nil, "", f.getInstanceErr
	}
	return &api.Instance{Name: name}, "etag", nil
}

func (f *fakeInstanceServer) CreateInstanceFromBackup(args incus.InstanceBackupArgs) (incus.Operation, error) {
	f.createCalled = true
	return nil, f.createErr
}

func TestRestoreRejectsExistingInstanceName(t *testing.T) {
	fake := &fakeInstanceServer{} // GetInstance succeeds: name is taken
	sink := &incusSink{server: fake}

	err := sink.Restore(context.Background(), "web-1", strings.NewReader(""))
	if err == nil {
		t.Fatal("Restore on an existing instance name: got nil error, want error")
	}
	if !strings.Contains(err.Error(), "web-1") || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error %q should name the instance and say it already exists", err)
	}
	if fake.createCalled {
		t.Fatal("CreateInstanceFromBackup was called despite the name being taken: the whole tarball would be uploaded for nothing")
	}
}

func TestRestoreExistingInstanceErrorMentionsProject(t *testing.T) {
	fake := &fakeInstanceServer{}
	sink := &incusSink{server: fake, project: "tenant-a"}

	err := sink.Restore(context.Background(), "web-1", strings.NewReader(""))
	if err == nil {
		t.Fatal("Restore on an existing instance name: got nil error, want error")
	}
	if !strings.Contains(err.Error(), "tenant-a") {
		t.Fatalf("error %q should mention the project when one is configured", err)
	}
}

func TestRestoreProceedsWhenInstanceNotFound(t *testing.T) {
	sentinel := errors.New("create failed")
	fake := &fakeInstanceServer{
		getInstanceErr: api.StatusErrorf(http.StatusNotFound, "Instance not found"),
		createErr:      sentinel,
	}
	sink := &incusSink{server: fake}

	err := sink.Restore(context.Background(), "web-1", strings.NewReader(""))
	if !fake.createCalled {
		t.Fatal("CreateInstanceFromBackup was not called after a clean not-found pre-check")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("Restore should surface CreateInstanceFromBackup's error, got %v", err)
	}
}

// The target option must pin the create call to the given cluster
// member, and stay out of the way when unset.
func TestRestoreUsesTargetMember(t *testing.T) {
	notFound := api.StatusErrorf(http.StatusNotFound, "Instance not found")
	// A non-nil create error makes the fake's (nil) Operation unused, so
	// Restore returns right after the call under test.
	sentinel := errors.New("create failed")

	fake := &fakeInstanceServer{getInstanceErr: notFound, createErr: sentinel}
	sink := &incusSink{server: fake, target: "ha1"}
	_ = sink.Restore(context.Background(), "web-1", strings.NewReader(""))
	if !fake.createCalled || fake.targetUsed != "ha1" {
		t.Fatalf("create called=%v target=%q, want create via UseTarget(\"ha1\")", fake.createCalled, fake.targetUsed)
	}

	fake = &fakeInstanceServer{getInstanceErr: notFound, createErr: sentinel}
	sink = &incusSink{server: fake}
	_ = sink.Restore(context.Background(), "web-1", strings.NewReader(""))
	if !fake.createCalled || fake.targetUsed != "" {
		t.Fatalf("create called=%v target=%q, want create without UseTarget", fake.createCalled, fake.targetUsed)
	}
}

func TestRestoreProceedsWhenPreCheckFails(t *testing.T) {
	// A pre-check failure other than 404 (permissions, transient API error)
	// must not block the restore: it is best-effort only, the create call
	// will report the real problem if there is one.
	sentinel := errors.New("create failed")
	fake := &fakeInstanceServer{
		getInstanceErr: api.StatusErrorf(http.StatusInternalServerError, "boom"),
		createErr:      sentinel,
	}
	sink := &incusSink{server: fake}

	err := sink.Restore(context.Background(), "web-1", strings.NewReader(""))
	if !fake.createCalled {
		t.Fatal("CreateInstanceFromBackup was not called after a non-404 pre-check failure")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("Restore should surface CreateInstanceFromBackup's error, got %v", err)
	}
}
