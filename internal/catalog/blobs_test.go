package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
)

// testDigest wraps a hex value as a wire digest with the given algorithm.
func testDigest(algorithm, value string) *commonv1.Digest {
	return &commonv1.Digest{Algorithm: algorithm, Value: value}
}

func TestBlobKey(t *testing.T) {
	t.Parallel()

	sum := sha256.Sum256([]byte("blob"))
	hexValue := hex.EncodeToString(sum[:])

	for _, tc := range []struct {
		name   string
		digest *commonv1.Digest
		want   string
	}{
		{"bare sha256 value", testDigest("sha256", hexValue), "sha256:" + hexValue},
		{"empty algorithm means sha256", testDigest("", hexValue), "sha256:" + hexValue},
		{"uppercase value is normalized", testDigest("SHA256", strings.ToUpper(hexValue)), "sha256:" + hexValue},
		{"surrounding space is ignored", testDigest("sha256", "  "+hexValue+"\n"), "sha256:" + hexValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := BlobKey(tc.digest)
			if err != nil {
				t.Fatalf("BlobKey: %v", err)
			}
			if got != tc.want {
				t.Fatalf("BlobKey = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBlobKeyRejectsUnusableDigests(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		digest *commonv1.Digest
	}{
		{"missing digest", nil},
		{"empty value", testDigest("sha256", "")},
		{"unsupported algorithm", testDigest("md5", strings.Repeat("a", 32))},
		{"prefixed value", testDigest("sha256", "sha256:"+strings.Repeat("a", 64))},
		{"too short", testDigest("sha256", "abc")},
		{"too long", testDigest("sha256", strings.Repeat("a", 65))},
		{"not hex", testDigest("sha256", strings.Repeat("z", 64))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := BlobKey(tc.digest); !errors.Is(err, ErrInvalidDigest) {
				t.Fatalf("BlobKey error = %v, want ErrInvalidDigest", err)
			}
		})
	}
}

func TestMemBlobsPutGet(t *testing.T) {
	t.Parallel()

	blobs := NewMemBlobs()
	data := []byte("file content")
	digest := testDigest("sha256", func() string {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}())

	if _, ok := blobs.Get(digest); ok {
		t.Fatal("Get on an empty store returned data")
	}
	if err := blobs.Put(digest, data); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok := blobs.Get(digest)
	if !ok {
		t.Fatal("Get: not found")
	}
	if string(got) != string(data) {
		t.Fatalf("Get = %q, want %q", got, data)
	}

	// The stored bytes are a copy: mutating what Get handed back must not
	// change the store.
	got[0] = 'X'
	again, ok := blobs.Get(digest)
	if !ok || string(again) != string(data) {
		t.Fatalf("Get after mutation = %q, want %q", again, data)
	}

	// A digest without an algorithm is the same key.
	if _, ok := blobs.Get(testDigest("", digest.Value)); !ok {
		t.Fatal("Get with an empty algorithm: not found")
	}
	// A different algorithm is not the same key, and cannot key a sha256
	// blob either.
	if _, ok := blobs.Get(testDigest("md5", digest.Value)); ok {
		t.Fatal("Get with an md5 digest returned data")
	}
}

func TestMemBlobsPutRejectsMismatches(t *testing.T) {
	t.Parallel()

	blobs := NewMemBlobs()
	good := sha256.Sum256([]byte("file content"))
	other := sha256.Sum256([]byte("other content"))

	if err := blobs.Put(testDigest("sha256", hex.EncodeToString(good[:])), []byte("other content")); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Put mismatch error = %v, want ErrDigestMismatch", err)
	}
	if err := blobs.Put(testDigest("md5", "abc"), []byte("x")); !errors.Is(err, ErrInvalidDigest) {
		t.Fatalf("Put invalid digest error = %v, want ErrInvalidDigest", err)
	}
	// Nothing was filed by the rejected puts.
	if _, ok := blobs.Get(testDigest("sha256", hex.EncodeToString(other[:]))); ok {
		t.Fatal("rejected Put stored data")
	}
}

func TestMemBlobsConcurrent(t *testing.T) {
	t.Parallel()

	blobs := NewMemBlobs()
	data := []byte("concurrent content")
	sum := sha256.Sum256(data)
	digest := testDigest("sha256", hex.EncodeToString(sum[:]))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = blobs.Put(digest, data)
				blobs.Get(digest)
			}
		}()
	}
	wg.Wait()

	got, ok := blobs.Get(digest)
	if !ok || string(got) != string(data) {
		t.Fatalf("Get after concurrent use = %q (ok=%v), want %q", got, ok, data)
	}
}
