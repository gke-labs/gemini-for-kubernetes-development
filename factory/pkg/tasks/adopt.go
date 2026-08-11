package tasks

import (
	"bytes"
	"fmt"
)

type AdoptParams struct {
	RepoOwner string
	RepoName  string
	CloneURL  string
	PRNumber  int
	PRURL     string
	AdoptFlag string // "open" or "close"
	Strategy  string // "reuse" or "reimplement"
	Title     string
	Body      string
	Diff      string
	Models    []string
}

func GetAdoptScript() ([]byte, error) {
	return scriptsFS.ReadFile("adopt.sh")
}

func RenderAdoptPrompt(params AdoptParams) ([]byte, error) {
	if len(params.Models) == 0 {
		params.Models = DefaultModels
	}

	promptTmpl, err := getPromptTemplate("adopt.txt")
	if err != nil {
		return nil, fmt.Errorf("getting prompt template: %w", err)
	}
	var pBuf bytes.Buffer
	if err := promptTmpl.Execute(&pBuf, params); err != nil {
		return nil, fmt.Errorf("executing prompt template: %w", err)
	}

	return pBuf.Bytes(), nil
}
