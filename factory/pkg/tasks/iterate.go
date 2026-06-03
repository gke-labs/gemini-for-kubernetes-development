package tasks

import (
	"bytes"
	"fmt"
)

type IterateParams struct {
	Repo        Repo
	Instruction string
	Branch      string
	PRNumber    int
	Models      []string
}

func GetIterateScript() ([]byte, error) {
	return scriptsFS.ReadFile("iterate.sh")
}

func RenderIteratePrompt(params IterateParams) ([]byte, error) {
	if len(params.Models) == 0 {
		params.Models = []string{"gemini-3.5-flash", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-2.5-pro"}
	}

	promptTmpl, err := getPromptTemplate("iterate.txt")
	if err != nil {
		return nil, fmt.Errorf("getting prompt template: %w", err)
	}
	var pBuf bytes.Buffer
	if err := promptTmpl.Execute(&pBuf, params); err != nil {
		return nil, fmt.Errorf("executing prompt template: %w", err)
	}

	return pBuf.Bytes(), nil
}
