package cache

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// helloSHA256 is the well-known sha256 digest of "hello world".
const helloSHA256 = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

func TestKey(t *testing.T) {
	t.Parallel()

	if got := Key([]byte("hello world")); got != helloSHA256 {
		t.Fatalf("Key(hello world) = %s, want %s", got, helloSHA256)
	}
	if got := Key(nil); got != Key([]byte{}) {
		t.Fatalf("Key(nil) = %s, want %s", got, Key([]byte{}))
	}
}

func TestKeyFromDigest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		digest  string
		want    string
		wantErr error
	}{
		{name: "prefixed", digest: "sha256:" + helloSHA256, want: helloSHA256},
		{name: "bare", digest: helloSHA256, want: helloSHA256},
		{name: "uppercase", digest: "SHA256:" + strings.ToUpper(helloSHA256), want: helloSHA256},
		{name: "whitespace", digest: "  sha256:" + helloSHA256 + "  ", want: helloSHA256},
		{name: "empty", digest: "", wantErr: ErrInvalidKey},
		{name: "short", digest: "sha256:abc", wantErr: ErrInvalidKey},
		{name: "non-hex", digest: "sha256:" + strings.Repeat("z", 64), wantErr: ErrInvalidKey},
		{name: "other algorithm", digest: "sha512:" + helloSHA256, wantErr: ErrUnsupportedAlgorithm},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := KeyFromDigest(tt.digest)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("KeyFromDigest(%q) error = %v, want %v", tt.digest, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("KeyFromDigest(%q): %v", tt.digest, err)
			}
			if got != tt.want {
				t.Fatalf("KeyFromDigest(%q) = %s, want %s", tt.digest, got, tt.want)
			}
		})
	}
}

func TestDefaultDir(t *testing.T) {
	// Not parallel: mutates the process environment.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvCacheDir, "")
	t.Setenv(EnvXDGCacheHome, "")

	t.Run("cache dir env wins", func(t *testing.T) {
		t.Setenv(EnvCacheDir, filepath.Join(home, "custom"))
		if got, want := DefaultDir(), filepath.Join(home, "custom"); got != want {
			t.Fatalf("DefaultDir() = %q, want %q", got, want)
		}
	})
	t.Run("xdg cache home", func(t *testing.T) {
		t.Setenv(EnvXDGCacheHome, filepath.Join(home, "xdg"))
		if got, want := DefaultDir(), filepath.Join(home, "xdg", DirName); got != want {
			t.Fatalf("DefaultDir() = %q, want %q", got, want)
		}
	})
	t.Run("home fallback", func(t *testing.T) {
		if got, want := DefaultDir(), filepath.Join(home, ".cache", DirName); got != want {
			t.Fatalf("DefaultDir() = %q, want %q", got, want)
		}
	})
}

func TestNewRequiresDir(t *testing.T) {
	t.Parallel()

	for _, dir := range []string{"", "  "} {
		if _, err := New(dir); err == nil {
			t.Fatalf("New(%q): expected error for empty directory", dir)
		}
	}
}

func TestPutGetHasRoundTrip(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data := []byte(`{"name":"code-reviewer"}`)
	key := Key(data)

	if c.Has(key) {
		t.Fatal("fresh cache should not have the key")
	}
	if err := c.Put(key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !c.Has(key) {
		t.Fatal("Has should report the stored key")
	}
	got, err := c.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Get = %q, want %q", got, data)
	}
	// Content-addressed layout: sha256/<aa>/<key>.
	wantPath := filepath.Join(c.Dir(), "sha256", key[:2], key)
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("object not stored at %q: %v", wantPath, err)
	}
}

func TestPutRejectsDigestMismatch(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	key := Key([]byte("expected"))
	if err := c.Put(key, []byte("tampered")); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Put error = %v, want ErrDigestMismatch", err)
	}
	if c.Has(key) {
		t.Fatal("mismatched Put must not store anything")
	}
}

func TestPutInvalidKey(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, key := range []string{"", "abc", strings.Repeat("g", 64), strings.Repeat("A", 63)} {
		if err := c.Put(key, []byte("data")); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("Put(%q) error = %v, want ErrInvalidKey", key, err)
		}
	}
	if _, err := c.Get("not-a-key"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Get error = %v, want ErrInvalidKey", err)
	}
	if c.Has("not-a-key") {
		t.Fatal("Has must reject invalid keys")
	}
}

func TestGetNotFound(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	key := Key([]byte("absent"))
	if _, err := c.Get(key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

func TestGetDetectsCorruption(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data := []byte("original content")
	key := Key(data)
	if err := c.Put(key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Bypass Put and corrupt the stored object directly.
	path := c.objectPath(key)
	if err := os.WriteFile(path, []byte("evil content"), 0o600); err != nil {
		t.Fatalf("corrupt object: %v", err)
	}
	if !c.Has(key) {
		t.Fatal("Has only checks presence, not integrity")
	}
	if _, err := c.Get(key); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Get error = %v, want ErrDigestMismatch", err)
	}
}

func TestPutOverwriteIsIdempotent(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data := []byte("same bytes")
	key := Key(data)
	for i := range 3 {
		if err := c.Put(key, data); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	got, err := c.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Get = %q, want %q", got, data)
	}
	// No leftover temp files.
	entries, err := os.ReadDir(filepath.Join(c.Dir(), "sha256", key[:2]))
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("shard has %d entries, want 1", len(entries))
	}
}

func TestConcurrentPuts(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data := []byte("shared object")
	key := Key(data)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- c.Put(key, data)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Put: %v", err)
		}
	}
	got, err := c.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Get = %q, want %q", got, data)
	}
}
