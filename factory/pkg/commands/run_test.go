package commands

import "testing"

func TestParseTargetPR(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"", 0, true},
		{"42", 42, true},
		{"#42", 42, true},
		{"https://github.com/GoogleCloudPlatform/k8s-config-connector/pull/42", 42, true},
		{"https://github.com/googlecloudplatform/k8s-config-connector/pull/42/", 42, true},
		// A number means nothing in another repository's checkout.
		{"https://github.com/someone/else/pull/42", 0, false},
		{"https://github.com/GoogleCloudPlatform/k8s-config-connector/issues/42", 0, false},
		{"https://github.com/GoogleCloudPlatform/k8s-config-connector/pull/42/files", 0, false},
		{"0", 0, false},
		{"-3", 0, false},
		{"main", 0, false},
		{"42; rm -rf /", 0, false},
	} {
		got, err := parseTargetPR(tc.in, "GoogleCloudPlatform", "k8s-config-connector")
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("parseTargetPR(%q) = %d, %v; want %d, ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}
