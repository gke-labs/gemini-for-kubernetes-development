package github

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadOnlyRefusesWrites(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	c := &http.Client{Transport: readOnly{next: http.DefaultTransport}}
	for _, tc := range []struct {
		method, path, body string
		ok                 bool
	}{
		{http.MethodGet, "/repos/o/r/issues/1", "", true},
		{http.MethodPost, "/graphql", `{"query":"query { viewer { login } }"}`, true},
		{http.MethodPost, "/graphql", `{"query":"mutation { addLabelsToLabelable }"}`, false},
		{http.MethodPost, "/repos/o/r/issues/1/labels", `["x"]`, false},
		{http.MethodPatch, "/repos/o/r/issues/1", `{}`, false},
		{http.MethodDelete, "/repos/o/r/issues/1/labels/x", "", false},
	} {
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, io.NopCloser(strings.NewReader(tc.body)))
		before := hits
		resp, err := c.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		if sent := hits > before; sent != tc.ok || (err == nil) != tc.ok {
			t.Errorf("%s %s: sent %v, err %v; want sent %v", tc.method, tc.path, sent, err, tc.ok)
		}
	}
}
