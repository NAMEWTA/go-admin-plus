package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalStorageRequiresCanonicalAbsolutePrivateRoot(t *testing.T) {
	if _, err := NewLocalStorage("relative"); !errors.Is(err, ErrStorageRoot) {
		t.Fatalf("relative root error = %v", err)
	}

	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		linkedRoot := filepath.Join(parent, "linked")
		if err := os.Symlink(realRoot, linkedRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := NewLocalStorage(linkedRoot); !errors.Is(err, ErrStorageRoot) {
			t.Fatalf("symlink root error = %v", err)
		}
		outsideParent := filepath.Join(canonicalParent, "outside-parent")
		if err := os.Mkdir(outsideParent, 0o700); err != nil {
			t.Fatal(err)
		}
		ancestorLink := filepath.Join(canonicalParent, "ancestor-link")
		if err := os.Symlink(outsideParent, ancestorLink); err != nil {
			t.Fatal(err)
		}
		if _, err := NewLocalStorage(filepath.Join(ancestorLink, "files")); !errors.Is(err, ErrStorageRoot) {
			t.Fatalf("symlink ancestor error = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(outsideParent, "files")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected ancestor created outside root: %v", err)
		}
	}

	root := filepath.Join(canonicalParent, "files")
	storage, err := NewLocalStorage(root)
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("root permissions = %o", info.Mode().Perm())
	}
}

func TestLocalStorageStreamsBoundsAndCleansInterruptedStages(t *testing.T) {
	root := canonicalTestRoot(t, "files")
	storage, err := NewLocalStorage(root, WithMaximumContentBytes(5))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	if _, err := storage.Stage(context.Background(), "text/plain", strings.NewReader("123456")); !errors.Is(err, ErrContentTooLarge) {
		t.Fatalf("oversize stage error = %v", err)
	}
	if _, err := storage.Stage(context.Background(), "image/png", strings.NewReader("hello")); !errors.Is(err, ErrMediaType) {
		t.Fatalf("mismatched media error = %v", err)
	}
	if _, err := storage.Stage(context.Background(), "text/plain", io.MultiReader(strings.NewReader("ok"), failingReader{})); err == nil {
		t.Fatal("interrupted stage unexpectedly succeeded")
	}
	assertRootEntries(t, root, nil)
}

func TestLocalStoragePublishesWithoutReplacingExistingContent(t *testing.T) {
	root := canonicalTestRoot(t, "files")
	storage, err := NewLocalStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	first, err := storage.Stage(context.Background(), "text/plain", strings.NewReader("first"))
	if err != nil {
		t.Fatal(err)
	}
	key := NewStorageKey()
	if err := storage.Publish(context.Background(), first.TemporaryKey, key); err != nil {
		t.Fatal(err)
	}
	second, err := storage.Stage(context.Background(), "text/plain", strings.NewReader("second"))
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Publish(context.Background(), second.TemporaryKey, key); !errors.Is(err, ErrStorageConflict) {
		t.Fatalf("second publish error = %v", err)
	}
	defer storage.Abort(context.Background(), second.TemporaryKey)

	reader, err := storage.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	content, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(content, []byte("first")) {
		t.Fatalf("published content = %q, read=%v close=%v", content, readErr, closeErr)
	}

	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		symlinkKey := NewStorageKey()
		if err := os.Symlink(outside, filepath.Join(root, symlinkKey)); err != nil {
			t.Fatal(err)
		}
		third, err := storage.Stage(context.Background(), "text/plain", strings.NewReader("third"))
		if err != nil {
			t.Fatal(err)
		}
		defer storage.Abort(context.Background(), third.TemporaryKey)
		if err := storage.Publish(context.Background(), third.TemporaryKey, symlinkKey); !errors.Is(err, ErrStorageConflict) {
			t.Fatalf("publish over symlink error = %v", err)
		}
		outsideContent, err := os.ReadFile(outside)
		if err != nil || string(outsideContent) != "outside" {
			t.Fatalf("outside content = %q, err=%v", outsideContent, err)
		}
	}
}

func TestLocalStorageOpenRejectsSymlinkAndNonRegularLeaf(t *testing.T) {
	root := canonicalTestRoot(t, "files")
	storage, err := NewLocalStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		symlinkKey := NewStorageKey()
		if err := os.Symlink(outside, filepath.Join(root, symlinkKey)); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.Open(context.Background(), symlinkKey); !errors.Is(err, ErrStorageNotFound) {
			t.Fatalf("open symlink = %v", err)
		}
	}

	directoryKey := NewStorageKey()
	if err := os.Mkdir(filepath.Join(root, directoryKey), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Open(context.Background(), directoryKey); !errors.Is(err, ErrStorageNotFound) {
		t.Fatalf("open directory = %v", err)
	}
}

func TestLocalStorageOpenNeverFollowsAdversarialLeafReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unprivileged Windows symlink creation is not portable")
	}
	root := canonicalTestRoot(t, "files")
	storage, err := NewLocalStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	outside := filepath.Join(canonicalTestRoot(t, "outside-parent"), "secret")
	if err := os.Mkdir(filepath.Dir(outside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := NewStorageKey()
	leaf := filepath.Join(root, key)
	if err := os.WriteFile(leaf, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = os.Remove(leaf)
			_ = os.Symlink(outside, leaf)
			_ = os.Remove(leaf)
			_ = os.WriteFile(leaf, []byte("inside"), 0o600)
		}
	}()
	for range 500 {
		reader, err := storage.Open(context.Background(), key)
		if err != nil {
			if !errors.Is(err, ErrStorageNotFound) && !errors.Is(err, ErrStorage) {
				close(done)
				<-writerDone
				t.Fatalf("unexpected raced open error = %v", err)
			}
			continue
		}
		content, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr == nil && string(content) == "outside-secret" {
			close(done)
			<-writerDone
			t.Fatalf("raced open escaped root: %q", content)
		}
	}
	close(done)
	<-writerDone
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("interrupted source") }

func assertRootEntries(t *testing.T, root string, expected []string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		actual = append(actual, entry.Name())
	}
	if strings.Join(actual, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("root entries = %v, want %v", actual, expected)
	}
}

func canonicalTestRoot(t *testing.T, leaf string) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, leaf)
}
