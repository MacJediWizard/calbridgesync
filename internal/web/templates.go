package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin/render"
)

// templatesFS holds the server-rendered pages. The UI is the React SPA in
// web/; the only server-rendered page left is error.html, which the OIDC
// login, callback and logout handlers render when sign-in fails.
//
//go:embed templates/error.html
var templatesFS embed.FS

// HTMLTemplates implements gin's render.HTMLRender interface.
type HTMLTemplates struct {
	templates map[string]*template.Template
	mu        sync.RWMutex
}

// Instance returns a render.Render implementation for a specific template.
func (h *HTMLTemplates) Instance(name string, data interface{}) render.Render {
	h.mu.RLock()
	tmpl, ok := h.templates[name]
	h.mu.RUnlock()

	if !ok {
		// Return a simple error template
		return &templateRender{
			Template: template.Must(template.New("error").Parse("<html><body>Template not found: {{.}}</body></html>")),
			Data:     name,
		}
	}

	return &templateRender{
		Template: tmpl,
		Data:     data,
	}
}

type templateRender struct {
	Template *template.Template
	Data     interface{}
}

func (t *templateRender) Render(w http.ResponseWriter) error {
	t.WriteContentType(w)
	return t.Template.Execute(w, t.Data)
}

func (t *templateRender) WriteContentType(w http.ResponseWriter) {
	header := w.Header()
	if val := header["Content-Type"]; len(val) == 0 {
		header["Content-Type"] = []string{"text/html; charset=utf-8"}
	}
}

// LoadTemplates parses the embedded error.html page. It is a standalone
// document with no layout and no external assets; handlers pass the
// message as gin.H{"error": ...}.
func LoadTemplates() (*HTMLTemplates, error) {
	const name = "error.html"
	tmpl, err := template.ParseFS(templatesFS, "templates/"+name)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", name, err)
	}

	return &HTMLTemplates{
		templates: map[string]*template.Template{name: tmpl},
	}, nil
}

// RenderTemplate renders a template to a bytes.Buffer (useful for testing).
func (h *HTMLTemplates) RenderTemplate(name string, data interface{}) (*bytes.Buffer, error) {
	h.mu.RLock()
	tmpl, ok := h.templates[name]
	h.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("template not found: %s", name)
	}

	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, data); err != nil {
		return nil, err
	}

	return buf, nil
}
