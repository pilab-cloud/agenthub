// Air-gap transfer CLI (M4.4): `agenthub export` streams one catalog version
// out of a hub into a JSON-lines archive and `agenthub import` replays such an
// archive into a hub. The archive is the export stream itself — one message
// per line, protojson-encoded — so the BundleHeader opens it, every later
// line is a file chunk and its bytes travel base64-encoded in `data`. The
// receiving hub stays the only side that verifies the transfer.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"connectrpc.com/connect"

	federationv1 "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1"
	federationv1connect "go.pilab.hu/agenthub/gen/go/agenthub/federation/v1/federationv1connect"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// maxArchiveLine bounds one line of a transfer archive. The hub accepts file
// chunks of at most 1 MiB, which base64-encode to roughly 1.4 MiB, so a line
// past this limit is corrupt or hostile rather than a record worth reading.
const maxArchiveLine = 2 << 20

// exportOptions controls where an export writes its archive and which hub it
// dials. The zero value exports to stdout from the configured hub.
type exportOptions struct {
	Hub        string       // hub base URL; resolveHub fills it in from the flag or config
	Output     string       // archive path; "-" or empty writes to stdout
	HTTPClient *http.Client // transport; nil means a client without a whole-request timeout
}

// importOptions controls which hub an import dials.
type importOptions struct {
	Hub        string       // hub base URL; resolveHub fills it in from the flag or config
	HTTPClient *http.Client // transport; nil means a client without a whole-request timeout
}

// newExportCmd is `agenthub export <ref>`: pull one version out of a hub as a
// transfer archive for an air-gapped import.
func newExportCmd() *cobra.Command {
	var hubFlag, output, configPath string
	cmd := &cobra.Command{
		Use:   "export <asset-ref>",
		Short: "Export one catalog version to a transfer archive",
		Long: strings.TrimSpace(`
Streams one version of the catalog from a hub and writes it as a JSON-lines
transfer archive: the export stream's BundleHeader first, then one record per
file chunk with its bytes base64-encoded in "data".

The archive carries the stream itself, so ` + "`agenthub import`" + ` replays it
into another hub over the air gap. An empty version in the ref selects the
asset's latest published version.

Progress goes to stderr, so stdout stays a clean archive when -o is omitted
or is "-".`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExport(cmd.Context(), cmd.ErrOrStderr(), cmd.OutOrStdout(), args[0],
				exportOptions{
					Hub:    resolveHub(cmd.Context(), hubFlag, configPath),
					Output: output,
				})
		},
	}
	cmd.Flags().StringVar(&hubFlag, "hub", "", "hub URL")
	cmd.Flags().StringVarP(&output, "output", "o", "-", "archive file to write (`-` writes to stdout)")
	cmd.Flags().StringVar(&configPath, "config", "", "path to config file")
	return cmd
}

// newImportCmd is `agenthub import <file>`: replay a transfer archive into a
// hub over the air gap.
func newImportCmd() *cobra.Command {
	var hubFlag, configPath string
	cmd := &cobra.Command{
		Use:   "import <archive>",
		Short: "Import a transfer archive into a hub",
		Long: strings.TrimSpace(`
Replays a JSON-lines transfer archive (the output of ` + "`agenthub export`" + `)
into a hub over Federation.ImportBundle. The hub verifies the whole stream —
manifest digest, file list, chunk order and every file's sha256 — before it
stores anything, so a truncated or tampered archive is rejected instead of
half imported.

Use "-" to read the archive from stdin.`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd.Context(), cmd.ErrOrStderr(), cmd.InOrStdin(), args[0],
				importOptions{
					Hub: resolveHub(cmd.Context(), hubFlag, configPath),
				})
		},
	}
	cmd.Flags().StringVar(&hubFlag, "hub", "", "hub URL")
	cmd.Flags().StringVar(&configPath, "config", "", "path to config file")
	return cmd
}

// runExport streams one version out of the hub and writes the archive.
//
// The archive holds the export stream itself, one message per JSON line: the
// BundleHeader on line 1, then one file chunk per line with its bytes
// base64-encoded in "data". A file destination is written to a temporary
// file and renamed only on success, so a failed export never leaves a
// truncated archive behind to be carried across the air gap. Progress goes
// to the progress writer (stderr), never into the archive itself.
func runExport(ctx context.Context, progress, stdout io.Writer, refArg string, opts exportOptions) error {
	ref, err := parseAssetRef(refArg)
	if err != nil {
		return err
	}
	stream, err := federationClient(opts.Hub, opts.HTTPClient).ExportBundle(ctx,
		connect.NewRequest(&federationv1.ExportBundleRequest{Ref: ref}))
	if err != nil {
		return err
	}

	aw, err := newArchiveWriter(opts.Output, stdout)
	if err != nil {
		return err
	}
	// No-op once the archive has been committed to its final path.
	defer aw.discard()

	var header *federationv1.BundleHeader
	var streamed int
	for stream.Receive() {
		msg := stream.Msg()
		if h := msg.GetHeader(); h != nil {
			if header != nil {
				return errors.New("hub sent more than one bundle header")
			}
			header = h
		}
		if err := aw.writeRecord(msg); err != nil {
			return fmt.Errorf("write archive: %w", err)
		}
		streamed += len(msg.GetData())
	}
	if err := stream.Err(); err != nil {
		return err
	}
	if header == nil {
		return errors.New("hub sent no bundle header")
	}
	if err := aw.commit(); err != nil {
		return err
	}
	fmt.Fprintf(progress, "exported %s: files=%d bytes=%d\n",
		assetRefString(header.GetRef()), len(header.GetFiles()), streamed)
	return nil
}

// runImport replays a transfer archive into the hub.
//
// The first record must be the bundle header; every later record is a file
// chunk. Records are forwarded to ImportBundle as they are read, so only the
// hub ever buffers the bundle — it is the side that has to verify it before
// storing anything. Anything wrong with the archive itself (a missing or
// duplicate header, a record that is neither) is caught here, before a byte
// is sent, instead of surfacing as a confusing hub-side rejection.
func runImport(ctx context.Context, progress io.Writer, stdin io.Reader, path string, opts importOptions) error {
	reader, err := openArchiveReader(path, stdin)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	scanner := newArchiveScanner(reader)

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		return errors.New("archive is empty")
	}
	first, err := decodeRecord(scanner.Bytes())
	if err != nil {
		return err
	}
	header, ok := first.GetPart().(*federationv1.ImportBundleRequest_Header)
	if !ok {
		return errors.New("archive does not start with a bundle header")
	}

	stream := federationClient(opts.Hub, opts.HTTPClient).ImportBundle(ctx)
	// abandon ends a half-sent stream: the hub rejects a stream that stops
	// early without storing anything, and closing the request releases the
	// connection instead of leaving it to the garbage collector.
	abandon := func() { _, _ = stream.CloseAndReceive() }

	if err := sendImportRecord(stream, first); err != nil {
		return err
	}
	for scanner.Scan() {
		rec, err := decodeRecord(scanner.Bytes())
		if err != nil {
			abandon()
			return err
		}
		if _, isHeader := rec.GetPart().(*federationv1.ImportBundleRequest_Header); isHeader {
			abandon()
			return errors.New("archive carries more than one bundle header")
		}
		if err := sendImportRecord(stream, rec); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		abandon()
		return fmt.Errorf("read archive: %w", err)
	}

	resp, err := stream.CloseAndReceive()
	if err != nil {
		return err
	}
	fmt.Fprintf(progress, "imported %s: bytes=%d\n",
		assetRefString(header.Header.GetRef()), resp.Msg.GetBytesWritten())
	return nil
}

// sendImportRecord forwards one record to the hub. A hub that rejects the
// stream while the CLI is still sending reports itself as io.EOF on Send —
// the real error only comes back from CloseAndReceive, which also ends the
// stream, so it is drained here and its error returned in Send's place.
func sendImportRecord(stream *connect.ClientStreamForClient[federationv1.ImportBundleRequest, federationv1.ImportBundleResponse],
	record *federationv1.ImportBundleRequest,
) error {
	err := stream.Send(record)
	if err == nil || !errors.Is(err, io.EOF) {
		return err
	}
	if _, cerr := stream.CloseAndReceive(); cerr != nil {
		return cerr
	}
	return err
}

// federationClient dials the hub for a transfer. A transfer stream must not
// be cut off by a whole-request timeout — a 64 MiB bundle over a slow link
// legitimately takes minutes — so the default client has none: it keeps the
// default transport (which bounds how long a dead host can stall the dial)
// and the command's context bounds the call instead.
func federationClient(hub string, httpClient *http.Client) federationv1connect.FederationServiceClient {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return federationv1connect.NewFederationServiceClient(httpClient, normalizeHubURL(hub))
}

// archiveWriter is the destination of an export. "-" (or an empty path)
// writes to stdout as-is; any other path is written to a temporary file in
// the target directory and renamed on commit, so the archive appears only
// once it is complete. Temporary files are created 0600: an archive can
// carry content that is not public yet.
type archiveWriter struct {
	w     io.Writer
	file  *os.File // nil when writing to stdout
	tmp   string
	final string
	done  bool
}

// newArchiveWriter opens the archive destination for an export.
func newArchiveWriter(path string, stdout io.Writer) (*archiveWriter, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "-" {
		if stdout == nil {
			return nil, errors.New("no archive destination")
		}
		return &archiveWriter{w: stdout}, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create archive directory %q: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".agenthub-export-*")
	if err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}
	return &archiveWriter{w: f, file: f, tmp: f.Name(), final: path}, nil
}

// writeRecord appends one message to the archive as a single JSON line.
func (a *archiveWriter) writeRecord(msg proto.Message) error {
	line, err := protojson.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = a.w.Write(append(line, '\n'))
	return err
}

// commit publishes the archive at its final path. For stdout it only marks
// the stream complete, since there is nothing to rename.
func (a *archiveWriter) commit() error {
	a.done = true
	if a.file == nil {
		return nil
	}
	if err := a.file.Close(); err != nil {
		_ = os.Remove(a.tmp)
		return fmt.Errorf("close archive: %w", err)
	}
	if err := os.Rename(a.tmp, a.final); err != nil {
		_ = os.Remove(a.tmp)
		return fmt.Errorf("publish archive %q: %w", a.final, err)
	}
	a.tmp = ""
	return nil
}

// discard drops an uncommitted archive; after commit it does nothing.
func (a *archiveWriter) discard() {
	if a.done || a.file == nil {
		return
	}
	a.done = true
	_ = a.file.Close()
	_ = os.Remove(a.tmp)
}

// openArchiveReader opens the archive to import: a file path, or stdin for
// "-" (and for an empty argument).
func openArchiveReader(path string, stdin io.Reader) (io.ReadCloser, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "-" {
		if stdin == nil {
			return nil, errors.New("no archive to read")
		}
		return io.NopCloser(stdin), nil
	}
	f, err := os.Open(path) //nolint:gosec // G304: the archive path is the command's argument
	if err != nil {
		return nil, fmt.Errorf("open archive %q: %w", path, err)
	}
	return f, nil
}

// newArchiveScanner reads a JSON-lines archive with a line limit that fits
// the largest record the hub accepts (a 1 MiB chunk, base64-encoded).
func newArchiveScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), maxArchiveLine)
	return s
}

// decodeRecord parses one archive line into the request it becomes on the
// import stream. Records are the messages of the export stream, so a line is
// decoded as that message and its parts copied across: the two messages
// share the data/header/file layout, but copying keeps the archive format
// from depending on that coincidence staying true.
func decodeRecord(line []byte) (*federationv1.ImportBundleRequest, error) {
	var rec federationv1.ExportBundleResponse
	if err := protojson.Unmarshal(line, &rec); err != nil {
		return nil, fmt.Errorf("archive record: %w", err)
	}
	switch part := rec.GetPart().(type) {
	case *federationv1.ExportBundleResponse_Header:
		if len(rec.GetData()) != 0 {
			return nil, errors.New("archive header record carries data")
		}
		return &federationv1.ImportBundleRequest{
			Part: &federationv1.ImportBundleRequest_Header{Header: part.Header},
		}, nil
	case *federationv1.ExportBundleResponse_File:
		return &federationv1.ImportBundleRequest{
			Data: rec.GetData(),
			Part: &federationv1.ImportBundleRequest_File{File: part.File},
		}, nil
	default:
		return nil, errors.New("archive record carries neither a header nor a file chunk")
	}
}
