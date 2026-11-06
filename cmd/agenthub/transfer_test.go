package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
	federationv1connect "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1/federationv1connect"

	"go.pilab.hu/agenthub/internal/api"
	"go.pilab.hu/agenthub/internal/catalog"
)

// newTransferHub mounts the real Federation handler on an httptest server and
// returns its URL together with the catalog and blob stores behind it, so a
// test can seed an origin hub and inspect an air-gapped one.
func newTransferHub(t *testing.T, hubID string) (string, catalog.Store, catalog.BlobStore) {
	t.Helper()
	store := catalog.NewMemStore()
	blobs := catalog.NewMemBlobs()
	f := api.NewFederationWithBlobs(hubID, "https://"+hubID, store, nil, blobs)
	path, handler := federationv1connect.NewFederationServiceHandler(f)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, store, blobs
}

// runTransfer executes the CLI with its own stdin, stdout and a captured
// stderr, in a fresh working directory.
func runTransfer(t *testing.T, stdin io.Reader, stdout io.Writer, args ...string) (string, error) {
	t.Helper()
	t.Chdir(t.TempDir())
	var stderr bytes.Buffer
	root := newRoot()
	if stdin != nil {
		root.SetIn(stdin)
	}
	root.SetOut(stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stderr.String(), err
}

// transferFixture is the version seedTransferAsset publishes on an origin
// hub.
type transferFixture struct {
	manifest []byte
	fileA    []byte // the hub holds these bytes and streams them
	fileB    []byte // declared by the manifest, never uploaded
}

// seedTransferAsset publishes pilab/agent/alpha@1.0.0 with a manifest that
// declares two files: one whose bytes the hub holds (and therefore streams
// with the header) and one that travels as a descriptor only. fileA is sized
// to span several export chunks, so the archive carries a record per chunk.
func seedTransferAsset(t *testing.T, store catalog.Store, blobs catalog.BlobStore) transferFixture {
	t.Helper()
	fileA := bytes.Repeat([]byte("agenthub-transfer-payload-"), 6000) // 156,000 bytes
	fileB := []byte("descriptor only: never uploaded")
	manifest := transferManifest(t, []*commonv1.FileDescriptor{
		transferDescriptor("prompt.md", fileA),
		transferDescriptor("guides/notes.md", fileB),
	})
	fx := transferFixture{manifest: manifest, fileA: fileA, fileB: fileB}

	published := timestamppb.New(time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	if err := store.UpsertAsset(&catalog.Asset{
		Namespace:   "pilab",
		Kind:        commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:        "alpha",
		DisplayName: "Alpha agent",
		Summary:     "Independent reviewer",
		Tags:        []string{"go", "review"},
		Categories:  []string{"nlp"},
		License:     "MIT",
		Repository:  "https://example.invalid/alpha",
		LatestVer:   "1.0.0",
		CreatedAt:   published,
		UpdatedAt:   published,
	}); err != nil {
		t.Fatalf("upsert asset: %v", err)
	}
	if err := store.UpsertVersion(&catalog.Version{
		Namespace:      "pilab",
		Kind:           commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:           "alpha",
		Version:        "1.0.0",
		Status:         commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		Manifest:       manifest,
		ManifestDigest: transferDigest(manifest),
		BundleDigest:   &commonv1.Digest{Algorithm: "sha256", Value: strings.Repeat("c", 64)},
		BundleSize:     uint64(len(fileA) + len(fileB)),
		PublishedAt:    published,
		Readme:         "readme",
		Changelog:      "changelog",
	}); err != nil {
		t.Fatalf("upsert version: %v", err)
	}
	if err := blobs.Put(transferDigest(fileA), fileA); err != nil {
		t.Fatalf("store blob: %v", err)
	}
	return fx
}

// transferDigest wraps b as the sha256 digest a descriptor carries.
func transferDigest(b []byte) *commonv1.Digest {
	sum := sha256.Sum256(b)
	return &commonv1.Digest{Algorithm: "sha256", Value: hex.EncodeToString(sum[:])}
}

// transferDescriptor describes data the way a manifest declares a file.
func transferDescriptor(path string, data []byte) *commonv1.FileDescriptor {
	return &commonv1.FileDescriptor{
		Path:      path,
		Sha256:    transferDigest(data).GetValue(),
		Size:      uint64(len(data)),
		MediaType: "application/octet-stream",
	}
}

// transferManifest renders a manifest document declaring files
// (docs/architecture/domain-model.md §3.2).
func transferManifest(t *testing.T, files []*commonv1.FileDescriptor) []byte {
	t.Helper()
	entries := make([]map[string]any, 0, len(files))
	for _, fd := range files {
		entries = append(entries, map[string]any{
			"path":      fd.GetPath(),
			"size":      fd.GetSize(),
			"sha256":    fd.GetSha256(),
			"mediaType": fd.GetMediaType(),
		})
	}
	raw, err := json.Marshal(map[string]any{
		"name":    "alpha",
		"version": "1.0.0",
		"files":   entries,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// readArchiveLines returns the raw records of an archive file, one per line.
func readArchiveLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test reads the file it just wrote
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	return archiveLines(string(raw))
}

// archiveLines splits raw archive text into its records.
func archiveLines(raw string) []string {
	raw = strings.TrimSuffix(raw, "\n")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

// parseArchive decodes every record of an archive, the way import does.
func parseArchive(t *testing.T, lines []string) []*federationv1.ExportBundleResponse {
	t.Helper()
	out := make([]*federationv1.ExportBundleResponse, 0, len(lines))
	for i, line := range lines {
		var rec federationv1.ExportBundleResponse
		if err := protojson.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("archive record %d: %v", i, err)
		}
		out = append(out, &rec)
	}
	return out
}

// TestExportImportRoundTrip moves one version from an origin hub to an
// air-gapped one through the archive and checks both sides of the transfer.
func TestExportImportRoundTrip(t *testing.T) {
	// Not parallel: changes the process working directory.
	originURL, originStore, originBlobs := newTransferHub(t, "origin.example")
	fx := seedTransferAsset(t, originStore, originBlobs)
	airgapURL, airgapStore, airgapBlobs := newTransferHub(t, "airgap.example")

	archive := filepath.Join(t.TempDir(), "alpha.jsonl")
	stderr, err := runTransfer(t, nil, io.Discard,
		"export", "pilab/agent/alpha@1.0.0", "--hub", originURL, "-o", archive)
	if err != nil {
		t.Fatalf("export: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stderr, "exported pilab/agent/alpha@1.0.0") {
		t.Fatalf("export progress missing from stderr:\n%s", stderr)
	}

	// The archive opens with the header record and follows with one chunk
	// record per message of the export stream.
	records := parseArchive(t, readArchiveLines(t, archive))
	if len(records) < 2 {
		t.Fatalf("archive has %d records, want a header plus file chunks", len(records))
	}
	if records[0].GetHeader() == nil {
		t.Fatalf("first archive record = %v, want a bundle header", records[0].GetPart())
	}
	for i, rec := range records[1:] {
		if rec.GetFile() == nil || len(rec.GetData()) == 0 {
			t.Fatalf("archive record %d = %v, want a file chunk with data", i+1, rec.GetPart())
		}
	}

	stderr, err = runTransfer(t, nil, io.Discard, "import", archive, "--hub", airgapURL)
	if err != nil {
		t.Fatalf("import: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stderr, "imported pilab/agent/alpha@1.0.0") {
		t.Fatalf("import progress missing from stderr:\n%s", stderr)
	}

	asset, ok := airgapStore.GetAsset("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha")
	if !ok {
		t.Fatal("imported asset is missing from the hub catalog")
	}
	if asset.LatestVer != "1.0.0" || asset.Summary != "Independent reviewer" || len(asset.Tags) != 2 {
		t.Fatalf("asset = %+v, want the exported projection", asset)
	}
	version, ok := airgapStore.GetVersion("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha", "1.0.0")
	if !ok {
		t.Fatal("imported version is missing from the hub catalog")
	}
	if version.Status != commonv1.VersionStatus_VERSION_STATUS_PUBLISHED ||
		!bytes.Equal(version.Manifest, fx.manifest) {
		t.Fatalf("version = %+v, want the exported manifest and status", version)
	}

	// The bytes the origin held travel with the transfer; a file the origin
	// never held stays a descriptor.
	got, ok := airgapBlobs.Get(transferDigest(fx.fileA))
	if !ok || !bytes.Equal(got, fx.fileA) {
		t.Fatalf("imported file bytes: ok=%v len=%d, want %d bytes", ok, len(got), len(fx.fileA))
	}
	if _, ok := airgapBlobs.Get(transferDigest(fx.fileB)); ok {
		t.Fatal("descriptor-only file gained bytes on import")
	}
}

// TestExportToStdoutAndImportFromStdin checks the pipe-friendly half of the
// CLI: without -o the stdout stream is nothing but the archive (progress
// goes to stderr) and "-" imports it back from stdin. The ref carries no
// version, so the export selects the asset's latest.
func TestExportToStdoutAndImportFromStdin(t *testing.T) {
	// Not parallel: changes the process working directory.
	originURL, originStore, originBlobs := newTransferHub(t, "origin.example")
	seedTransferAsset(t, originStore, originBlobs)
	airgapURL, airgapStore, _ := newTransferHub(t, "airgap.example")

	var stdout bytes.Buffer
	stderr, err := runTransfer(t, nil, &stdout, "export", "pilab/agent/alpha", "--hub", originURL)
	if err != nil {
		t.Fatalf("export: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stderr, "exported pilab/agent/alpha@1.0.0") {
		t.Fatalf("export progress missing from stderr:\n%s", stderr)
	}
	if strings.Contains(stdout.String(), "exported ") {
		t.Fatalf("progress leaked into the archive:\n%s", stdout.String())
	}
	records := parseArchive(t, archiveLines(stdout.String()))
	if len(records) < 2 {
		t.Fatalf("stdout holds %d archive records, want a header plus chunks", len(records))
	}
	if records[0].GetHeader() == nil {
		t.Fatalf("first archive record = %v, want a bundle header", records[0].GetPart())
	}

	stderr, err = runTransfer(t, bytes.NewReader(stdout.Bytes()), io.Discard,
		"import", "-", "--hub", airgapURL)
	if err != nil {
		t.Fatalf("import: %v (stderr: %s)", err, stderr)
	}
	if _, ok := airgapStore.GetAsset("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha"); !ok {
		t.Fatal("asset missing after importing from stdin")
	}
}

// TestImportRejectsArchiveWithoutHeader checks that an archive whose first
// record is not a bundle header fails locally, before anything is sent.
func TestImportRejectsArchiveWithoutHeader(t *testing.T) {
	// Not parallel: changes the process working directory.
	airgapURL, airgapStore, _ := newTransferHub(t, "airgap.example")

	archive := filepath.Join(t.TempDir(), "broken.jsonl")
	record := `{"data":"aGVsbG8=","file":{"path":"prompt.md","offset":0}}`
	if err := os.WriteFile(archive, []byte(record+"\n"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	stderr, err := runTransfer(t, nil, io.Discard, "import", archive, "--hub", airgapURL)
	if err == nil {
		t.Fatalf("import accepted an archive without a header (stderr: %s)", stderr)
	}
	if !strings.Contains(err.Error(), "does not start with a bundle header") {
		t.Fatalf("error = %v, want a missing-header error", err)
	}
	if _, ok := airgapStore.GetAsset("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha"); ok {
		t.Fatal("hub stored an asset from a rejected archive")
	}
}

// TestImportTruncatedArchiveRejectedByHub cuts an archive down to its header
// record — the declared file bytes never follow — and checks that the hub,
// not the CLI, rejects the incomplete stream without storing anything.
func TestImportTruncatedArchiveRejectedByHub(t *testing.T) {
	// Not parallel: changes the process working directory.
	originURL, originStore, originBlobs := newTransferHub(t, "origin.example")
	fx := seedTransferAsset(t, originStore, originBlobs)
	airgapURL, airgapStore, airgapBlobs := newTransferHub(t, "airgap.example")

	full := filepath.Join(t.TempDir(), "alpha.jsonl")
	stderr, err := runTransfer(t, nil, io.Discard,
		"export", "pilab/agent/alpha@1.0.0", "--hub", originURL, "-o", full)
	if err != nil {
		t.Fatalf("export: %v (stderr: %s)", err, stderr)
	}
	lines := readArchiveLines(t, full)
	if len(lines) < 3 {
		t.Fatalf("archive has %d records, want a header plus chunks", len(lines))
	}
	truncated := filepath.Join(t.TempDir(), "truncated.jsonl")
	if err := os.WriteFile(truncated, []byte(lines[0]+"\n"), 0o600); err != nil {
		t.Fatalf("write truncated archive: %v", err)
	}

	stderr, err = runTransfer(t, nil, io.Discard, "import", truncated, "--hub", airgapURL)
	if err == nil {
		t.Fatalf("import accepted a truncated archive (stderr: %s)", stderr)
	}
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Fatalf("error code = %s, want %s (err: %v)", code, connect.CodeInvalidArgument, err)
	}
	if _, ok := airgapStore.GetAsset("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha"); ok {
		t.Fatal("hub stored an asset from a truncated archive")
	}
	if _, ok := airgapBlobs.Get(transferDigest(fx.fileA)); ok {
		t.Fatal("hub stored a blob from a truncated archive")
	}
}
