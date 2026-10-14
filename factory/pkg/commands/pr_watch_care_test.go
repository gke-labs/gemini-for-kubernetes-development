package commands

import (
	"testing"
)

// care's replies and reports are not comments for care to address.
func TestFactoryPosted(t *testing.T) {
	if !factoryPosted("Done.\n\n<!-- factory:task-output kind=Change task=fix-2 reply=1 -->") || factoryPosted("please fix") {
		t.Error("factoryPosted")
	}
}
