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

type fixture struct {
	d     *Dialer
	opens int
	now   time.Time
	fwds  []*forward
}

func newFixture(t *testing.T, open func() (string, error)) *fixture {
	t.Helper()
	f := &fixture{now: time.Unix(1_000_000, 0)}
	f.d = &Dialer{
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
}

// reached is whether the pod's sessions were offered; when they were,
// they must be the task server's.
func (f *fixture) reached(t *testing.T) bool {
	t.Helper()
	c, ok := f.d.Sessions(context.Background(), pod)
	if !ok {
		return false
	}
	s, err := c.GetSession(context.Background(), "s")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !s.Busy {
		t.Fatal("the session is not the task server's")
	}
	return true
}

func TestAPodWhoseDaemonHostsSessionsIsForwardedToOnce(t *testing.T) {
	srv := taskServer(t, `{"api":1,"sessions":1}`)
	f := newFixture(t, func() (string, error) { return srv.URL, nil })
	for range 3 {
		if !f.reached(t) {
			t.Fatal("a daemon that hosts sessions was not reached")
		}
	}
	if f.opens != 1 {
		t.Errorf("opened %d forwards, want 1", f.opens)
	}

	// The forward ended, with its pod say: the next call opens another.
	close(f.fwds[0].done)
	if !f.reached(t) || f.opens != 2 {
		t.Errorf("a dead forward was not replaced: opens = %d", f.opens)
	}
}

func TestAnOlderDaemonIsUnreachableAndNotAskedAgain(t *testing.T) {
	srv := taskServer(t, `{"api":1}`)
	f := newFixture(t, func() (string, error) { return srv.URL, nil })
	for range 2 {
		if f.reached(t) {
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
		if f.reached(t) {
			t.Fatal("no forward, yet reached")
		}
	}
	if f.opens != 1 {
		t.Errorf("retried at once: opens = %d", f.opens)
	}

	fail = false
	f.now = f.now.Add(retryAfter + time.Second)
	if !f.reached(t) {
		t.Error("the forward was not tried again")
	}
}

func TestIdlePodsAreForgotten(t *testing.T) {
	srv := taskServer(t, `{"api":1,"sessions":1}`)
	f := newFixture(t, func() (string, error) { return srv.URL, nil })
	f.reached(t)
	f.now = f.now.Add(idleAfter + time.Minute)
	f.d.Sessions(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "other"}})
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

func TestSessionsAreOnlyOnADaemonThatHostsThem(t *testing.T) {
	srv := taskServer(t, `{"api":1,"sessions":1}`)
	f := newFixture(t, func() (string, error) { return srv.URL, nil })
	if _, ok := f.d.Sessions(context.Background(), pod); !ok {
		t.Error("a daemon that hosts sessions has none")
	}

	old := taskServer(t, `{"api":1}`)
	f = newFixture(t, func() (string, error) { return old.URL, nil })
	if _, ok := f.d.Sessions(context.Background(), pod); ok {
		t.Error("an older daemon was offered as having sessions")
	}
	var d *Dialer
	if _, ok := d.Sessions(context.Background(), pod); ok {
		t.Error("a nil Dialer has sessions")
	}
}
