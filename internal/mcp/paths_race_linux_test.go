//go:build linux

package mcp

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// Exercise the exact historical TOCTOU shape: a path component alternates
// between an in-root directory and a symlink to an outside directory while the
// MCP side opens it. Errors are expected during the swap, but any successful
// descriptor must refer to the in-root file.
func TestOpenUploadDirectorySwapNeverEscapesRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	slot := filepath.Join(root, "slot")
	parked := filepath.Join(root, "parked")
	if err := os.Mkdir(slot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slot, "value"), []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "value"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	files := newTestLocalFiles(t, root, "confined")

	var wg sync.WaitGroup
	stop := make(chan struct{})
	swapErr := make(chan error, 1)
	wg.Go(func() {
		defer close(stop)
		for i := 0; i < 2_000; i++ {
			if err := os.Rename(slot, parked); err != nil {
				swapErr <- err
				return
			}
			if err := os.Symlink(outside, slot); err != nil {
				swapErr <- err
				return
			}
			runtime.Gosched()
			if err := os.Remove(slot); err != nil {
				swapErr <- err
				return
			}
			if err := os.Rename(parked, slot); err != nil {
				swapErr <- err
				return
			}
		}
	})

	for {
		select {
		case <-stop:
			wg.Wait()
			select {
			case err := <-swapErr:
				t.Fatalf("swap loop: %v", err)
			default:
			}
			return
		default:
		}
		file, _, err := files.openUpload(filepath.Join("slot", "value"))
		if err != nil {
			runtime.Gosched()
			continue
		}
		data, readErr := io.ReadAll(file)
		file.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(data) != "inside" {
			t.Fatalf("descriptor escaped root and read %q", data)
		}
	}
}
