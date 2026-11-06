// Package web renders the server-side pages and serves embedded assets.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	commonv1 "go.pilab.hu/agenthub/gen/go/agenthub/common/v1"
	"go.pilab.hu/agenthub/internal/catalog"
	"go.pilab.hu/agenthub/internal/registry"
)

//go:embed templates static
var assets embed.FS

// Handler renders the server-side pages.
type Handler struct {
	tpl           *template.Template
	version       string
	CatalogStore  catalog.Store
	RegistryStore registry.Store
	BlobStore     catalog.BlobStore
}

// New builds the web handler from the embedded templates.
func New(version string) (*Handler, error) {
	return NewWithStores(version, nil, nil)
}

// NewWithStores builds the web handler with optional backing stores.
func NewWithStores(version string, regStore registry.Store, catStore catalog.Store) (*Handler, error) {
	funcs := template.FuncMap{
		"kindSlug": func(k commonv1.AssetKind) string {
			switch k {
			case commonv1.AssetKind_ASSET_KIND_AGENT:
				return "agent"
			case commonv1.AssetKind_ASSET_KIND_SKILL:
				return "skill"
			case commonv1.AssetKind_ASSET_KIND_TOOL:
				return "tool"
			case commonv1.AssetKind_ASSET_KIND_PROMPT:
				return "prompt"
			case commonv1.AssetKind_ASSET_KIND_PACK:
				return "pack"
			}
			return "asset"
		},
		"kindIcon": func(k commonv1.AssetKind) string {
			switch k {
			case commonv1.AssetKind_ASSET_KIND_AGENT:
				return "🤖"
			case commonv1.AssetKind_ASSET_KIND_SKILL:
				return "🧠"
			case commonv1.AssetKind_ASSET_KIND_TOOL:
				return "🔧"
			case commonv1.AssetKind_ASSET_KIND_PROMPT:
				return "💬"
			case commonv1.AssetKind_ASSET_KIND_PACK:
				return "📦"
			}
			return "📄"
		},
	}
	tpl, err := template.New("").Funcs(funcs).ParseFS(assets, "templates/*.tmpl")
	if err != nil {
		return nil, err
	}
	if regStore == nil {
		regStore = registry.NewMemStore()
	}
	if catStore == nil {
		catStore = catalog.NewMemStore()
	}
	return &Handler{tpl: tpl, version: version, RegistryStore: regStore, CatalogStore: catStore}, nil
}

// Index renders the landing page.
func (h *Handler) Index(c echo.Context) error {
	assets := []any{}
	if h.CatalogStore != nil {
		for _, a := range h.CatalogStore.ListAssets() {
			if !a.LandingVisible {
				continue
			}
			assets = append(assets, a)
			if len(assets) >= 6 {
				break
			}
		}
	}
	data := map[string]any{
		"Version": h.version,
		"Title":   "AgentHub",
		"Assets":  assets,
	}
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
	return h.tpl.ExecuteTemplate(c.Response().Writer, "index", data)
}

// Catalog renders the browsable asset catalog.
func (h *Handler) Catalog(c echo.Context) error {
	q := strings.TrimSpace(c.QueryParam("q"))
	kindFilter := strings.TrimSpace(c.QueryParam("kind"))
	nsFilter := strings.TrimSpace(c.QueryParam("ns"))

	assets := []*catalog.Asset{}
	if h.CatalogStore != nil {
		for _, a := range h.CatalogStore.ListAssets() {
			if kindFilter != "" && assetKindSlug(a.Kind) != strings.ToLower(kindFilter) {
				continue
			}
			if nsFilter != "" && a.Namespace != nsFilter {
				continue
			}
			if q != "" {
				hay := strings.ToLower(a.DisplayName + " " + a.Summary + " " + a.Name + " " + strings.Join(a.Tags, " "))
				if !strings.Contains(hay, strings.ToLower(q)) {
					continue
				}
			}
			assets = append(assets, a)
		}
	}

	namespaces := []string{}
	seen := map[string]bool{}
	list := []*catalog.Asset{}
	if h.CatalogStore != nil {
		list = h.CatalogStore.ListAssets()
	}
	for _, a := range list {
		if !seen[a.Namespace] {
			seen[a.Namespace] = true
			namespaces = append(namespaces, a.Namespace)
		}
	}

	data := map[string]any{
		"Version":    h.version,
		"Title":      "Catalog",
		"Assets":     assets,
		"Query":      q,
		"KindFilter": kindFilter,
		"Namespaces": namespaces,
		"Kinds":      []string{"agent", "skill", "tool", "prompt", "pack"},
	}
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
	return h.tpl.ExecuteTemplate(c.Response().Writer, "catalog", data)
}

// Icon serves an icon blob by its digest.
func (h *Handler) Icon(c echo.Context) error {
	digest := c.Param("digest")
	if h.BlobStore == nil {
		return c.NoContent(http.StatusNotFound)
	}
	data, ok := h.BlobStore.Get(&commonv1.Digest{Algorithm: "sha256", Value: digest})
	if !ok {
		return c.NoContent(http.StatusNotFound)
	}
	c.Response().Header().Set(echo.HeaderContentType, http.DetectContentType(data))
	c.Response().Header().Set(echo.HeaderCacheControl, "public, max-age=86400, immutable")
	return c.Blob(http.StatusOK, http.DetectContentType(data), data)
}

// Namespace renders a namespace page.
func (h *Handler) Namespace(c echo.Context) error {
	slug := c.Param("slug")
	data := map[string]any{
		"Version": h.version,
		"Title":   "Namespace " + slug,
		"Slug":    slug,
	}
	if h.RegistryStore != nil {
		if ns, ok := h.RegistryStore.Get(slug); ok {
			data["Namespace"] = ns
		}
	}
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
	return h.tpl.ExecuteTemplate(c.Response().Writer, "namespace", data)
}

// Asset renders an asset page.
func (h *Handler) Asset(c echo.Context) error {
	ns := c.Param("ns")
	kindStr := c.Param("kind")
	name := c.Param("name")
	kind := parseAssetKind(kindStr)
	data := map[string]any{
		"Version":   h.version,
		"Title":     "Asset " + ns + "/" + name,
		"Namespace": ns,
		"Kind":      kindStr,
		"KindValue": kind,
		"Name":      name,
	}
	if h.CatalogStore != nil {
		if a, ok := h.CatalogStore.GetAsset(ns, kind, name); ok {
			data["Asset"] = a
		}
	}
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
	return h.tpl.ExecuteTemplate(c.Response().Writer, "asset", data)
}

// Version renders a version page.
func (h *Handler) Version(c echo.Context) error {
	ns := c.Param("ns")
	kindStr := c.Param("kind")
	name := c.Param("name")
	ver := c.Param("version")
	kind := parseAssetKind(kindStr)
	data := map[string]any{
		"Version":   h.version,
		"Title":     "Version " + ns + "/" + name + "@" + ver,
		"Namespace": ns,
		"Kind":      kindStr,
		"KindValue": kind,
		"Name":      name,
		"Ver":       ver,
	}
	if h.CatalogStore != nil {
		if v, ok := h.CatalogStore.GetVersion(ns, kind, name, ver); ok {
			data["VersionInfo"] = v
		}
	}
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
	return h.tpl.ExecuteTemplate(c.Response().Writer, "version", data)
}

func parseAssetKind(s string) commonv1.AssetKind {
	switch strings.ToLower(s) {
	case "agent":
		return commonv1.AssetKind_ASSET_KIND_AGENT
	case "skill":
		return commonv1.AssetKind_ASSET_KIND_SKILL
	case "tool":
		return commonv1.AssetKind_ASSET_KIND_TOOL
	case "prompt":
		return commonv1.AssetKind_ASSET_KIND_PROMPT
	case "pack":
		return commonv1.AssetKind_ASSET_KIND_PACK
	}
	return commonv1.AssetKind_ASSET_KIND_UNSPECIFIED
}

func assetKindSlug(k commonv1.AssetKind) string {
	switch k {
	case commonv1.AssetKind_ASSET_KIND_AGENT:
		return "agent"
	case commonv1.AssetKind_ASSET_KIND_SKILL:
		return "skill"
	case commonv1.AssetKind_ASSET_KIND_TOOL:
		return "tool"
	case commonv1.AssetKind_ASSET_KIND_PROMPT:
		return "prompt"
	case commonv1.AssetKind_ASSET_KIND_PACK:
		return "pack"
	}
	return "asset"
}

// StaticFS returns the embedded static asset filesystem.
func StaticFS() fs.FS {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

// PublishDraft handles the dashboard publish POST endpoint.
func (h *Handler) PublishDraft(c echo.Context) error {
	namespace := c.Param("namespace")
	kindStr := c.Param("kind")
	name := c.Param("name")

	// Map short kind names to full enum values
	kindMap := map[string]string{
		"agent":   "ASSET_KIND_AGENT",
		"skill":   "ASSET_KIND_SKILL",
		"tool":    "ASSET_KIND_TOOL",
		"prompt":  "ASSET_KIND_PROMPT",
		"pack":    "ASSET_KIND_PACK",
	}
	fullKind, ok := kindMap[kindStr]
	if !ok {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid kind")
	}

	// Validate kind is a known asset kind
	_, ok = commonv1.AssetKind_value[fullKind]
	if !ok {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid kind")
	}

	// For now, return a success response with a publish ID
	publishID := fmt.Sprintf("%s-%s-%s-%d", namespace, kindStr, name, time.Now().Unix())
	return c.JSON(http.StatusOK, map[string]string{
		"publish_id": publishID,
		"namespace":  namespace,
		"kind":       kindStr,
		"name":       name,
		"status":     "published",
	})
}
