package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"

	"google.golang.org/protobuf/types/known/timestamppb"

	"go.pilab.hu/agenthub/internal/catalog"
)

// Bundle streaming limits of M4.3 (offline export/import).
const (
	// exportChunkSize is the payload of one exported file chunk: large
	// enough to keep per-message overhead low, small enough that a reader
	// can bound what it buffers.
	exportChunkSize = 64 << 10
	// maxImportManifestBytes caps the manifest of an imported bundle.
	maxImportManifestBytes = 4 << 20
	// maxImportChunkBytes caps one imported file chunk.
	maxImportChunkBytes = 1 << 20
	// maxImportBundleBytes caps the file bytes of one import
	// (docs/architecture/federation.md §2: maxBundleBytes is 64 MiB).
	maxImportBundleBytes = 67108864
	// maxImportFiles caps the file count of one import.
	maxImportFiles = 1 << 15
)

// invalidBundlef and exhaustedBundlef build the errors of a malformed and of
// an oversized bundle stream.
func invalidBundlef(format string, args ...any) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, args...))
}

func exhaustedBundlef(format string, args ...any) error {
	return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf(format, args...))
}

// ExportBundle streams one catalog version for an air-gapped transfer.
//
// The stream opens with a single BundleHeader message: the version's
// identity and metadata, the manifest bytes with their digest, and one
// descriptor per file the manifest declares. A descriptor carries has_bytes
// when this hub holds that file's bytes; those files follow the header as
// one message per exportChunkSize bytes, each message naming its path and
// offset so the reader can reassemble and verify them as they arrive.
// Descriptors without bytes travel alone (M4.3 holds blobs only where an
// import stored them — a publish never uploads file content), so the
// importing hub records the file but stores nothing for it.
//
// The version is selected by ref when the request carries one (an empty
// version means the asset's latest), otherwise by bundle_digest; with both
// set they must agree. Drafts are not part of the federated catalog, so
// they are CodeNotFound exactly like an unknown version.
//
// Errors: an unusable selector or an unknown version is
// CodeInvalidArgument/CodeNotFound; a manifest whose file list this hub
// cannot turn into descriptors is CodeFailedPrecondition; and stored bytes
// that no longer hash to their recorded digest are CodeInternal — this hub
// must not export content it cannot vouch for.
func (f *Federation) ExportBundle(ctx context.Context, req *connect.Request[federationv1.ExportBundleRequest], stream *connect.ServerStream[federationv1.ExportBundleResponse]) error {
	a, v, err := f.selectExport(req.Msg)
	if err != nil {
		return err
	}
	descriptors, err := manifestDescriptors(v.Manifest)
	if err != nil {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("stored manifest of %s/%s/%s@%s: %w", a.Namespace, a.Kind, a.Name, v.Version, err))
	}
	if err := verifyManifestDigest(v.Manifest, v.ManifestDigest); err != nil {
		return connect.NewError(connect.CodeInternal,
			fmt.Errorf("stored manifest of %s/%s/%s@%s: %w", a.Namespace, a.Kind, a.Name, v.Version, err))
	}

	// Resolve every descriptor against the blob store before the header
	// goes out: has_bytes has to be decided up front, and a blob that does
	// not hash to its descriptor is a reason not to export it at all.
	type exportFile struct {
		descriptor *commonv1.FileDescriptor
		data       []byte
		present    bool
	}
	files := make([]exportFile, 0, len(descriptors))
	headerFiles := make([]*federationv1.BundleFile, 0, len(descriptors))
	for _, fd := range descriptors {
		digest := &commonv1.Digest{Algorithm: "sha256", Value: fd.GetSha256()}
		data, present := f.blobs.Get(digest)
		if present {
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != fd.GetSha256() {
				return connect.NewError(connect.CodeInternal,
					fmt.Errorf("stored blob for %q does not hash to %s", fd.GetPath(), digestText(digest)))
			}
			// The stored bytes are authoritative for the size: a manifest
			// that misreports it must not break the transfer.
			fd.Size = uint64(len(data)) //nolint:gosec // G115: len() is never negative
		}
		files = append(files, exportFile{descriptor: fd, data: data, present: present})
		headerFiles = append(headerFiles, &federationv1.BundleFile{File: fd, HasBytes: present})
	}

	header := &federationv1.BundleHeader{
		Ref: &commonv1.AssetRef{
			Hub:       f.hubID,
			Namespace: a.Namespace,
			Kind:      a.Kind,
			Name:      a.Name,
			Version:   v.Version,
		},
		ManifestDigest: cloneDigest(v.ManifestDigest),
		BundleDigest:   cloneDigest(v.BundleDigest),
		BundleSize:     v.BundleSize,
		Status:         v.Status,
		PublishedAt:    cloneTimestamp(v.PublishedAt),
		Manifest:       append([]byte(nil), v.Manifest...),
		Files:          headerFiles,
		DisplayName:    a.DisplayName,
		Summary:        a.Summary,
		Tags:           append([]string{}, a.Tags...),
		Categories:     append([]string{}, a.Categories...),
		License:        a.License,
		Repository:     a.Repository,
		Readme:         v.Readme,
		Changelog:      v.Changelog,
	}
	if err := stream.Send(&federationv1.ExportBundleResponse{
		Part: &federationv1.ExportBundleResponse_Header{Header: header},
	}); err != nil {
		return err
	}

	for _, ef := range files {
		if !ef.present {
			continue
		}
		for offset := 0; offset < len(ef.data); offset += exportChunkSize {
			end := offset + exportChunkSize
			if end > len(ef.data) {
				end = len(ef.data)
			}
			if err := stream.Send(&federationv1.ExportBundleResponse{
				Data: ef.data[offset:end],
				Part: &federationv1.ExportBundleResponse_File{
					File: &federationv1.BundleFileChunk{
						Path:   ef.descriptor.GetPath(),
						Offset: uint64(offset), //nolint:gosec // G115: len() is never negative
					},
				},
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// selectExport resolves the version an export request selects: by ref when
// one is given (ref.version empty means the asset's latest), otherwise by
// bundle digest. With both selectors set, the resolved version has to carry
// the digest the caller asked for.
func (f *Federation) selectExport(m *federationv1.ExportBundleRequest) (*catalog.Asset, *catalog.Version, error) {
	ref := m.GetRef()
	wantDigest := m.GetBundleDigest()
	hasRef := ref.GetNamespace() != "" || ref.GetName() != "" || ref.GetVersion() != "" ||
		ref.GetKind() != commonv1.AssetKind_ASSET_KIND_UNSPECIFIED
	hasDigest := wantDigest != nil && strings.TrimSpace(wantDigest.GetValue()) != ""

	switch {
	case !hasRef && !hasDigest:
		return nil, nil, invalidBundlef("ref or bundle_digest is required")
	case !hasRef:
		return f.exportByDigest(wantDigest)
	case ref.GetKind() == commonv1.AssetKind_ASSET_KIND_UNSPECIFIED:
		return nil, nil, invalidBundlef("ref requires a kind")
	case ref.GetNamespace() == "" || ref.GetName() == "":
		return nil, nil, invalidBundlef("ref requires a namespace and a name")
	}

	a, ok := f.store.GetAsset(ref.GetNamespace(), ref.GetKind(), ref.GetName())
	if !ok {
		return nil, nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("asset %s/%s/%s not found", ref.GetNamespace(), kindSlug(ref.GetKind()), ref.GetName()))
	}
	version := ref.GetVersion()
	if version == "" {
		version = a.LatestVer
	}
	if version == "" {
		return nil, nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("asset %s/%s/%s has no version to export", ref.GetNamespace(), kindSlug(ref.GetKind()), ref.GetName()))
	}
	v, ok := f.store.GetVersion(a.Namespace, a.Kind, a.Name, version)
	if !ok {
		return nil, nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("version %s/%s/%s@%s not found", ref.GetNamespace(), kindSlug(ref.GetKind()), ref.GetName(), version))
	}
	if !exportableVersion(v.Status) {
		return nil, nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("version %s/%s/%s@%s is a draft and is not part of the federated catalog",
				ref.GetNamespace(), kindSlug(ref.GetKind()), ref.GetName(), version))
	}
	if hasDigest && !digestMatches(v.BundleDigest, wantDigest) {
		return nil, nil, invalidBundlef("bundle_digest does not match the selected ref")
	}
	return a, v, nil
}

// digestMatches compares two digests the way selectors do: a missing
// algorithm means sha256 and hex values are case-insensitive, so a caller
// may send "sha256:AB…" or the bare value against a stored digest either
// way.
func digestMatches(a, b *commonv1.Digest) bool {
	if a == nil || b == nil {
		return false
	}
	algOf := func(d *commonv1.Digest) string {
		if alg := strings.ToLower(strings.TrimSpace(d.GetAlgorithm())); alg != "" {
			return alg
		}
		return "sha256"
	}
	return algOf(a) == algOf(b) &&
		strings.EqualFold(strings.TrimSpace(a.GetValue()), strings.TrimSpace(b.GetValue()))
}

// exportByDigest finds a version by its bundle digest in the catalog's
// asset order. Drafts are skipped: they are not federated, so asking for
// their digest is the same as asking for something unknown.
func (f *Federation) exportByDigest(digest *commonv1.Digest) (*catalog.Asset, *catalog.Version, error) {
	if alg := strings.ToLower(strings.TrimSpace(digest.GetAlgorithm())); alg != "" && alg != "sha256" {
		return nil, nil, invalidBundlef("bundle digest must use sha256, got %q", digest.GetAlgorithm())
	}
	for _, a := range f.store.ListAssets() {
		for _, v := range f.store.ListVersions(a.Namespace, a.Kind, a.Name) {
			if exportableVersion(v.Status) && digestMatches(v.BundleDigest, digest) {
				return a, v, nil
			}
		}
	}
	return nil, nil, connect.NewError(connect.CodeNotFound,
		fmt.Errorf("no version with bundle digest %s", digestText(digest)))
}

// exportableVersion reports whether a version is part of the federated
// catalog. Drafts and unset statuses never travel: the canonical change log
// of GetSnapshot/SyncChanges skips them, and export/import must agree with
// it about what exists.
func exportableVersion(status commonv1.VersionStatus) bool {
	switch status {
	case commonv1.VersionStatus_VERSION_STATUS_PUBLISHED,
		commonv1.VersionStatus_VERSION_STATUS_DEPRECATED,
		commonv1.VersionStatus_VERSION_STATUS_YANKED:
		return true
	default:
		return false
	}
}

// ImportBundle accepts a bundle stream and stores the version it carries.
//
// The stream mirrors ExportBundle: it must open with one BundleHeader and
// may only follow with chunks for files the header declared with has_bytes.
// Everything is verified before anything is stored — the manifest hashes to
// its digest, the file list agrees with what the manifest declares, every
// file arrives in order at its declared size and hashes to the digest the
// descriptor carries — and only then are the blobs filed and the asset and
// version written to the catalog store, so a truncated or tampered stream
// never becomes a version. Re-importing a bundle is idempotent: the asset
// and the version are upserted, and content-addressed blobs file the same
// bytes under the same key.
//
// Errors: a malformed, incomplete or digest-mismatched stream is
// CodeInvalidArgument, declared content past the limits above is
// CodeResourceExhausted, and catalog or blob store failures are CodeInternal.
func (f *Federation) ImportBundle(ctx context.Context, stream *connect.ClientStream[federationv1.ImportBundleRequest]) (*connect.Response[federationv1.ImportBundleResponse], error) {
	imp := &bundleImporter{}
	for stream.Receive() {
		if err := imp.accept(stream.Msg()); err != nil {
			return nil, err
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if err := imp.finish(); err != nil {
		return nil, err
	}
	if err := f.storeBundle(imp); err != nil {
		return nil, err
	}
	return connect.NewResponse(&federationv1.ImportBundleResponse{
		BundleDigest: cloneDigest(imp.header.GetBundleDigest()),
		BytesWritten: uint64(imp.manifestBytes + imp.fileBytes), //nolint:gosec // G115: both counts are capped
	}), nil
}

// bundleImporter accumulates and verifies one imported bundle. Nothing is
// written to a store until finish has accepted the whole stream.
type bundleImporter struct {
	header *federationv1.BundleHeader
	// order lists the declared file paths in header order, so verification
	// errors do not depend on map iteration.
	order []string
	// files holds the declared files by path; data collects the bytes
	// received for the ones declared with has_bytes.
	files         map[string]*importFile
	manifestBytes int
	fileBytes     int
}

// importFile is one declared file of an import in flight.
type importFile struct {
	desc     *commonv1.FileDescriptor
	hasBytes bool
	data     []byte
	// next is the offset the next chunk of this file must start at.
	next uint64
}

// accept folds one stream message into the import, in stream order.
func (b *bundleImporter) accept(msg *federationv1.ImportBundleRequest) error {
	switch part := msg.GetPart().(type) {
	case *federationv1.ImportBundleRequest_Header:
		if b.header != nil {
			return invalidBundlef("bundle stream carries more than one header")
		}
		if len(msg.GetData()) != 0 {
			return invalidBundlef("header message must not carry data")
		}
		return b.start(part.Header)
	case *federationv1.ImportBundleRequest_File:
		if b.header == nil {
			return invalidBundlef("bundle stream must start with a header")
		}
		return b.chunk(part.File, msg.GetData())
	default:
		return invalidBundlef("bundle message carries neither a header nor a file chunk")
	}
}

// start validates the header and the file list it declares, including the
// agreement between the descriptors and the manifest they claim to come
// from.
func (b *bundleImporter) start(h *federationv1.BundleHeader) error {
	if h == nil {
		return invalidBundlef("bundle stream opens with an empty header")
	}
	ref := h.GetRef()
	switch {
	case strings.TrimSpace(ref.GetNamespace()) == "":
		return invalidBundlef("header ref requires a namespace")
	case ref.GetKind() == commonv1.AssetKind_ASSET_KIND_UNSPECIFIED:
		return invalidBundlef("header ref requires a kind")
	case strings.TrimSpace(ref.GetName()) == "":
		return invalidBundlef("header ref requires a name")
	case strings.TrimSpace(ref.GetVersion()) == "":
		return invalidBundlef("header ref requires a version")
	}
	if !exportableVersion(h.GetStatus()) {
		return invalidBundlef("header status %q cannot be imported", versionStatus(h.GetStatus()))
	}
	manifest := h.GetManifest()
	if len(manifest) > maxImportManifestBytes {
		return exhaustedBundlef("manifest is %d bytes, limit is %d", len(manifest), maxImportManifestBytes)
	}
	if err := verifyManifestDigest(manifest, h.GetManifestDigest()); err != nil {
		return invalidBundlef("manifest digest: %w", err)
	}
	// The bundle digest cannot be re-derived (it covers the tar archive the
	// hub never built), so it is validated for shape and carried through
	// unchanged — federation.md's F3, "the bundle digest survives the hop".
	// An origin that never recorded one may send none.
	if bd := h.GetBundleDigest(); bd.GetValue() != "" || bd.GetAlgorithm() != "" {
		if _, err := catalog.BlobKey(bd); err != nil {
			return invalidBundlef("bundle digest: %w", err)
		}
	}
	if ts := h.GetPublishedAt(); ts != nil {
		if err := ts.CheckValid(); err != nil {
			return invalidBundlef("published_at: %w", err)
		}
	}
	if len(h.GetFiles()) > maxImportFiles {
		return exhaustedBundlef("bundle declares %d files, limit is %d", len(h.GetFiles()), maxImportFiles)
	}

	files := make(map[string]*importFile, len(h.GetFiles()))
	order := make([]string, 0, len(h.GetFiles()))
	var declared uint64
	for _, bf := range h.GetFiles() {
		fd := bf.GetFile()
		path := fd.GetPath()
		if strings.TrimSpace(path) == "" {
			return invalidBundlef("bundle declares a file without a path")
		}
		if _, dup := files[path]; dup {
			return invalidBundlef("file %q is declared twice", path)
		}
		if _, err := fileDigest(fd); err != nil {
			return invalidBundlef("file %q: %w", path, err)
		}
		if bf.GetHasBytes() {
			if fd.GetSize() > maxImportBundleBytes {
				return exhaustedBundlef("file %q is %d bytes, limit is %d", path, fd.GetSize(), maxImportBundleBytes)
			}
			declared += fd.GetSize()
			if declared > maxImportBundleBytes {
				return exhaustedBundlef("bundle declares %d file bytes, limit is %d", declared, maxImportBundleBytes)
			}
		}
		files[path] = &importFile{desc: fd, hasBytes: bf.GetHasBytes()}
		order = append(order, path)
	}

	// The manifest is authoritative for the file list: when it declares one,
	// the header must agree with it on paths and digests.
	declaredFiles, err := manifestDescriptors(manifest)
	if err != nil {
		return invalidBundlef("manifest: %w", err)
	}
	if len(declaredFiles) > 0 {
		if len(declaredFiles) != len(files) {
			return invalidBundlef("manifest declares %d files, header lists %d", len(declaredFiles), len(files))
		}
		for _, fd := range declaredFiles {
			got, ok := files[fd.GetPath()]
			if !ok {
				return invalidBundlef("manifest file %q is missing from the header", fd.GetPath())
			}
			if got.desc.GetSha256() != fd.GetSha256() {
				return invalidBundlef("file %q disagrees with the digest the manifest declares", fd.GetPath())
			}
		}
	}

	b.header = h
	b.files = files
	b.order = order
	b.manifestBytes = len(manifest)
	return nil
}

// chunk appends one file chunk, enforcing order and the declared size.
func (b *bundleImporter) chunk(fc *federationv1.BundleFileChunk, data []byte) error {
	if fc == nil {
		return invalidBundlef("file chunk carries no file")
	}
	f, ok := b.files[fc.GetPath()]
	if !ok {
		return invalidBundlef("file %q is not declared in the header", fc.GetPath())
	}
	if !f.hasBytes {
		return invalidBundlef("file %q was declared without bytes", fc.GetPath())
	}
	if len(data) == 0 {
		return invalidBundlef("chunk of %q at offset %d carries no bytes", fc.GetPath(), fc.GetOffset())
	}
	if len(data) > maxImportChunkBytes {
		return exhaustedBundlef("chunk of %q is %d bytes, limit is %d", fc.GetPath(), len(data), maxImportChunkBytes)
	}
	if fc.GetOffset() != f.next {
		return invalidBundlef("chunk of %q starts at offset %d, want %d", fc.GetPath(), fc.GetOffset(), f.next)
	}
	if f.next+uint64(len(data)) > f.desc.GetSize() { //nolint:gosec // G115: len() is never negative
		return invalidBundlef("file %q would grow past its declared %d bytes", fc.GetPath(), f.desc.GetSize())
	}
	f.data = append(f.data, data...)
	f.next += uint64(len(data)) //nolint:gosec // G115: len() is never negative
	b.fileBytes += len(data)
	return nil
}

// finish verifies that every file declared with has_bytes arrived complete
// and unaltered. Files declared without bytes never receive chunks (chunk
// rejects them), so they are complete by construction.
func (b *bundleImporter) finish() error {
	if b.header == nil {
		return invalidBundlef("bundle stream carries no header")
	}
	for _, path := range b.order {
		f := b.files[path]
		if !f.hasBytes {
			continue
		}
		if uint64(len(f.data)) != f.desc.GetSize() { //nolint:gosec // G115: len() is never negative
			return invalidBundlef("file %q is truncated: got %d of %d bytes", path, len(f.data), f.desc.GetSize())
		}
		sum := sha256.Sum256(f.data)
		if hex.EncodeToString(sum[:]) != f.desc.GetSha256() {
			return invalidBundlef("file %q does not hash to its digest", path)
		}
	}
	return nil
}

// storeBundle files the verified bytes into the blob store and upserts the
// asset and the version into the catalog store. It runs only after finish,
// so a rejected stream leaves no trace. Blobs are written first: they are
// content-addressed and harmless on their own, while a version whose blobs
// are missing would be worse than an unused blob.
func (f *Federation) storeBundle(b *bundleImporter) error {
	h := b.header
	ref := h.GetRef()
	for _, path := range b.order {
		file := b.files[path]
		if !file.hasBytes {
			continue
		}
		digest, err := fileDigest(file.desc)
		if err != nil {
			// start already validated every descriptor.
			return connect.NewError(connect.CodeInternal, fmt.Errorf("file %q: %w", path, err))
		}
		if err := f.blobs.Put(digest, file.data); err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("store file %q: %w", path, err))
		}
	}

	now := timestamppb.Now()
	createdAt := now
	if existing, ok := f.store.GetAsset(ref.GetNamespace(), ref.GetKind(), ref.GetName()); ok && existing.CreatedAt != nil {
		createdAt = existing.CreatedAt
	}
	displayName := h.GetDisplayName()
	if displayName == "" {
		displayName = ref.GetName()
	}
	summary := h.GetSummary()
	if summary == "" {
		summary = parseManifestMeta(h.GetManifest()).Summary
	}
	if displayName == ref.GetName() {
		if m := parseManifestMeta(h.GetManifest()).DisplayName; m != "" {
			displayName = m
		}
	}
	// Resolve the icon blob: the manifest's `icon` is a relative path into
	// the bundle; the matching file's sha256 is the content address.
	iconDigest := ""
	if iconPath := parseManifestMeta(h.GetManifest()).Icon; iconPath != "" {
		if file, ok := b.files[iconPath]; ok && file.hasBytes && file.desc.GetSha256() != "" {
			iconDigest = "sha256:" + file.desc.GetSha256()
		}
	}
	if err := f.store.UpsertAsset(&catalog.Asset{
		Namespace:      ref.GetNamespace(),
		Kind:           ref.GetKind(),
		Name:           ref.GetName(),
		DisplayName:    displayName,
		Summary:        summary,
		IconDigest:     iconDigest,
		Tags:           append([]string{}, h.GetTags()...),
		Categories:     append([]string{}, h.GetCategories()...),
		License:        h.GetLicense(),
		Repository:     h.GetRepository(),
		LatestVer:      ref.GetVersion(),
		LandingVisible: true,
		CreatedAt:      createdAt,
		UpdatedAt:      now,
	}); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}

	published := h.GetPublishedAt()
	if published == nil {
		published = now
	}
	if err := f.store.UpsertVersion(&catalog.Version{
		Namespace:      ref.GetNamespace(),
		Kind:           ref.GetKind(),
		Name:           ref.GetName(),
		Version:        ref.GetVersion(),
		Status:         h.GetStatus(),
		Manifest:       append([]byte(nil), h.GetManifest()...),
		ManifestDigest: cloneDigest(h.GetManifestDigest()),
		BundleDigest:   cloneDigest(h.GetBundleDigest()),
		BundleSize:     h.GetBundleSize(),
		PublishedAt:    published,
		Readme:         h.GetReadme(),
		Changelog:      h.GetChangelog(),
	}); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return nil
}

// verifyManifestDigest checks that manifest hashes to digest, which has to
// be a usable sha256 digest.
func verifyManifestDigest(manifest []byte, digest *commonv1.Digest) error {
	key, err := catalog.BlobKey(digest)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(manifest)
	got := hex.EncodeToString(sum[:])
	if key != "sha256:"+got {
		return fmt.Errorf("manifest hashes to sha256:%s, want %s", got, key)
	}
	return nil
}

// manifestDescriptors extracts the file descriptors a manifest declares.
//
// A manifest that is not JSON, or that declares no "files", declares no
// files at all: its bytes still travel, but there is nothing to stream
// after the header. Every declared file must carry a path and a usable
// sha256 — a manifest this hub cannot turn into descriptors is not exported,
// and a stream carrying such a manifest is not imported.
func manifestDescriptors(manifest []byte) ([]*commonv1.FileDescriptor, error) {
	var doc struct {
		Files []struct {
			Path       string `json:"path"`
			MediaType  string `json:"mediaType"`
			Size       uint64 `json:"size"`
			SHA256     string `json:"sha256"`
			Mode       uint32 `json:"mode"`
			Executable bool   `json:"executable"`
		} `json:"files"`
	}
	if err := json.Unmarshal(manifest, &doc); err != nil {
		// Not a JSON manifest: it declares no files (see doc comment).
		return nil, nil
	}
	out := make([]*commonv1.FileDescriptor, 0, len(doc.Files))
	for _, mf := range doc.Files {
		if strings.TrimSpace(mf.Path) == "" {
			return nil, errors.New("manifest declares a file without a path")
		}
		sum, err := sha256Hex(mf.SHA256)
		if err != nil {
			return nil, fmt.Errorf("manifest file %q: %w", mf.Path, err)
		}
		out = append(out, &commonv1.FileDescriptor{
			Path:       mf.Path,
			MediaType:  mf.MediaType,
			Size:       mf.Size,
			Sha256:     sum,
			Mode:       mf.Mode,
			Executable: mf.Executable,
		})
	}
	return out, nil
}

// fileDigest parses a descriptor's sha256 into the digest that keys the
// blob store.
func fileDigest(fd *commonv1.FileDescriptor) (*commonv1.Digest, error) {
	value, err := sha256Hex(fd.GetSha256())
	if err != nil {
		return nil, err
	}
	return &commonv1.Digest{Algorithm: "sha256", Value: value}, nil
}

// sha256Hex normalizes a hex sha256 value — bare or "sha256:"-prefixed, in
// any case — to the lowercase bare hex form descriptors use.
func sha256Hex(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	alg, rest, hasAlg := strings.Cut(trimmed, ":")
	if !hasAlg {
		alg, rest = "sha256", trimmed
	}
	key, err := catalog.BlobKey(&commonv1.Digest{Algorithm: alg, Value: rest})
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(key, "sha256:"), nil
}
