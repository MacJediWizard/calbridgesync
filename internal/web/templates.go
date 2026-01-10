package web

import (
	"embed"
	"html/template"
	"io/fs"
	"path/filepath"
)

//go:embed templates/*.html templates/sources/*.html templates/partials/*.html
var templatesFS embed.FS

// TemplateFuncs returns custom template functions.
func TemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"divide": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"plus": func(a, b int) int {
			return a + b
		},
		"minus": func(a, b int) int {
			return a - b
		},
		"multiply": func(a, b int) int {
			return a * b
		},
	}
}

// LoadTemplates loads all templates from the embedded filesystem.
func LoadTemplates() (*template.Template, error) {
	tmpl := template.New("").Funcs(TemplateFuncs())

	// Walk through embedded templates
	err := fs.WalkDir(templatesFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		if filepath.Ext(path) != ".html" {
			return nil
		}

		content, err := templatesFS.ReadFile(path)
		if err != nil {
			return err
		}

		name := path[len("templates/"):]
		_, err = tmpl.New(name).Parse(string(content))
		return err
	})

	if err != nil {
		return nil, err
	}

	return tmpl, nil
}
