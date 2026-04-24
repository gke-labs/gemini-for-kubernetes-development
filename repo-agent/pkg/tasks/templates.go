/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tasks

import (
	"embed"
	_ "embed"
	"text/template"
)

//go:embed *.sh
var scriptsFS embed.FS

func getScriptTemplate(name string) (*template.Template, error) {
	if t, ok := templates[name]; ok {
		return t, nil
	}

	data, err := scriptsFS.ReadFile(name)
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
