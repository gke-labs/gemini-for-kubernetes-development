package tasks

import (
	"embed"
	_ "embed"
	"text/template"
)

//go:embed *.sh
var scriptsFS embed.FS

//go:embed *.txt
var promptFS embed.FS

var templates = make(map[string]*template.Template)

func getPromptTemplate(name string) (*template.Template, error) {
	if t, ok := templates[name]; ok {
		return t, nil
	}

	data, err := promptFS.ReadFile(name)
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
