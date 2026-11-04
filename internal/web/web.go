// Package web renders the server-side pages and serves embedded assets.
package web

import (
	"embed"
	"html/template"
	"io/fs"

	"github.com/labstack/echo/v4"
)

//go:embed templates static
var assets embed.FS

// Handler renders the landing page.
type Handler struct {
	tpl     *template.Template
	version string
}

// New builds the web handler from the embedded templates.
func New(version string) (*Handler, error) {
	tpl, err := template.ParseFS(assets, "templates/*.tmpl")
	if err != nil {
		return nil, err
	}
	return &Handler{tpl: tpl, version: version}, nil
}

// Index renders the landing page.
func (h *Handler) Index(c echo.Context) error {
	data := map[string]any{
		"Version": h.version,
		"Title":   "AgentHub",
	}
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
	return h.tpl.ExecuteTemplate(c.Response().Writer, "index", data)
}

// StaticFS returns the embedded static asset filesystem.
func StaticFS() fs.FS {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
