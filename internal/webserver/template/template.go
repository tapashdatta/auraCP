// Package template renders a per-site nginx vhost by running an ordered
// chain of single-purpose Processors against an embedded .tmpl file.
//
// The shape:
//   1. Load body — `body, err := Load("php")` reads templates/php.tmpl
//      from the embedded FS.
//   2. Pick processors — one of `For(siteType)` returns the right
//      Template subtype (PhpTemplate, NodejsTemplate, …), each carrying
//      its own processor list.
//   3. Build — calls every processor in order, then strips any
//      leftover `{{xxx}}` placeholders so they don't leak into nginx.
//   4. Render — returns the substituted body.
//
// Each Template subtype is just a slice of processor.Funcs. The actual
// substitution loop lives in the base type's Render method — composition
// over inheritance, Go-idiomatic.
package template

import (
	"embed"
	"fmt"
	"regexp"
	"strings"

	"github.com/auracp/auracp/internal/webserver/processor"
)

//go:embed templates/*.tmpl
var bundled embed.FS

// Load returns the raw template body for a site type.
// Type maps to filename: "php" → templates/php.tmpl, "wordpress" →
// templates/wordpress.tmpl, etc.
func Load(siteType string) (string, error) {
	name := "templates/" + siteType + ".tmpl"
	b, err := bundled.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("vhost template %q not bundled: %w", siteType, err)
	}
	return string(b), nil
}

// Template is the shared render machinery. Each per-type subtype is just
// a Template with a pre-filled .Processors slice.
type Template struct {
	Type       string
	Processors []processor.Func
}

// emptyPlaceholder matches any leftover `{{anything}}` so RemoveEmpty
// can strip them. We deliberately do this after processor chain runs
// so a typo in the template body fails loud (the unsubstituted token
// shows up in `nginx -t` and the operator sees it) rather than silently
// embedding `{{phpversion}}` into the live vhost.
var emptyPlaceholder = regexp.MustCompile(`\{\{[a-z_]+\}\}`)

// Render runs every Processor against the body, strips any unmatched
// `{{xxx}}` placeholders, and returns the result.
func (t *Template) Render(body string, ctx *processor.SiteContext) string {
	for _, p := range t.Processors {
		body = p(body, ctx)
	}
	// Trim leftover unsubstituted placeholders (e.g. {{app_port}} in a
	// PHP template). Leaves the surrounding whitespace alone — the
	// final string-clean pass removes consecutive blank lines.
	body = emptyPlaceholder.ReplaceAllString(body, "")
	// Collapse runs of >=3 blank lines (artifacts of stripped placeholders)
	// down to one blank line. Keeps the rendered config readable when an
	// operator `cat`s it.
	body = collapseBlanks(body)
	return body
}

// collapseBlanks runs after substitution to keep the output readable.
// Three or more consecutive newlines → exactly two (one blank line).
func collapseBlanks(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// For returns the Template wired with the right processor chain for a
// given site type. Unknown type → error (so a bad type from the DB
// doesn't silently render a broken vhost).
func For(siteType string) (*Template, error) {
	switch siteType {
	case "php":
		return Php(), nil
	case "wordpress":
		return WordPress(), nil
	case "nodejs":
		return Nodejs(), nil
	case "python":
		return Python(), nil
	case "static":
		return Static(), nil
	case "reverseproxy":
		return ReverseProxy(), nil
	}
	return nil, fmt.Errorf("unknown site type: %q", siteType)
}
