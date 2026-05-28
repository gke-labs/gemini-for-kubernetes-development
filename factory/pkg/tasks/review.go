package tasks

import (
	"bytes"
	"fmt"
)

type ReviewParams struct {
	PullRequest  PullRequest
	Instructions []string
	Models       []string
}

func GetReviewScript() ([]byte, error) {
	return scriptsFS.ReadFile("review.sh")
}

func RenderReviewPrompt(params ReviewParams) ([]byte, error) {
	if len(params.Models) == 0 {
		params.Models = []string{"gemini-3.5-flash", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-2.5-pro"}
	}

	promptTmpl, err := getPromptTemplate("review.txt")
	if err != nil {
		return nil, fmt.Errorf("getting prompt template: %w", err)
	}

	var pBuf bytes.Buffer
	if err := promptTmpl.Execute(&pBuf, params); err != nil {
		return nil, fmt.Errorf("executing prompt template: %w", err)
	}

	return pBuf.Bytes(), nil
}
