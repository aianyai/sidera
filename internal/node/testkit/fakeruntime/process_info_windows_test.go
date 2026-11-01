package fakeruntime_test

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
)

func TestProcessInfoIsReadableWhileThePublisherHoldsItsRenameHandle(t *testing.T) {
	e := newProcEnv(t)
	s, _ := e.create(1, "sess-1")
	path, err := syscall.UTF16PtrFromString(filepath.Join(e.dir, s.Ref()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	// MoveFileEx publishes the new name before closing its DELETE-access handle.
	// Hold the same access to make that publication window deterministic.
	const deleteAccess = 0x00010000
	h, err := syscall.CreateFile(path, deleteAccess,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.CloseHandle(h) })
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	again, err := e.launcher.Create(ctx, fakeruntime.CreateRequest{
		Attempt: attempt(1), Endpoint: "sess-1", Preset: "default",
		FirstAttempt: attempt(1), FirstPrompt: "hello",
	})
	if err != nil {
		t.Fatalf("Create while publication handle is open: %v", err)
	}
	if again.Ref() != s.Ref() || again.PID() != s.PID() || again.StartKey() != s.StartKey() || !again.Alive() {
		t.Fatal("replay did not return the same live native session")
	}
	found, ok := e.launcher.Find(s.Ref())
	if !ok || found.PID() != s.PID() || found.StartKey() != s.StartKey() || !found.Alive() {
		t.Fatal("Find did not return the published live native session")
	}
}
