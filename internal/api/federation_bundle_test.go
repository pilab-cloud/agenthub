package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
	federationv1connect "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1/federationv1connect"

	"go.pilab.hu/agenthub/internal/catalog"
)

// bundlePublishedAt is the fixed publish time of every seeded version.
var bundlePublishedAt = timestamppb.New(time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))

// newBundleClient mounts a federation handler on an httptest server and
// returns a client for it, which is how the streaming RPCs of M4.3 are
// exercised end to end.
func newBundleClient(t *testing.T, f *Federation) federationv1connect.FederationServiceClient {
	t.Helper()
	path, handler := federationv1connect.NewFederationServiceHandler(f)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return federationv1connect.NewFederationServiceClient(srv.Client(), srv.URL)
}

// wantCode fails unless err is a connect error carrying code.
func wantCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("error: nil, want %s", code)
	}
	if got := connect.CodeOf(err); got != code {
		t.Fatalf("code: %s, want %s (err: %v)", got, code, err)
	}
}

// digestOf wraps the sha256 of b as a wire digest.
func digestOf(b []byte) *commonv1.Digest {
	sum := sha256.Sum256(b)
	return &commonv1.Digest{Algorithm: "sha256", Value: hex.EncodeToString(sum[:])}
}

// fileDescriptor describes data by its path, size and sha256, the way a
// manifest declares a file.
func fileDescriptor(path string, data []byte) *commonv1.FileDescriptor {
	sum := sha256.Sum256(data)
	return &commonv1.FileDescriptor{
		Path:      path,
		Sha256:    hex.EncodeToString(sum[:]),
		Size:      uint64(len(data)),
		MediaType: "application/octet-stream",
	}
}

// bundleManifest renders a manifest document declaring files
// (docs/architecture/domain-model.md §3.2).
func bundleManifest(t *testing.T, files []*commonv1.FileDescriptor) []byte {
	t.Helper()
	entries := make([]map[string]any, 0, len(files))
	for _, fd := range files {
		entries = append(entries, map[string]any{
			"path":      fd.GetPath(),
			"size":      fd.GetSize(),
			"sha256":    fd.GetSha256(),
			"mediaType": fd.GetMediaType(),
			"mode":      fd.GetMode(),
		})
	}
	raw, err := json.Marshal(map[string]any{
		"name":    "alpha",
		"version": "1.0.0",
		"files":   entries,
		"spec":    map[string]any{"agent": map[string]any{"prompt": "prompt.md"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// testBundle is the version seedBundleVersion publishes.
type testBundle struct {
	ref          *commonv1.AssetRef
	manifest     []byte
	fileA        []byte // the hub holds these bytes and streams them
	fileB        []byte // declared by the manifest, never uploaded
	bundleDigest *commonv1.Digest
}

// seedBundleVersion publishes pilab/agent/alpha@1.0.0 with a manifest that
// declares two files: one whose bytes the hub holds and streams with the
// header, one whose bytes were never uploaded and travels as a descriptor
// only. A yanked and a draft version of the same asset come along for the
// selector tests.
func seedBundleVersion(t *testing.T, f *Federation, store catalog.Store) testBundle {
	t.Helper()
	// 24 × 6400 = 153,600 bytes, three chunks of exportChunkSize.
	fileA := bytes.Repeat([]byte("agenthub-export-payload-"), 6400)
	fileB := []byte("descriptor only: never uploaded")
	manifest := bundleManifest(t, []*commonv1.FileDescriptor{
		fileDescriptor("prompt.md", fileA),
		fileDescriptor("guides/notes.md", fileB),
	})
	bundle := testBundle{
		ref: &commonv1.AssetRef{
			Hub:       testHubID,
			Namespace: "pilab",
			Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
			Name:      "alpha",
			Version:   "1.0.0",
		},
		manifest:     manifest,
		fileA:        fileA,
		fileB:        fileB,
		bundleDigest: &commonv1.Digest{Algorithm: "sha256", Value: strings.Repeat("c", 64)},
	}

	seedAssetVersion(t, store, &catalog.Asset{
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
		CreatedAt:   bundlePublishedAt,
		UpdatedAt:   bundlePublishedAt,
	}, &catalog.Version{
		Namespace:      "pilab",
		Kind:           commonv1.AssetKind_ASSET_KIND_AGENT,
		Name:           "alpha",
		Version:        "1.0.0",
		Status:         commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		Manifest:       manifest,
		ManifestDigest: digestOf(manifest),
		BundleDigest:   bundle.bundleDigest,
		BundleSize:     uint64(len(fileA) + len(fileB) + 1024),
		PublishedAt:    bundlePublishedAt,
		Readme:         "readme",
		Changelog:      "changelog",
	},
		&catalog.Version{
			Namespace:      "pilab",
			Kind:           commonv1.AssetKind_ASSET_KIND_AGENT,
			Name:           "alpha",
			Version:        "0.9.0",
			Status:         commonv1.VersionStatus_VERSION_STATUS_YANKED,
			Manifest:       []byte(`{"name":"alpha","version":"0.9.0"}`),
			ManifestDigest: digestOf([]byte(`{"name":"alpha","version":"0.9.0"}`)),
			BundleDigest:   &commonv1.Digest{Algorithm: "sha256", Value: strings.Repeat("d", 64)},
			PublishedAt:    bundlePublishedAt,
		},
		&catalog.Version{
			Namespace:      "pilab",
			Kind:           commonv1.AssetKind_ASSET_KIND_AGENT,
			Name:           "alpha",
			Version:        "2.0.0-draft",
			Status:         commonv1.VersionStatus_VERSION_STATUS_DRAFT,
			Manifest:       []byte(`{"name":"alpha","version":"2.0.0-draft"}`),
			ManifestDigest: digestOf([]byte(`{"name":"alpha","version":"2.0.0-draft"}`)),
			BundleDigest:   &commonv1.Digest{Algorithm: "sha256", Value: strings.Repeat("e", 64)},
			PublishedAt:    bundlePublishedAt,
		})

	if err := f.blobs.Put(digestOf(fileA), fileA); err != nil {
		t.Fatalf("store blob: %v", err)
	}
	return bundle
}

// seedAssetVersion upserts versions under one asset, creating the asset when
// it is missing. An existing asset keeps its creation time, like a re-import
// does.
func seedAssetVersion(t *testing.T, store catalog.Store, asset *catalog.Asset, versions ...*catalog.Version) {
	t.Helper()
	if existing, ok := store.GetAsset(asset.Namespace, asset.Kind, asset.Name); ok && existing.CreatedAt != nil {
		asset.CreatedAt = existing.CreatedAt
	}
	if err := store.UpsertAsset(asset); err != nil {
		t.Fatalf("upsert asset: %v", err)
	}
	for _, v := range versions {
		if err := store.UpsertVersion(v); err != nil {
			t.Fatalf("upsert version: %v", err)
		}
	}
}

// exportOnce runs one ExportBundle call to completion and returns the
// header, every message of the stream (header first) and the error the RPC
// ended with. Shape violations are errors, not test failures, so it is safe
// to call from a goroutine.
func exportOnce(t *testing.T, client federationv1connect.FederationServiceClient,
	req *federationv1.ExportBundleRequest,
) (*federationv1.BundleHeader, []*federationv1.ExportBundleResponse, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.ExportBundle(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, nil, err
	}

	var header *federationv1.BundleHeader
	var msgs []*federationv1.ExportBundleResponse
	for stream.Receive() {
		msg := stream.Msg()
		switch msg.GetPart().(type) {
		case *federationv1.ExportBundleResponse_Header:
			if header != nil {
				return nil, nil, errors.New("export stream carries more than one header")
			}
			header = msg.GetHeader()
		case *federationv1.ExportBundleResponse_File:
			if header == nil {
				return nil, nil, errors.New("export stream starts with a file chunk")
			}
		default:
			return nil, nil, errors.New("export message carries neither a header nor a file chunk")
		}
		msgs = append(msgs, msg)
	}
	if err := stream.Err(); err != nil {
		return nil, nil, err
	}
	if header == nil {
		return nil, nil, errors.New("export stream carries no header")
	}
	return header, msgs, nil
}

// chunkCount counts the chunks of one file in an export stream.
func chunkCount(msgs []*federationv1.ExportBundleResponse, path string) int {
	n := 0
	for _, msg := range msgs[1:] { // msgs[0] is the header
		if msg.GetFile().GetPath() == path {
			n++
		}
	}
	return n
}

// reassemble collects one file's chunks and checks that they are non-empty,
// bounded by exportChunkSize and contiguous from offset 0.
func reassemble(t *testing.T, msgs []*federationv1.ExportBundleResponse, path string) []byte {
	t.Helper()
	var buf bytes.Buffer
	var offset uint64
	for _, msg := range msgs[1:] {
		fc := msg.GetFile()
		if fc.GetPath() != path {
			continue
		}
		if fc.GetOffset() != offset {
			t.Fatalf("chunk of %s starts at %d, want %d", path, fc.GetOffset(), offset)
		}
		if len(msg.GetData()) == 0 || len(msg.GetData()) > exportChunkSize {
			t.Fatalf("chunk of %s is %d bytes, want 1..%d", path, len(msg.GetData()), exportChunkSize)
		}
		buf.Write(msg.GetData())
		offset += uint64(len(msg.GetData()))
	}
	return buf.Bytes()
}

// importMsgsFrom converts one export stream into the import stream that
// carries it to another hub. Everything is cloned, so the messages can be
// sent concurrently without sharing protobuf state.
func importMsgsFrom(msgs []*federationv1.ExportBundleResponse) []*federationv1.ImportBundleRequest {
	out := make([]*federationv1.ImportBundleRequest, 0, len(msgs))
	for _, msg := range msgs {
		clone := proto.Clone(msg).(*federationv1.ExportBundleResponse)
		req := &federationv1.ImportBundleRequest{Data: clone.GetData()}
		switch part := clone.GetPart().(type) {
		case *federationv1.ExportBundleResponse_Header:
			req.Part = &federationv1.ImportBundleRequest_Header{Header: part.Header}
		case *federationv1.ExportBundleResponse_File:
			req.Part = &federationv1.ImportBundleRequest_File{File: part.File}
		}
		out = append(out, req)
	}
	return out
}

// sendBundle sends msgs as one ImportBundle stream and returns the response
// the handler produced.
func sendBundle(t *testing.T, client federationv1connect.FederationServiceClient,
	msgs ...*federationv1.ImportBundleRequest,
) (*federationv1.ImportBundleResponse, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := client.ImportBundle(ctx)
	var sendErr error
	for _, msg := range msgs {
		if err := stream.Send(msg); err != nil && sendErr == nil {
			sendErr = err
		}
	}
	resp, err := stream.CloseAndReceive()
	if err != nil {
		return nil, err
	}
	return resp.Msg, sendErr
}

// headerRequest wraps a header as the opening message of a stream.
func headerRequest(h *federationv1.BundleHeader) *federationv1.ImportBundleRequest {
	return &federationv1.ImportBundleRequest{Part: &federationv1.ImportBundleRequest_Header{Header: h}}
}

// chunkRequest wraps one file chunk.
func chunkRequest(path string, offset uint64, data []byte) *federationv1.ImportBundleRequest {
	return &federationv1.ImportBundleRequest{
		Data: data,
		Part: &federationv1.ImportBundleRequest_File{
			File: &federationv1.BundleFileChunk{Path: path, Offset: offset},
		},
	}
}

// bundleFile describes one file of a bundle stream.
func bundleFile(path string, data []byte, hasBytes bool) *federationv1.BundleFile {
	return &federationv1.BundleFile{File: fileDescriptor(path, data), HasBytes: hasBytes}
}

// bundleHeader returns a well-formed header declaring one file with bytes.
func bundleHeader(t *testing.T, data []byte) *federationv1.BundleHeader {
	t.Helper()
	return headerWithManifest(t, bundleManifest(t, []*commonv1.FileDescriptor{fileDescriptor("f.bin", data)}),
		bundleFile("f.bin", data, true))
}

// headerWithManifest builds a header over manifest with the given files.
func headerWithManifest(t *testing.T, manifest []byte, files ...*federationv1.BundleFile) *federationv1.BundleHeader {
	t.Helper()
	return &federationv1.BundleHeader{
		Ref: &commonv1.AssetRef{
			Hub:       testHubID,
			Namespace: "pilab",
			Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
			Name:      "alpha",
			Version:   "1.0.0",
		},
		ManifestDigest: digestOf(manifest),
		BundleDigest:   &commonv1.Digest{Algorithm: "sha256", Value: strings.Repeat("c", 64)},
		BundleSize:     4096,
		Status:         commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		PublishedAt:    bundlePublishedAt,
		Manifest:       manifest,
		Files:          files,
	}
}

// rogueBlobs hands back the same bytes for every digest: an exporter must
// refuse to stream them, which a verified store cannot produce on demand.
type rogueBlobs struct {
	blob []byte
}

func (r rogueBlobs) Put(*commonv1.Digest, []byte) error { return nil }

func (r rogueBlobs) Get(*commonv1.Digest) ([]byte, bool) {
	return append([]byte(nil), r.blob...), true
}

var _ catalog.BlobStore = rogueBlobs{}

func TestExportImportBundleRoundTrip(t *testing.T) {
	t.Parallel()

	src, srcStore := newFederation(t)
	bundle := seedBundleVersion(t, src, srcStore)
	dst, dstStore := newFederation(t)

	header, msgs, err := exportOnce(t, newBundleClient(t, src), &federationv1.ExportBundleRequest{Ref: bundle.ref})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// The header carries the version, its manifest and the asset record.
	if header.GetRef().GetHub() != testHubID {
		t.Fatalf("ref hub = %q, want %q", header.GetRef().GetHub(), testHubID)
	}
	if header.GetRef().GetVersion() != "1.0.0" {
		t.Fatalf("ref version = %q, want 1.0.0", header.GetRef().GetVersion())
	}
	if !bytes.Equal(header.GetManifest(), bundle.manifest) {
		t.Fatal("manifest bytes differ from the stored manifest")
	}
	if got := digestOf(header.GetManifest()); !digestEqual(got, header.GetManifestDigest()) {
		t.Fatalf("manifest digest = %v, want %v", header.GetManifestDigest(), got)
	}
	if !digestEqual(header.GetBundleDigest(), bundle.bundleDigest) {
		t.Fatalf("bundle digest = %v, want %v", header.GetBundleDigest(), bundle.bundleDigest)
	}
	if header.GetStatus() != commonv1.VersionStatus_VERSION_STATUS_PUBLISHED {
		t.Fatalf("status = %s, want published", header.GetStatus())
	}
	if !header.GetPublishedAt().AsTime().Equal(bundlePublishedAt.AsTime()) {
		t.Fatalf("published at = %v, want %v", header.GetPublishedAt().AsTime(), bundlePublishedAt.AsTime())
	}
	if header.GetReadme() != "readme" || header.GetChangelog() != "changelog" {
		t.Fatalf("readme/changelog = %q/%q", header.GetReadme(), header.GetChangelog())
	}
	if header.GetDisplayName() != "Alpha agent" || header.GetSummary() != "Independent reviewer" {
		t.Fatalf("display name/summary = %q/%q", header.GetDisplayName(), header.GetSummary())
	}
	if len(header.GetTags()) != 2 || header.GetTags()[0] != "go" {
		t.Fatalf("tags = %v", header.GetTags())
	}
	if header.GetLicense() != "MIT" || header.GetRepository() != "https://example.invalid/alpha" {
		t.Fatalf("license/repository = %q/%q", header.GetLicense(), header.GetRepository())
	}

	// Both declared files travel as descriptors; only the one the hub holds
	// is marked as carrying bytes.
	if len(header.GetFiles()) != 2 {
		t.Fatalf("files = %d, want 2", len(header.GetFiles()))
	}
	first, second := header.GetFiles()[0], header.GetFiles()[1]
	if first.GetFile().GetPath() != "prompt.md" || second.GetFile().GetPath() != "guides/notes.md" {
		t.Fatalf("file paths = %q, %q", first.GetFile().GetPath(), second.GetFile().GetPath())
	}
	if !first.GetHasBytes() {
		t.Fatal("held file is declared without bytes")
	}
	if second.GetHasBytes() {
		t.Fatal("unuploaded file is declared with bytes")
	}
	if first.GetFile().GetSize() != uint64(len(bundle.fileA)) {
		t.Fatalf("declared size = %d, want %d", first.GetFile().GetSize(), len(bundle.fileA))
	}
	if first.GetFile().GetSha256() != digestOf(bundle.fileA).GetValue() {
		t.Fatalf("declared sha256 = %q, want %q", first.GetFile().GetSha256(), digestOf(bundle.fileA).GetValue())
	}

	// The held file is streamed in order, the unuploaded one is not.
	wantChunks := (len(bundle.fileA) + exportChunkSize - 1) / exportChunkSize
	if got := chunkCount(msgs, "prompt.md"); got != wantChunks {
		t.Fatalf("chunks of prompt.md = %d, want %d", got, wantChunks)
	}
	if got := chunkCount(msgs, "guides/notes.md"); got != 0 {
		t.Fatalf("chunks of guides/notes.md = %d, want 0", got)
	}
	if got := len(msgs); got != 1+wantChunks {
		t.Fatalf("messages = %d, want %d", got, 1+wantChunks)
	}
	if got := reassemble(t, msgs, "prompt.md"); !bytes.Equal(got, bundle.fileA) {
		t.Fatalf("reassembled file = %d bytes, want %d", len(got), len(bundle.fileA))
	}

	// The air-gapped hub takes the stream as it came.
	resp, err := sendBundle(t, newBundleClient(t, dst), importMsgsFrom(msgs)...)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !digestEqual(resp.GetBundleDigest(), bundle.bundleDigest) {
		t.Fatalf("response bundle digest = %v, want %v", resp.GetBundleDigest(), bundle.bundleDigest)
	}
	if want := uint64(len(bundle.manifest) + len(bundle.fileA)); resp.GetBytesWritten() != want {
		t.Fatalf("bytes written = %d, want %d", resp.GetBytesWritten(), want)
	}

	version, ok := dstStore.GetVersion("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha", "1.0.0")
	if !ok {
		t.Fatal("imported version not found")
	}
	if !bytes.Equal(version.Manifest, bundle.manifest) {
		t.Fatal("imported manifest differs")
	}
	if !digestEqual(version.ManifestDigest, digestOf(bundle.manifest)) {
		t.Fatalf("imported manifest digest = %v", version.ManifestDigest)
	}
	if !digestEqual(version.BundleDigest, bundle.bundleDigest) {
		t.Fatalf("imported bundle digest = %v", version.BundleDigest)
	}
	if version.BundleSize != uint64(len(bundle.fileA)+len(bundle.fileB)+1024) {
		t.Fatalf("imported bundle size = %d", version.BundleSize)
	}
	if version.Status != commonv1.VersionStatus_VERSION_STATUS_PUBLISHED {
		t.Fatalf("imported status = %s", version.Status)
	}
	if !version.PublishedAt.AsTime().Equal(bundlePublishedAt.AsTime()) {
		t.Fatalf("imported published at = %v", version.PublishedAt.AsTime())
	}
	if version.Readme != "readme" || version.Changelog != "changelog" {
		t.Fatalf("imported readme/changelog = %q/%q", version.Readme, version.Changelog)
	}

	asset, ok := dstStore.GetAsset("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha")
	if !ok {
		t.Fatal("imported asset not found")
	}
	if asset.DisplayName != "Alpha agent" || asset.Summary != "Independent reviewer" {
		t.Fatalf("imported display name/summary = %q/%q", asset.DisplayName, asset.Summary)
	}
	if asset.LatestVer != "1.0.0" {
		t.Fatalf("imported latest = %q, want 1.0.0", asset.LatestVer)
	}
	if len(asset.Tags) != 2 || len(asset.Categories) != 1 || asset.License != "MIT" {
		t.Fatalf("imported asset metadata = %v / %v / %q", asset.Tags, asset.Categories, asset.License)
	}
	if asset.CreatedAt == nil || asset.UpdatedAt == nil {
		t.Fatal("imported asset carries no timestamps")
	}

	// The held file is stored content-addressed, the unuploaded one is not.
	stored, ok := dst.blobs.Get(digestOf(bundle.fileA))
	if !ok || !bytes.Equal(stored, bundle.fileA) {
		t.Fatalf("imported blob = %d bytes (ok=%v), want %d", len(stored), ok, len(bundle.fileA))
	}
	if _, ok := dst.blobs.Get(digestOf(bundle.fileB)); ok {
		t.Fatal("descriptor-only file was stored as a blob")
	}
}

func TestExportBundleWithoutJSONManifest(t *testing.T) {
	t.Parallel()

	manifest := []byte("manifest-bytes")
	f, store := newFederation(t)
	seedAssetVersion(t, store, &catalog.Asset{
		Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha",
		DisplayName: "alpha", LatestVer: "1.0.0", CreatedAt: bundlePublishedAt, UpdatedAt: bundlePublishedAt,
	}, &catalog.Version{
		Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha", Version: "1.0.0",
		Status:         commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		Manifest:       manifest,
		ManifestDigest: digestOf(manifest),
		BundleDigest:   &commonv1.Digest{Algorithm: "sha256", Value: strings.Repeat("b", 64)},
		PublishedAt:    bundlePublishedAt,
	})

	header, msgs, err := exportOnce(t, newBundleClient(t, f), &federationv1.ExportBundleRequest{
		Ref: &commonv1.AssetRef{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha", Version: "1.0.0",
		},
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(header.GetFiles()) != 0 {
		t.Fatalf("files = %d, want 0 (a manifest without a files list declares none)", len(header.GetFiles()))
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want only the header", len(msgs))
	}
	if !bytes.Equal(header.GetManifest(), manifest) {
		t.Fatal("manifest bytes differ")
	}

	// The manifest-only bundle still imports: there is nothing to stream
	// after the header, and nothing to store but the version.
	dst, dstStore := newFederation(t)
	if _, err := sendBundle(t, newBundleClient(t, dst), importMsgsFrom(msgs)...); err != nil {
		t.Fatalf("import: %v", err)
	}
	version, ok := dstStore.GetVersion("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha", "1.0.0")
	if !ok || !bytes.Equal(version.Manifest, manifest) {
		t.Fatalf("imported version (ok=%v): manifest = %q", ok, version.Manifest)
	}
}

func TestExportBundleSelectors(t *testing.T) {
	t.Parallel()

	f, store := newFederation(t)
	bundle := seedBundleVersion(t, f, store)
	yanked, ok := store.GetVersion("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha", "0.9.0")
	if !ok {
		t.Fatal("yanked version not seeded")
	}
	ref := func(version string) *commonv1.AssetRef {
		return &commonv1.AssetRef{
			Namespace: "pilab",
			Kind:      commonv1.AssetKind_ASSET_KIND_AGENT,
			Name:      "alpha",
			Version:   version,
		}
	}

	for _, tc := range []struct {
		name        string
		req         *federationv1.ExportBundleRequest
		code        connect.Code
		wantVersion string
		wantStatus  commonv1.VersionStatus
	}{
		{
			name:        "ref with a version",
			req:         &federationv1.ExportBundleRequest{Ref: ref("1.0.0")},
			wantVersion: "1.0.0",
			wantStatus:  commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		},
		{
			name:        "ref without a version resolves the latest",
			req:         &federationv1.ExportBundleRequest{Ref: ref("")},
			wantVersion: "1.0.0",
			wantStatus:  commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		},
		{
			name: "ref and digest agreeing",
			req: &federationv1.ExportBundleRequest{
				Ref:          ref("1.0.0"),
				BundleDigest: bundle.bundleDigest,
			},
			wantVersion: "1.0.0",
			wantStatus:  commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		},
		{
			name: "bundle digest only",
			req: &federationv1.ExportBundleRequest{
				BundleDigest: bundle.bundleDigest,
			},
			wantVersion: "1.0.0",
			wantStatus:  commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		},
		{
			name: "bundle digest of a yanked version",
			req: &federationv1.ExportBundleRequest{
				BundleDigest: yanked.BundleDigest,
			},
			wantVersion: "0.9.0",
			wantStatus:  commonv1.VersionStatus_VERSION_STATUS_YANKED,
		},
		{
			name: "no selector",
			req:  &federationv1.ExportBundleRequest{},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "digest of an unsupported algorithm",
			req: &federationv1.ExportBundleRequest{
				BundleDigest: &commonv1.Digest{Algorithm: "md5", Value: strings.Repeat("a", 32)},
			},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "digest no version carries",
			req: &federationv1.ExportBundleRequest{
				BundleDigest: &commonv1.Digest{Algorithm: "sha256", Value: strings.Repeat("0", 64)},
			},
			code: connect.CodeNotFound,
		},
		{
			name: "digest of the draft version",
			req: &federationv1.ExportBundleRequest{
				BundleDigest: &commonv1.Digest{Algorithm: "sha256", Value: strings.Repeat("e", 64)},
			},
			code: connect.CodeNotFound,
		},
		{
			name: "ref and digest disagreeing",
			req: &federationv1.ExportBundleRequest{
				Ref:          ref("1.0.0"),
				BundleDigest: digestOf([]byte("something else")),
			},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "ref without a kind",
			req: &federationv1.ExportBundleRequest{Ref: &commonv1.AssetRef{
				Namespace: "pilab", Name: "alpha", Version: "1.0.0",
			}},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "ref without a namespace",
			req: &federationv1.ExportBundleRequest{Ref: &commonv1.AssetRef{
				Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha", Version: "1.0.0",
			}},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "ref without a name",
			req: &federationv1.ExportBundleRequest{Ref: &commonv1.AssetRef{
				Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Version: "1.0.0",
			}},
			code: connect.CodeInvalidArgument,
		},
		{
			name: "unknown asset",
			req: &federationv1.ExportBundleRequest{Ref: &commonv1.AssetRef{
				Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "missing", Version: "1.0.0",
			}},
			code: connect.CodeNotFound,
		},
		{
			name: "unknown version",
			req:  &federationv1.ExportBundleRequest{Ref: ref("9.9.9")},
			code: connect.CodeNotFound,
		},
		{
			name: "draft version",
			req:  &federationv1.ExportBundleRequest{Ref: ref("2.0.0-draft")},
			code: connect.CodeNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			header, _, err := exportOnce(t, newBundleClient(t, f), tc.req)
			if tc.code != connect.Code(0) { // zero: the export must succeed
				wantCode(t, err, tc.code)
				if err == nil {
					t.Fatalf("header = %v, want an error", header)
				}
				return
			}
			if err != nil {
				t.Fatalf("export: %v", err)
			}
			if header.GetRef().GetVersion() != tc.wantVersion {
				t.Fatalf("version = %q, want %q", header.GetRef().GetVersion(), tc.wantVersion)
			}
			if header.GetStatus() != tc.wantStatus {
				t.Fatalf("status = %s, want %s", header.GetStatus(), tc.wantStatus)
			}
		})
	}
}

func TestExportBundleRefWithoutLatestVersion(t *testing.T) {
	t.Parallel()

	f, store := newFederation(t)
	manifest := []byte(`{"name":"alpha","version":"1.0.0"}`)
	seedAssetVersion(t, store, &catalog.Asset{
		Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha",
		DisplayName: "alpha", CreatedAt: bundlePublishedAt, UpdatedAt: bundlePublishedAt,
	}, &catalog.Version{
		Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha", Version: "1.0.0",
		Status:         commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		Manifest:       manifest,
		ManifestDigest: digestOf(manifest),
		PublishedAt:    bundlePublishedAt,
	})
	client := newBundleClient(t, f)
	bare := &commonv1.AssetRef{
		Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha",
	}

	// An asset without a recorded latest has no default version to export,
	// but the explicit one still resolves.
	_, _, err := exportOnce(t, client, &federationv1.ExportBundleRequest{Ref: bare})
	wantCode(t, err, connect.CodeNotFound)

	bare.Version = "1.0.0"
	header, _, err := exportOnce(t, client, &federationv1.ExportBundleRequest{Ref: bare})
	if err != nil {
		t.Fatalf("export with an explicit version: %v", err)
	}
	if header.GetRef().GetVersion() != "1.0.0" {
		t.Fatalf("version = %q, want 1.0.0", header.GetRef().GetVersion())
	}
}

func TestExportBundleRejectsCorruptStoredData(t *testing.T) {
	t.Parallel()

	t.Run("manifest does not hash to its digest", func(t *testing.T) {
		t.Parallel()

		f, store := newFederation(t)
		seedAssetVersion(t, store, &catalog.Asset{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha",
			DisplayName: "alpha", LatestVer: "1.0.0", CreatedAt: bundlePublishedAt, UpdatedAt: bundlePublishedAt,
		}, &catalog.Version{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha", Version: "1.0.0",
			Status:         commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
			Manifest:       []byte("stored manifest"),
			ManifestDigest: digestOf([]byte("some other manifest")),
			PublishedAt:    bundlePublishedAt,
		})
		_, _, err := exportOnce(t, newBundleClient(t, f), &federationv1.ExportBundleRequest{
			Ref: &commonv1.AssetRef{
				Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha", Version: "1.0.0",
			},
		})
		wantCode(t, err, connect.CodeInternal)
	})

	t.Run("manifest declares an unusable file digest", func(t *testing.T) {
		t.Parallel()

		f, store := newFederation(t)
		manifest := []byte(`{"files":[{"path":"prompt.md","sha256":"not-a-digest"}]}`)
		seedAssetVersion(t, store, &catalog.Asset{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha",
			DisplayName: "alpha", LatestVer: "1.0.0", CreatedAt: bundlePublishedAt, UpdatedAt: bundlePublishedAt,
		}, &catalog.Version{
			Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha", Version: "1.0.0",
			Status:         commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
			Manifest:       manifest,
			ManifestDigest: digestOf(manifest),
			PublishedAt:    bundlePublishedAt,
		})
		_, _, err := exportOnce(t, newBundleClient(t, f), &federationv1.ExportBundleRequest{
			Ref: &commonv1.AssetRef{
				Namespace: "pilab", Kind: commonv1.AssetKind_ASSET_KIND_AGENT, Name: "alpha", Version: "1.0.0",
			},
		})
		wantCode(t, err, connect.CodeFailedPrecondition)
	})

	t.Run("stored blob does not hash to its descriptor", func(t *testing.T) {
		t.Parallel()

		base, store := newFederation(t)
		bundle := seedBundleVersion(t, base, store)
		corrupt := NewFederationWithBlobs(testHubID, testBaseURL, store, nil,
			rogueBlobs{blob: []byte("bytes that hash to nothing declared")})
		_, _, err := exportOnce(t, newBundleClient(t, corrupt), &federationv1.ExportBundleRequest{Ref: bundle.ref})
		wantCode(t, err, connect.CodeInternal)
	})
}

func TestImportBundleRejectsMalformedStreams(t *testing.T) {
	t.Parallel()

	data := []byte("0123456789")
	base := bundleHeader(t, data)

	oversized := proto.Clone(base).(*federationv1.BundleHeader)
	oversized.Manifest = bytes.Repeat([]byte("a"), maxImportManifestBytes+1)

	draft := proto.Clone(base).(*federationv1.BundleHeader)
	draft.Status = commonv1.VersionStatus_VERSION_STATUS_DRAFT

	unspecified := proto.Clone(base).(*federationv1.BundleHeader)
	unspecified.Status = commonv1.VersionStatus_VERSION_STATUS_UNSPECIFIED

	withoutVersion := proto.Clone(base).(*federationv1.BundleHeader)
	withoutVersion.Ref.Version = ""

	withoutNamespace := proto.Clone(base).(*federationv1.BundleHeader)
	withoutNamespace.Ref.Namespace = ""

	withoutKind := proto.Clone(base).(*federationv1.BundleHeader)
	withoutKind.Ref.Kind = commonv1.AssetKind_ASSET_KIND_UNSPECIFIED

	withoutManifestDigest := proto.Clone(base).(*federationv1.BundleHeader)
	withoutManifestDigest.ManifestDigest = digestOf([]byte("another manifest"))

	badBundleDigest := proto.Clone(base).(*federationv1.BundleHeader)
	badBundleDigest.BundleDigest = &commonv1.Digest{Algorithm: "sha256", Value: "zz"}

	badTimestamp := proto.Clone(base).(*federationv1.BundleHeader)
	badTimestamp.PublishedAt = &timestamppb.Timestamp{Seconds: 999999999999}

	tooManyFiles := proto.Clone(base).(*federationv1.BundleHeader)
	tooManyFiles.Files = make([]*federationv1.BundleFile, maxImportFiles+1)
	for i := range tooManyFiles.Files {
		tooManyFiles.Files[i] = bundleFile("f.bin", data, false)
	}

	overCap := proto.Clone(base).(*federationv1.BundleHeader)
	overCap.Files = []*federationv1.BundleFile{{
		File: &commonv1.FileDescriptor{
			Path:   "big.bin",
			Sha256: strings.Repeat("a", 64),
			Size:   maxImportBundleBytes + 1,
		},
		HasBytes: true,
	}}

	for _, tc := range []struct {
		name string
		msgs []*federationv1.ImportBundleRequest
		code connect.Code
	}{
		{"empty stream", nil, connect.CodeInvalidArgument},
		{"chunk before a header", []*federationv1.ImportBundleRequest{chunkRequest("f.bin", 0, data)}, connect.CodeInvalidArgument},
		{"header carrying data", []*federationv1.ImportBundleRequest{{
			Data: data,
			Part: &federationv1.ImportBundleRequest_Header{Header: base},
		}}, connect.CodeInvalidArgument},
		{"message without a part", []*federationv1.ImportBundleRequest{{}}, connect.CodeInvalidArgument},
		{"two headers", []*federationv1.ImportBundleRequest{headerRequest(base), headerRequest(base)}, connect.CodeInvalidArgument},
		{"header without a version", []*federationv1.ImportBundleRequest{headerRequest(withoutVersion)}, connect.CodeInvalidArgument},
		{"header without a namespace", []*federationv1.ImportBundleRequest{headerRequest(withoutNamespace)}, connect.CodeInvalidArgument},
		{"header without a kind", []*federationv1.ImportBundleRequest{headerRequest(withoutKind)}, connect.CodeInvalidArgument},
		{"draft status", []*federationv1.ImportBundleRequest{headerRequest(draft)}, connect.CodeInvalidArgument},
		{"unspecified status", []*federationv1.ImportBundleRequest{headerRequest(unspecified)}, connect.CodeInvalidArgument},
		{"manifest digest mismatch", []*federationv1.ImportBundleRequest{headerRequest(withoutManifestDigest)}, connect.CodeInvalidArgument},
		{"unusable bundle digest", []*federationv1.ImportBundleRequest{headerRequest(badBundleDigest)}, connect.CodeInvalidArgument},
		{"invalid published_at", []*federationv1.ImportBundleRequest{headerRequest(badTimestamp)}, connect.CodeInvalidArgument},
		{"oversized manifest", []*federationv1.ImportBundleRequest{headerRequest(oversized)}, connect.CodeResourceExhausted},
		{"too many files", []*federationv1.ImportBundleRequest{headerRequest(tooManyFiles)}, connect.CodeResourceExhausted},
		{"declared file over the cap", []*federationv1.ImportBundleRequest{headerRequest(overCap)}, connect.CodeResourceExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, _ := newFederation(t)
			_, err := sendBundle(t, newBundleClient(t, f), tc.msgs...)
			wantCode(t, err, tc.code)
		})
	}
}

// TestImportBundleAcceptsMissingBundleDigest covers the one optional field of
// the header: an origin that never recorded a bundle digest sends none, and
// the version stores it the same way.
func TestImportBundleAcceptsMissingBundleDigest(t *testing.T) {
	t.Parallel()

	data := []byte("0123456789")
	base := bundleHeader(t, data)
	base.BundleDigest = nil

	f, store := newFederation(t)
	resp, err := sendBundle(t, newBundleClient(t, f),
		headerRequest(base),
		chunkRequest("f.bin", 0, data),
	)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if resp.GetBundleDigest() != nil {
		t.Fatalf("bundle digest = %v, want none", resp.GetBundleDigest())
	}
	version, ok := store.GetVersion("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha", "1.0.0")
	if !ok {
		t.Fatal("imported version not found")
	}
	if version.BundleDigest != nil {
		t.Fatalf("stored bundle digest = %v, want none", version.BundleDigest)
	}
	if resp.GetBytesWritten() != uint64(len(base.GetManifest())+len(data)) {
		t.Fatalf("bytes written = %d, want %d", resp.GetBytesWritten(), len(base.GetManifest())+len(data))
	}
}

func TestImportBundleRejectsBadChunks(t *testing.T) {
	t.Parallel()

	data := []byte("0123456789") // the header declares exactly these 10 bytes
	base := bundleHeader(t, data)

	descriptorOnly := proto.Clone(base).(*federationv1.BundleHeader)
	descriptorOnly.Files = []*federationv1.BundleFile{bundleFile("f.bin", data, false)}

	for _, tc := range []struct {
		name string
		msgs []*federationv1.ImportBundleRequest
	}{
		{"undeclared path", []*federationv1.ImportBundleRequest{
			headerRequest(base),
			chunkRequest("other.bin", 0, data[:4]),
		}},
		{"gap in the offsets", []*federationv1.ImportBundleRequest{
			headerRequest(base),
			chunkRequest("f.bin", 0, data[:4]),
			chunkRequest("f.bin", 6, data[4:]),
		}},
		{"overlapping offsets", []*federationv1.ImportBundleRequest{
			headerRequest(base),
			chunkRequest("f.bin", 0, data[:4]),
			chunkRequest("f.bin", 2, data[4:]),
		}},
		{"empty chunk", []*federationv1.ImportBundleRequest{
			headerRequest(base),
			chunkRequest("f.bin", 0, nil),
		}},
		{"chunk past the declared size", []*federationv1.ImportBundleRequest{
			headerRequest(base),
			chunkRequest("f.bin", 0, bytes.Repeat([]byte("x"), len(data)+1)),
		}},
		{"chunk for a descriptor-only file", []*federationv1.ImportBundleRequest{
			headerRequest(descriptorOnly),
			chunkRequest("f.bin", 0, data[:4]),
		}},
		{"truncated file", []*federationv1.ImportBundleRequest{
			headerRequest(base),
			chunkRequest("f.bin", 0, data[:4]),
		}},
		{"content that does not hash", []*federationv1.ImportBundleRequest{
			headerRequest(base),
			chunkRequest("f.bin", 0, bytes.Repeat([]byte("x"), len(data))),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, _ := newFederation(t)
			_, err := sendBundle(t, newBundleClient(t, f), tc.msgs...)
			wantCode(t, err, connect.CodeInvalidArgument)
		})
	}
}

func TestImportBundleRejectsManifestDisagreement(t *testing.T) {
	t.Parallel()

	fileA := []byte("aaaa")
	fileB := []byte("bbbb")
	manifest := bundleManifest(t, []*commonv1.FileDescriptor{
		fileDescriptor("a.bin", fileA),
		fileDescriptor("b.bin", fileB),
	})
	badDigestManifest := []byte(`{"files":[{"path":"a.bin","sha256":"xyz"}]}`)
	noPathManifest := []byte(`{"files":[{"sha256":"` + strings.Repeat("a", 64) + `"}]}`)

	b := func(path string, data []byte) *federationv1.BundleFile {
		return bundleFile(path, data, false)
	}
	rehashed := func(path string, data []byte) *federationv1.BundleFile {
		f := b(path, data)
		f.File.Sha256 = digestOf([]byte("not what the manifest says")).GetValue()
		return f
	}
	unusable := &federationv1.BundleFile{
		File:     &commonv1.FileDescriptor{Path: "a.bin", Sha256: "zz", Size: 4},
		HasBytes: false,
	}

	for _, tc := range []struct {
		name string
		msgs []*federationv1.ImportBundleRequest
	}{
		{"header drops a declared file", []*federationv1.ImportBundleRequest{
			headerRequest(headerWithManifest(t, manifest, b("a.bin", fileA))),
		}},
		{"header invents a file", []*federationv1.ImportBundleRequest{
			headerRequest(headerWithManifest(t, manifest, b("a.bin", fileA), b("b.bin", fileB), b("c.bin", []byte("cccc")))),
		}},
		{"header disagrees with a manifest digest", []*federationv1.ImportBundleRequest{
			headerRequest(headerWithManifest(t, manifest, rehashed("a.bin", fileA), b("b.bin", fileB))),
		}},
		{"manifest declares an unusable digest", []*federationv1.ImportBundleRequest{
			headerRequest(headerWithManifest(t, badDigestManifest, b("a.bin", fileA))),
		}},
		{"manifest declares a file without a path", []*federationv1.ImportBundleRequest{
			headerRequest(headerWithManifest(t, noPathManifest, b("a.bin", fileA))),
		}},
		{"header file without a path", []*federationv1.ImportBundleRequest{
			headerRequest(headerWithManifest(t, manifest, b("", fileA), b("b.bin", fileB))),
		}},
		{"header file with an unusable digest", []*federationv1.ImportBundleRequest{
			headerRequest(headerWithManifest(t, manifest, unusable, b("b.bin", fileB))),
		}},
		{"header declares a file twice", []*federationv1.ImportBundleRequest{
			headerRequest(headerWithManifest(t, manifest, b("a.bin", fileA), b("a.bin", fileA), b("b.bin", fileB))),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, _ := newFederation(t)
			_, err := sendBundle(t, newBundleClient(t, f), tc.msgs...)
			wantCode(t, err, connect.CodeInvalidArgument)
		})
	}
}

func TestImportBundleIsIdempotent(t *testing.T) {
	t.Parallel()

	f, store := newFederation(t)
	bundle := seedBundleVersion(t, f, store)
	client := newBundleClient(t, f)
	_, msgs, err := exportOnce(t, client, &federationv1.ExportBundleRequest{Ref: bundle.ref})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	first, err := sendBundle(t, client, importMsgsFrom(msgs)...)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	assetAfterFirst, ok := store.GetAsset("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha")
	if !ok {
		t.Fatal("asset not found after the first import")
	}

	second, err := sendBundle(t, client, importMsgsFrom(msgs)...)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if first.GetBytesWritten() != second.GetBytesWritten() {
		t.Fatalf("bytes written: %d then %d", first.GetBytesWritten(), second.GetBytesWritten())
	}
	if !digestEqual(first.GetBundleDigest(), second.GetBundleDigest()) {
		t.Fatalf("bundle digests: %v then %v", first.GetBundleDigest(), second.GetBundleDigest())
	}

	asset, ok := store.GetAsset("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha")
	if !ok {
		t.Fatal("asset not found after the second import")
	}
	if !asset.CreatedAt.AsTime().Equal(assetAfterFirst.CreatedAt.AsTime()) {
		t.Fatalf("created at changed on re-import: %v then %v",
			asset.CreatedAt.AsTime(), assetAfterFirst.CreatedAt.AsTime())
	}
	version, ok := store.GetVersion("pilab", commonv1.AssetKind_ASSET_KIND_AGENT, "alpha", "1.0.0")
	if !ok || !bytes.Equal(version.Manifest, bundle.manifest) {
		t.Fatalf("version after re-import (ok=%v): manifest = %d bytes", ok, len(version.Manifest))
	}
	stored, ok := f.blobs.Get(digestOf(bundle.fileA))
	if !ok || !bytes.Equal(stored, bundle.fileA) {
		t.Fatalf("blob after re-import = %d bytes (ok=%v)", len(stored), ok)
	}
}

func TestBundleStreamsConcurrent(t *testing.T) {
	t.Parallel()

	f, store := newFederation(t)
	bundle := seedBundleVersion(t, f, store)
	client := newBundleClient(t, f)
	req := &federationv1.ExportBundleRequest{Ref: bundle.ref}

	header, msgs, err := exportOnce(t, client, req)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if header.GetRef().GetVersion() != "1.0.0" {
		t.Fatalf("version = %q, want 1.0.0", header.GetRef().GetVersion())
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		// Exports read the catalog and the blob store while imports write
		// them, so this exercises the locking under -race.
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				got, _, err := exportOnce(t, client, req)
				if err != nil {
					t.Errorf("concurrent export: %v", err)
					return
				}
				if !digestEqual(got.GetBundleDigest(), bundle.bundleDigest) {
					t.Errorf("concurrent export bundle digest = %v", got.GetBundleDigest())
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				if _, err := sendBundle(t, client, importMsgsFrom(msgs)...); err != nil {
					t.Errorf("concurrent import: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
