// Package podacpd finds the way to a sandbox pod's acpd.
//
// A sandbox whose factory daemon hosts agent sessions serves them on its
// loopback task server (127.0.0.1:49990, /v1/sessions), reached through a
// port-forward: the API server authenticates the caller and checks
// pods/portforward, so nothing in the pod listens unauthenticated. Older
// images only have acpd on the pod IP's :49984, which stays the fallback.
package podacpd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
)

// TaskServerPort is the factory daemon's task server, on the pod's
// loopback only. Defined here rather than imported: repo-agent does not
// depend on factory.
const TaskServerPort = 49990

const (
	// probeTimeout bounds opening a forward and asking it for the
	// version, so a caller with a long context is not held up by a pod
	// that will not answer.
	probeTimeout = 10 * time.Second
	// retryAfter is how long a pod that could not be forwarded to is
	// dialled on its IP before the forward is tried again: missing RBAC
	// or a flaky API server should not cost every ten-second poll a
	// failed forward.
	retryAfter = time.Minute
	// idleAfter closes a forward nobody has used for this long.
	idleAfter = 15 * time.Minute
)

// Dialer hands out acpd clients for sandbox pods, keeping one forward per
// pod. It is safe for concurrent use.
type Dialer struct {
	// open starts a forward to the pod's task server and returns its base
	// URL. A seam for tests.
	open func(ctx context.Context, pod *corev1.Pod) (*forward, error)
	// byIP is the fallback, the pod IP's acpd. A seam for tests.
	byIP func(ip string) *acpd.Client

	mu    sync.Mutex
	pods  map[types.UID]*podState
	clock func() time.Time
}

type podState struct {
	mu  sync.Mutex
	fwd *forward
	// legacy says the pod's daemon hosts no sessions: an image does not
	// change under a pod, so it is asked once.
	legacy bool
	// failedAt is when a forward last could not be opened.
	failedAt time.Time
	// usedAt is guarded by Dialer.mu, the rest by mu.
	usedAt time.Time
}

// New returns a Dialer that port-forwards with config. A nil config, or
// one that cannot build a clientset, gives a Dialer that only dials pod
// IPs, which is what every caller did before.
func New(config *rest.Config) *Dialer {
	d := &Dialer{
		byIP:  func(ip string) *acpd.Client { return acpd.NewForPodIP(ip) },
		pods:  map[types.UID]*podState{},
		clock: time.Now,
	}
	if config != nil {
		if cs, err := kubernetes.NewForConfig(config); err == nil {
			d.open = func(ctx context.Context, pod *corev1.Pod) (*forward, error) {
				return openForward(ctx, config, cs, pod)
			}
		}
	}
	return d
}

// Client is the acpd client for pod: over its forward when the pod's
// daemon hosts sessions, else on the pod IP. It never fails; a pod that
// cannot be reached either way fails on the client's first call, as it
// always did.
func (d *Dialer) Client(ctx context.Context, pod *corev1.Pod) *acpd.Client {
	if d == nil {
		return acpd.NewForPodIP(pod.Status.PodIP)
	}
	if base, ok := d.forwarded(ctx, pod); ok {
		return acpd.New(base + "/v1")
	}
	return d.byIP(pod.Status.PodIP)
}

// forwarded is the base URL of a live forward to a pod that hosts
// sessions, opening and probing one when there is none.
func (d *Dialer) forwarded(ctx context.Context, pod *corev1.Pod) (string, bool) {
	if d.open == nil {
		return "", false
	}
	d.mu.Lock()
	now := d.clock()
	d.sweep(now)
	st := d.pods[pod.UID]
	if st == nil {
		st = &podState{}
		d.pods[pod.UID] = st
	}
	st.usedAt = now
	d.mu.Unlock()

	// Per pod: concurrent callers for one pod wait for a single forward
	// rather than each opening their own, and a slow pod holds up no
	// other.
	st.mu.Lock()
	defer st.mu.Unlock()
	switch {
	case st.legacy:
		return "", false
	case st.fwd != nil && st.fwd.alive():
		return st.fwd.base, true
	case !st.failedAt.IsZero() && now.Sub(st.failedAt) < retryAfter:
		return "", false
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	fwd, err := d.open(ctx, pod)
	if err != nil {
		st.failedAt = now
		return "", false
	}
	sessions, err := probe(ctx, fwd.base)
	switch {
	case err != nil:
		fwd.close()
		st.failedAt = now
		return "", false
	case !sessions:
		fwd.close()
		st.legacy = true
		return "", false
	}
	st.fwd, st.failedAt = fwd, time.Time{}
	return fwd.base, true
}

// sweep forgets pods unused for idleAfter, closing their forwards. A pod
// that was deleted is never asked about again, so this is also how its
// state goes.
func (d *Dialer) sweep(now time.Time) {
	for uid, st := range d.pods {
		if now.Sub(st.usedAt) > idleAfter {
			go st.closeForward()
			delete(d.pods, uid)
		}
	}
}

// Close ends every forward.
func (d *Dialer) Close() {
	d.mu.Lock()
	pods := d.pods
	d.pods = map[types.UID]*podState{}
	d.mu.Unlock()
	for _, st := range pods {
		st.closeForward()
	}
}

// closeForward waits out a probe in flight, so its forward is not left
// open behind the sweep.
func (st *podState) closeForward() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.fwd != nil {
		st.fwd.close()
	}
}

// probe asks the task server whether it hosts sessions. A task server
// from before sessions answers without the field; a pod with no task
// server at all fails the request.
func probe(ctx context.Context, base string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/version", nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("task server version: %s", resp.Status)
	}
	var v struct {
		Sessions int `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return false, err
	}
	return v.Sessions >= 1, nil
}

// forward is a port-forward to a pod's task server, on a local port.
type forward struct {
	base string
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func (f *forward) alive() bool {
	select {
	case <-f.done:
		return false
	default:
		return true
	}
}

func (f *forward) close() {
	f.once.Do(func() { close(f.stop) })
}

// openForward port-forwards a local port to the pod's task server. The
// forward outlives ctx, which bounds only getting it ready; it ends when
// closed or when its connection to the API server does, as when the pod
// goes.
func openForward(ctx context.Context, config *rest.Config, cs kubernetes.Interface, pod *corev1.Pod) (*forward, error) {
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("portforward")
	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return nil, err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, req.URL())
	// Websockets first, as kubectl does, for API servers and proxies
	// that no longer upgrade to SPDY.
	if ws, err := portforward.NewSPDYOverWebsocketDialer(req.URL(), config); err == nil {
		dialer = portforward.NewFallbackDialer(ws, dialer, func(err error) bool {
			return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
		})
	}
	f := &forward{stop: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan struct{})
	var errOut bytes.Buffer
	fw, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", TaskServerPort)}, f.stop, ready, io.Discard, &errOut)
	if err != nil {
		return nil, err
	}
	failed := make(chan error, 1)
	go func() {
		defer close(f.done)
		if err := fw.ForwardPorts(); err != nil {
			failed <- err
		}
	}()
	select {
	case <-ready:
	case err := <-failed:
		return nil, fmt.Errorf("port-forwarding to %s: %w", pod.Name, err)
	case <-ctx.Done():
		f.close()
		return nil, fmt.Errorf("port-forwarding to %s: %w: %s", pod.Name, ctx.Err(), strings.TrimSpace(errOut.String()))
	}
	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		f.close()
		return nil, fmt.Errorf("port-forwarding to %s: no local port: %v", pod.Name, err)
	}
	f.base = fmt.Sprintf("http://127.0.0.1:%d", ports[0].Local)
	return f, nil
}
