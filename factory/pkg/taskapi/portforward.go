package taskapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/clients"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// forward is a port-forward to a pod's task server, on a local port. It
// goes through the API server, which checks the caller may port-forward
// to the pod (pods/portforward), and needs no kubectl.
type forward struct {
	local int
	stop  chan struct{}
	done  chan struct{}
}

func openForward(ctx context.Context, kc *clients.KubernetesClient, namespace, pod string) (*forward, error) {
	req := kc.Clientset.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("portforward")
	transport, upgrader, err := spdy.RoundTripperFor(kc.RestConfig)
	if err != nil {
		return nil, err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, req.URL())
	// Websockets first, as kubectl does, for API servers and proxies
	// that no longer upgrade to SPDY.
	if ws, err := portforward.NewSPDYOverWebsocketDialer(req.URL(), kc.RestConfig); err == nil {
		dialer = portforward.NewFallbackDialer(ws, dialer, func(err error) bool {
			return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
		})
	}
	f := &forward{stop: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan struct{})
	var errOut bytes.Buffer
	fw, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", Port)}, f.stop, ready, io.Discard, &errOut)
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
		return nil, fmt.Errorf("port-forwarding to %s: %w", pod, err)
	case <-ctx.Done():
		close(f.stop)
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		close(f.stop)
		return nil, fmt.Errorf("port-forwarding to %s: not ready after 30s: %s", pod, strings.TrimSpace(errOut.String()))
	}
	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		close(f.stop)
		return nil, fmt.Errorf("port-forwarding to %s: no local port: %v", pod, err)
	}
	f.local = int(ports[0].Local)
	return f, nil
}

func (f *forward) baseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", f.local)
}

// alive is whether the forward still runs: it ends with its connection to
// the API server, when the pod goes, say.
func (f *forward) alive() bool {
	select {
	case <-f.done:
		return false
	default:
		return true
	}
}

func (f *forward) close() {
	select {
	case <-f.stop:
	default:
		close(f.stop)
	}
}
