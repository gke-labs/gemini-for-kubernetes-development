package taskapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/spool"
)

// Client calls a task server at a base URL, such as a port-forward's.
type Client struct {
	base string
	http *http.Client
}

func NewClient(base string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{base: strings.TrimSuffix(base, "/"), http: hc}
}

// StatusError is a server's answer other than success.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("task server: %d %s", e.Code, e.Message)
}

func (c *Client) Version(ctx context.Context) (int, error) {
	var v Version
	if err := c.do(ctx, http.MethodGet, "/v1/version", nil, &v); err != nil {
		return 0, err
	}
	return v.API, nil
}

func (c *Client) List(ctx context.Context) ([]spool.Entry, error) {
	var entries []spool.Entry
	err := c.do(ctx, http.MethodGet, "/v1/tasks", nil, &entries)
	return entries, err
}

func (c *Client) Post(ctx context.Context, req PostRequest) (PostResponse, error) {
	var resp PostResponse
	err := c.do(ctx, http.MethodPost, "/v1/tasks", req, &resp)
	return resp, err
}

func (c *Client) Get(ctx context.Context, id string) (spool.Entry, error) {
	var e spool.Entry
	err := c.do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(id), nil, &e)
	return e, err
}

func (c *Client) Cancel(ctx context.Context, id string, kill bool) (spool.Entry, error) {
	var e spool.Entry
	err := c.do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(id)+"/cancel", CancelRequest{Kill: kill}, &e)
	return e, err
}

func (c *Client) Files(ctx context.Context, id string) ([]string, error) {
	var names []string
	err := c.do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(id)+"/files", nil, &names)
	return names, err
}

// ReadFile is a file in the task's directory, os.ErrNotExist when it has
// none of that name.
func (c *Client) ReadFile(ctx context.Context, id, name string) ([]byte, error) {
	resp, err := c.send(ctx, http.MethodGet, filePath(id, name), nil, "")
	if err != nil {
		return nil, notExist(err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (c *Client) WriteFile(ctx context.Context, id, name string, data []byte) error {
	resp, err := c.send(ctx, http.MethodPut, filePath(id, name), bytes.NewReader(data), "application/octet-stream")
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// Log is the task's log from offset; following, it goes on until the task
// ends. The caller closes it.
func (c *Client) Log(ctx context.Context, id string, offset int64, follow bool) (io.ReadCloser, error) {
	q := url.Values{"offset": {strconv.FormatInt(offset, 10)}, "follow": {strconv.FormatBool(follow)}}
	resp, err := c.send(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(id)+"/log?"+q.Encode(), nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func filePath(id, name string) string {
	return "/v1/tasks/" + url.PathEscape(id) + "/files/" + url.PathEscape(name)
}

func notExist(err error) error {
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return fmt.Errorf("%w: %s", os.ErrNotExist, se.Message)
	}
	return err
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, contentType = bytes.NewReader(data), "application/json"
	}
	resp, err := c.send(ctx, method, path, body, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("task server: decoding %s %s: %w", method, path, err)
	}
	return nil
}

// send makes the request and fails unless it succeeded; on success the
// caller closes the body.
func (c *Client) send(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		var eb errorBody
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(data, &eb) != nil || eb.Error == "" {
			eb.Error = strings.TrimSpace(string(data))
		}
		return nil, &StatusError{Code: resp.StatusCode, Message: eb.Error}
	}
	return resp, nil
}
