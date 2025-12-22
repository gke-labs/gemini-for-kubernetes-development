package llm

import (
	"bytes"
	"log"
	"strings"
)

const (
	dummyReview = `note: |
  This is a dummy test review
review:
  body: |
    Nice work overall! Here are a few suggestions to improve the code
  comments:
    - path: nofile.go
      line: 279
      body: Rather than hardcoding these suffixes, consider making them configurable
      side: RIGHT
    - path: sdk/somefile.py
      line: 42
      body: Consider using a more efficient algorithm here
      side: RIGHT
`

	triageOutput = `/kind feature
This issue is a feature request to add AGI to the project.
`
)

var _ Provider = &Dummy{}

type Dummy struct {
	processors []PostProcessor
}

func (d *Dummy) Setup(_, _ string) error {
	log.Println("Dummy provider setup")
	return nil
}

func (d *Dummy) Cleanup(_ string) error {
	log.Println("Dummy provider cleanup")
	return nil
}

func (d *Dummy) ExpandPrompt(prompt string) (string, error) {
	return prompt, nil
}

func (d *Dummy) response(prompt string) []byte {
	// if prompt contains "class DraftReviewComment(BaseModel)", return a different response
	if strings.Contains(prompt, "class DraftReviewComment(BaseModel)") {
		return []byte(dummyReview)
	}
	if bytes.Contains([]byte(prompt), []byte(`Start the response with "/kind <Category>" where`)) {
		return []byte(triageOutput)
	}
	return []byte("Response from Dummy LLM. This is a test")
}

func (d *Dummy) Run(prompt string) ([]byte, error) {
	var err error
	log.Printf("Dummy provider running with prompt")
	output := d.response(prompt)
	for _, p := range d.processors {
		output, err = p(output)
		if err != nil {
			return nil, err
		}
	}
	return output, nil
}

func (d *Dummy) AddPostProcessor(p PostProcessor) {
	d.processors = append(d.processors, p)
}
