package server

import (
	"io"
	"net/http"

	"github.com/labstack/echo/v4"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	"go.pilab.hu/agenthub/internal/catalog"
)

// getBlob serves a content-addressed blob; digests are not secret.
func getBlob(blobs catalog.BlobStore) echo.HandlerFunc {
	return func(c echo.Context) error {
		data, ok := blobs.Get(&commonv1.Digest{Algorithm: "sha256", Value: c.Param("sha")})
		if !ok {
			return echo.NewHTTPError(http.StatusNotFound, "blob not found")
		}
		return c.Blob(http.StatusOK, "application/octet-stream", data)
	}
}

// putBlob stores an uploaded blob; the store rejects bytes that do not hash
// to the digest in the URL. Uploading requires an identified caller.
func putBlob(blobs catalog.BlobStore, maxBytes int64) echo.HandlerFunc {
	return func(c echo.Context) error {
		if SubjectFromContext(c.Request().Context()) == "" {
			return echo.NewHTTPError(http.StatusUnauthorized, "authentication required")
		}
		body := io.Reader(c.Request().Body)
		if maxBytes > 0 {
			body = http.MaxBytesReader(c.Response(), c.Request().Body, maxBytes)
		}
		data, err := io.ReadAll(body)
		if err != nil {
			return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "blob too large")
		}
		if err := blobs.Put(&commonv1.Digest{Algorithm: "sha256", Value: c.Param("sha")}, data); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return c.NoContent(http.StatusNoContent)
	}
}
