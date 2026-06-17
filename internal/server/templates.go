// Package server implements the HTTP server, route registration, handlers
// and bundled frontend (templates + static assets) for the splat
// application.
package server

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// templateFuncs is the set of helper funcs exposed to all templates.
var templateFuncs = template.FuncMap{
	// pathescape escapes a path segment so it is safe to drop into a URL.
	// Slashes inside the key are preserved; per-segment escaping ensures
	// reserved characters in filenames (spaces, "#", etc.) don't break the
	// htmx-targeted URL.
	"pathescape": func(s string) string {
		return pathEscape(s)
	},
}

// pathEscape escapes each "/"-separated segment of s with url.PathEscape
// and rejoins with "/". It matches the encoding the server's path-routing
// produces for {key...} wildcards.
func pathEscape(s string) string {
	parts := splitPath(s)
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return joinPath(parts)
}

// splitPath splits a key on "/" without using strings.Split (to keep the
// helper allocation-light for the common case of a flat key).
func splitPath(s string) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// joinPath joins parts with "/".
func joinPath(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	n := len(parts) - 1
	for _, p := range parts {
		n += len(p)
	}
	buf := make([]byte, 0, n)
	for i, p := range parts {
		if i > 0 {
			buf = append(buf, '/')
		}
		buf = append(buf, p...)
	}
	return string(buf)
}

// loadTemplates parses every *.html file under the embedded templates/ dir.
func loadTemplates() (*template.Template, error) {
	root := template.New("").Funcs(templateFuncs)
	sub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		return nil, fmt.Errorf("server: sub templates fs: %w", err)
	}
	parsed, err := root.ParseFS(sub, "*.html")
	if err != nil {
		return nil, fmt.Errorf("server: parse templates: %w", err)
	}
	return parsed, nil
}

// staticSubFS returns the embedded static/ subtree as an fs.FS rooted at
// the directory itself (so paths look like "app.js" rather than
// "static/app.js").
func staticSubFS() (fs.FS, error) {
	return fs.Sub(staticFS, "static")
}
