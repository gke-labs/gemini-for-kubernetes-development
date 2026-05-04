package prompts

import (
	"embed"
	_ "embed"
	"text/template"
)

//go:embed *.txt
var templatesFS embed.FS

var templates = make(map[string]*template.Template)

func getTemplate(name string) (*template.Template, error) {
	if t, ok := templates[name]; ok {
		return t, nil
	}

	data, err := templatesFS.ReadFile(name)
	if err != nil {
		return nil, err
	}

	t, err := template.New(name).Parse(string(data))
	if err != nil {
		return nil, err
	}

	templates[name] = t
	return t, nil
}
