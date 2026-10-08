package fanout

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// maxItemsURLBytes is the most an items URL may return.
const maxItemsURLBytes = 5 << 20

// isItemsURL says items.from names a URL, not a file in the repository.
func isItemsURL(from string) bool {
	return strings.Contains(from, "://")
}

// checkItemsURL is the spec's rule for an items URL: https, with a host.
func checkItemsURL(from string) error {
	if !strings.HasPrefix(from, "https://") {
		return fmt.Errorf("items.from: %s: only https URLs are read", from)
	}
	if strings.TrimPrefix(from, "https://") == "" {
		return fmt.Errorf("items.from: %s: no host", from)
	}
	return nil
}

// errUnreadableURL is a URL whose answer is its own fault (a 4xx, too
// large, not JSON, a host that is not public): retrying does not help.
var errUnreadableURL = errors.New("unreadable URL")

// fetchItemsURL returns what an items URL serves and the first bytes of its
// SHA-256, which stand in for a commit: the progress comment shows them, so
// a change between passes is visible.
func fetchItemsURL(ctx context.Context, client *http.Client, url string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %v: %w", url, err, errUnreadableURL)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errNotPublic) || errors.Is(err, errUnreadableURL) {
			return nil, "", fmt.Errorf("%w: %w", err, errUnreadableURL)
		}
		return nil, "", fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return nil, "", fmt.Errorf("fetching %s: %s", url, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, "", fmt.Errorf("%s: %s: %w", url, resp.Status, errUnreadableURL)
	case strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html"):
		return nil, "", fmt.Errorf("%s is a web page, not JSON: link the raw file: %w", url, errUnreadableURL)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxItemsURLBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", url, err)
	}
	if len(data) > maxItemsURLBytes {
		return nil, "", fmt.Errorf("%s: more than %d MB: %w", url, maxItemsURLBytes>>20, errUnreadableURL)
	}
	return data, fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// errNotPublic is a connection refused because it was not to a public
// address.
var errNotPublic = errors.New("not a public address")

// notPublic are the addresses an items URL may not reach besides the
// loopback, private, link-local (the metadata server) and multicast ones:
// shared address space, which clusters use for pods and services too.
var notPublic = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10")}

// publicAddress refuses a connection to anything but a public address. It
// checks the address dialled, after DNS, so no name or redirect gets
// round it.
func publicAddress(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	ip := ap.Addr().Unmap()
	bad := ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
	for _, p := range notPublic {
		bad = bad || p.Contains(ip)
	}
	if bad {
		return fmt.Errorf("%s: %w", ip, errNotPublic)
	}
	return nil
}

// itemsURLClient reads items URLs: public addresses only, no proxy (it
// would be the address checked), and https all the way through redirects.
var itemsURLClient = &http.Client{
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, Control: publicAddress}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects: %w", errUnreadableURL)
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("redirected to %s: only https URLs are read: %w", req.URL.Redacted(), errUnreadableURL)
		}
		return nil
	},
}
