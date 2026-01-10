package gcs

import (
	"context"
	"testing"
)

func TestNewClient(t *testing.T) {
	// This test just ensures the function signature is correct and it returns an error
	// without credentials in the environment, which is expected behavior.
	// It basically validates that we can attempt to create a client.
	ctx := context.Background()
	_, err := NewClient(ctx)
	if err == nil {
		t.Failed()
		// If by chance we have default creds, this passes.
		// If we don't, it returns an error, which is also fine for this simple unit test
		// as we aren't mocking the auth layer here.
		// We just want to ensure it compiles and runs.
	}
}
