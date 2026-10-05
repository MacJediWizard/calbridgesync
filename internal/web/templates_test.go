package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/macjediwizard/calbridgesync/internal/auth"
	"github.com/macjediwizard/calbridgesync/internal/config"
)

func TestLoadTemplates(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	if templates == nil {
		t.Fatal("LoadTemplates() returned nil")
	}

	if _, ok := templates.templates["error.html"]; !ok {
		t.Error(`template "error.html" not found`)
	}
	if len(templates.templates) != 1 {
		t.Errorf("expected only error.html to be loaded, got %d templates", len(templates.templates))
	}
}

func TestRenderErrorTemplate(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	t.Run("shows the error passed by the handlers", func(t *testing.T) {
		buf, err := templates.RenderTemplate("error.html", gin.H{"error": "Invalid state parameter"})
		if err != nil {
			t.Fatalf("RenderTemplate() error = %v", err)
		}
		html := buf.String()

		if !strings.Contains(html, "<!DOCTYPE html>") {
			t.Error("expected a standalone HTML document with a doctype")
		}
		if !strings.Contains(html, "Invalid state parameter") {
			t.Errorf("expected the error text in output, got:\n%s", html)
		}
	})

	t.Run("loads no external assets", func(t *testing.T) {
		buf, err := templates.RenderTemplate("error.html", gin.H{"error": "x"})
		if err != nil {
			t.Fatalf("RenderTemplate() error = %v", err)
		}
		if strings.Contains(buf.String(), "http") {
			t.Errorf("expected no http(s) URLs in the error page, got:\n%s", buf.String())
		}
		if strings.Contains(buf.String(), "<script") {
			t.Error("expected no scripts in the error page")
		}
	})

	t.Run("escapes the error text", func(t *testing.T) {
		buf, err := templates.RenderTemplate("error.html", gin.H{"error": "<script>alert(1)</script>"})
		if err != nil {
			t.Fatalf("RenderTemplate() error = %v", err)
		}
		if strings.Contains(buf.String(), "<script>") {
			t.Error("expected the error text to be HTML-escaped")
		}
	})

	t.Run("falls back to a generic message", func(t *testing.T) {
		buf, err := templates.RenderTemplate("error.html", gin.H{})
		if err != nil {
			t.Fatalf("RenderTemplate() error = %v", err)
		}
		if !strings.Contains(buf.String(), "An unexpected error occurred.") {
			t.Error("expected the generic message when no error is passed")
		}
	})
}

// TestCallbackErrorPageShowsError drives the live OIDC callback through gin
// with the real renderer: a callback without a matching state cookie must
// render the error page with the handler's message.
func TestCallbackErrorPageShowsError(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	sm := auth.NewSessionManager("0123456789abcdef0123456789abcdef", false, 3600, 300)
	h := NewHandlers(&config.Config{}, nil, nil, sm, nil, nil, nil, nil, nil)

	r := gin.New()
	r.HTMLRender = templates
	r.GET("/auth/callback", h.Callback)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=bogus&code=x", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Invalid state parameter") {
		t.Errorf("expected the handler's error in the page, got:\n%s", w.Body.String())
	}
}

func TestRenderTemplateNotFound(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	_, err = templates.RenderTemplate("nonexistent.html", nil)
	if err == nil {
		t.Fatal("Expected error for nonexistent template")
	}

	if !strings.Contains(err.Error(), "template not found") {
		t.Errorf("Expected 'template not found' error, got: %v", err)
	}
}

func TestHTMLTemplatesInstance(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	t.Run("returns render for existing template", func(t *testing.T) {
		render := templates.Instance("error.html", gin.H{"error": "Test"})
		if render == nil {
			t.Error("Expected render instance")
		}
	})

	t.Run("returns error render for missing template", func(t *testing.T) {
		render := templates.Instance("nonexistent.html", nil)
		if render == nil {
			t.Error("Expected render instance for error case")
		}
	})
}

func TestTemplateRenderWriteContentType(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	render := templates.Instance("error.html", gin.H{"error": "test"})

	// Create a mock response writer
	w := &mockResponseWriter{header: make(map[string][]string)}

	err = render.Render(w)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	contentType := w.header.Get("Content-Type")
	if contentType != "text/html; charset=utf-8" {
		t.Errorf("Expected Content-Type 'text/html; charset=utf-8', got %q", contentType)
	}
}

func TestTemplateRenderWithExistingContentType(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	render := templates.Instance("error.html", gin.H{"error": "test"})

	// Create a mock response writer with existing Content-Type
	w := &mockResponseWriter{header: make(map[string][]string)}
	w.header.Set("Content-Type", "application/json")

	err = render.Render(w)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	// Should preserve existing Content-Type
	contentType := w.header.Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected existing Content-Type to be preserved, got %q", contentType)
	}
}

// mockResponseWriter is a simple mock for http.ResponseWriter
type mockResponseWriter struct {
	header     http.Header
	body       strings.Builder
	statusCode int
}

func (m *mockResponseWriter) Header() http.Header {
	return m.header
}

func (m *mockResponseWriter) Write(b []byte) (int, error) {
	return m.body.Write(b)
}

func (m *mockResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}
