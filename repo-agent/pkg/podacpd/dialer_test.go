package podacpd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
)

// taskServer answers the version with version, and a session "s" at
// /v1/sessions/s, the way a daemon that hosts sessions does.
func taskServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(version))
	})
	mux.HandleFunc("GET /v1/sessions/s", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"s","engine":"gemini","busy":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// podIP is the fallback, answering for any session as not busy.
func podIP(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"s","engine":"gemini","busy":false}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type fixture struct {
	d     *Dialer
	opens int
	now   time.Time
	fwds  []*forward
}

func newFixture(t *testing.T, open func() (string, error)) *fixture {
	t.Helper()
	ip := podIP(t)
	f := &fixture{now: time.Unix(1_000_000, 0)}
	f.d = &Dialer{
		byIP:  func(string) *acpd.Client { return acpd.New(ip.URL) },
		pods:  map[types.UID]*podState{},
		clock: func() time.Time { return f.now },
		open: func(context.Context, *corev1.Pod) (*forward, error) {
			f.opens++
			base, err := open()
			if err != nil {
				return nil, err
			}
			fwd := &forward{base: base, stop: make(chan struct{}), done: make(chan struct{})}
			f.fwds = append(f.fwds, fwd)
			return fwd, nil
		},
	}
	return f
}

var pod = &corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns", UID: "uid-1"},
	Status:     corev1.PodStatus{PodIP: "10.0.0.1"},
}

// busy is which way the client went: the task server says busy, the pod
// IP not.
func (f *fixture) busy(t *testing.T) bool {
	t.Helper()
	s, err := f.d.Client(context.Background(), pod).GetSession(context.Background(), "s")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	return s.Busy
}

func TestAPodWhoseDaemonHostsSessionsIsForwardedToOnce(t *testing.T) {
	srv := taskServer(t, `{"api":1,"sessions":1}`)
	f := newFixture(t, func() (string, error) { return srv.URL, nil })
	for range 3 {
		if !f.busy(t) {
			t.Fatal("went to the pod IP, not the forward")
		}
	}
	if f.opens != 1 {
		t.Errorf("opened %d forwards, want 1", f.opens)
	}

	// The forward ended, with its pod say: the next call opens another.
	close(f.fwds[0].done)
	if !f.busy(t) || f.opens != 2 {
		t.Errorf("a dead forward was not replaced: opens = %d", f.opens)
	}
}

func TestAnOlderDaemonIsDialledOnItsIPAndNotAskedAgain(t *testing.T) {
	srv := taskServer(t, `{"api":1}`)
	f := newFixture(t, func() (string, error) { return srv.URL, nil })
	for range 2 {
		if f.busy(t) {
			t.Fatal("used the forward of a daemon that hosts no sessions")
		}
	}
	if f.opens != 1 {
		t.Errorf("opened %d forwards, want 1", f.opens)
	}
	select {
	case <-f.fwds[0].stop:
	default:
		t.Error("the probe's forward was left open")
	}
}

func TestAForwardThatFailsIsRetriedLater(t *testing.T) {
	srv := taskServer(t, `{"api":1,"sessions":1}`)
	fail := true
	f := newFixture(t, func() (string, error) {
		if fail {
			return "", errors.New("forbidden: pods/portforward")
		}
		return srv.URL, nil
	})
	for range 2 {
		if f.busy(t) {
			t.Fatal("no forward, yet not the pod IP")
		}
	}
	if f.opens != 1 {
		t.Errorf("retried at once: opens = %d", f.opens)
	}

	fail = false
	f.now = f.now.Add(retryAfter + time.Second)
	if !f.busy(t) {
		t.Error("the forward was not tried again")
	}
}

func TestIdlePodsAreForgotten(t *testing.T) {
	srv := taskServer(t, `{"api":1,"sessions":1}`)
	f := newFixture(t, func() (string, error) { return srv.URL, nil })
	f.busy(t)
	f.now = f.now.Add(idleAfter + time.Minute)
	f.d.Client(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "other"}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case <-f.fwds[0].stop:
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("an idle pod's forward was not closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestANilDialerDialsTheIP(t *testing.T) {
	var d *Dialer
	if d.Client(context.Background(), pod) == nil {
		t.Fatal("no client")
	}
}

func TestSessionsAreOnlyOnTheForward(t *testing.T) {
	srv := taskServer(t, `{"api":1,"sessions":1}`)
	f := newFixture(t, func() (string, error) { return srv.URL, nil })
	if _, ok := f.d.Sessions(context.Background(), pod); !ok {
		t.Error("a daemon that hosts sessions has none")
	}

	old := taskServer(t, `{"api":1}`)
	f = newFixture(t, func() (string, error) { return old.URL, nil })
	if _, ok := f.d.Sessions(context.Background(), pod); ok {
		t.Error("an older daemon's pod IP was offered as its sessions")
	}
	var d *Dialer
	if _, ok := d.Sessions(context.Background(), pod); ok {
		t.Error("a nil Dialer has sessions")
	}
}
