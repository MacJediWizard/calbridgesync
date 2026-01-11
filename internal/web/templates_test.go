package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestLoadTemplates(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	if templates == nil {
		t.Fatal("LoadTemplates() returned nil")
	}

	// Check that expected templates are loaded
	expectedTemplates := []string{
		"login.html",
		"dashboard.html",
		"error.html",
		"logs.html",
		"sources/list.html",
		"sources/add.html",
		"sources/edit.html",
		"partials/error.html",
		"partials/sync_triggered.html",
	}

	for _, name := range expectedTemplates {
		if _, ok := templates.templates[name]; !ok {
			t.Errorf("Template %q not found", name)
		}
	}
}

func TestRenderLoginTemplate(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	data := map[string]interface{}{
		"Title": "Test Login",
	}

	buf, err := templates.RenderTemplate("login.html", data)
	if err != nil {
		t.Fatalf("RenderTemplate() error = %v", err)
	}

	html := buf.String()

	// Check that the layout is included
	if !strings.Contains(html, "<!DOCTYPE html>") {
		t.Error("Expected HTML doctype in output")
	}

	// Check that the content is included
	if !strings.Contains(html, "CalBridgeSync") {
		t.Error("Expected 'CalBridgeSync' in output")
	}

	if !strings.Contains(html, "Sign in with SSO") {
		t.Error("Expected 'Sign in with SSO' button in output")
	}

	// Check that Tailwind CSS is loaded
	if !strings.Contains(html, "tailwindcss.com") {
		t.Error("Expected Tailwind CSS script in output")
	}
}

func TestRenderPartialTemplate(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	data := map[string]interface{}{
		"error": "Test error message",
	}

	buf, err := templates.RenderTemplate("partials/error.html", data)
	if err != nil {
		t.Fatalf("RenderTemplate() error = %v", err)
	}

	html := buf.String()

	// Partials should NOT include the layout
	if strings.Contains(html, "<!DOCTYPE html>") {
		t.Error("Partial should not include DOCTYPE")
	}

	// But should include the error message
	if !strings.Contains(html, "Test error message") {
		t.Error("Expected error message in output")
	}
}

func TestRenderTemplateNotFound(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	_, err = templates.RenderTemplate("nonexistent.html", nil)
	if err == nil {
		t.Error("Expected error for nonexistent template")
	}

	if !strings.Contains(err.Error(), "template not found") {
		t.Errorf("Expected 'template not found' error, got: %v", err)
	}
}

func TestTemplateFuncs(t *testing.T) {
	funcs := TemplateFuncs()

	t.Run("divide function", func(t *testing.T) {
		divide := funcs["divide"].(func(a, b int) int)

		if divide(10, 2) != 5 {
			t.Error("divide(10, 2) should equal 5")
		}
		if divide(7, 3) != 2 {
			t.Error("divide(7, 3) should equal 2")
		}
		if divide(10, 0) != 0 {
			t.Error("divide(10, 0) should return 0 for division by zero")
		}
	})

	t.Run("plus function", func(t *testing.T) {
		plus := funcs["plus"].(func(a, b int) int)

		if plus(5, 3) != 8 {
			t.Error("plus(5, 3) should equal 8")
		}
		if plus(-5, 10) != 5 {
			t.Error("plus(-5, 10) should equal 5")
		}
	})

	t.Run("minus function", func(t *testing.T) {
		minus := funcs["minus"].(func(a, b int) int)

		if minus(10, 3) != 7 {
			t.Error("minus(10, 3) should equal 7")
		}
		if minus(5, 10) != -5 {
			t.Error("minus(5, 10) should equal -5")
		}
	})

	t.Run("multiply function", func(t *testing.T) {
		multiply := funcs["multiply"].(func(a, b int) int)

		if multiply(5, 3) != 15 {
			t.Error("multiply(5, 3) should equal 15")
		}
		if multiply(-2, 4) != -8 {
			t.Error("multiply(-2, 4) should equal -8")
		}
	})
}

func TestHTMLTemplatesInstance(t *testing.T) {
	templates, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	t.Run("returns render for existing template", func(t *testing.T) {
		render := templates.Instance("login.html", map[string]interface{}{"title": "Test"})
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

	render := templates.Instance("partials/error.html", map[string]interface{}{"error": "test"})

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

	render := templates.Instance("partials/error.html", map[string]interface{}{"error": "test"})

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
