package factorycli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/clients"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/k8s"
	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/sandbox"
)

// PodTaskProber implements TaskProber against the sandbox pod. envd
// launches tasks as nohup processes under /workspaces/tasks/<prefix>-<ts>/
// with pid / exit_code / output files, and factory stamps the sandbox's
// last-task annotations — together they distinguish the three verdicts the
// dispatchTask discipline needs:
//
//   - annotation Running + pid alive        → running   (skip, requeue)
//   - annotation Running + exit_code present → orphan    (the invocation
//     died with the old controller before it could stamp or harvest —
//     correct the annotation)
//   - anything else                          → none      (launch normally;
//     a properly finished run was stamped by its own live invocation)
type PodTaskProber struct {
	kube *clients.KubernetesClient
}

func NewPodTaskProber() (*PodTaskProber, error) {
	kube, err := clients.NewKubernetesClient()
	if err != nil {
		return nil, err
	}
	return &PodTaskProber{kube: kube}, nil
}

func (p *PodTaskProber) Probe(ctx context.Context, namespace, sandboxName, prefix string) (TaskProbe, error) {
	none := TaskProbe{State: ProbeNone}

	sb, err := p.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		return none, nil // no sandbox yet: nothing to duplicate
	}
	annotations := sb.GetAnnotations()
	// A side task — triage in the issue's sandbox — claims Running under
	// its own name (sandbox.gemini.google.com/<type>-task-state) and
	// leaves last-task-* to the fix.
	own, side, other := runningClaims(annotations, prefix)
	if !own && !other {
		// A live invocation owns (or owned) this sandbox's story; only a
		// stale Running claim marks a recovered orphan.
		return none, nil
	}
	stamp := func(state string) {
		if own {
			p.stampTaskState(ctx, namespace, sandboxName, prefix, state, side)
		}
	}

	podID, err := sandbox.FindSandboxPodInNamespace(ctx, sandboxName, namespace)
	if err != nil || podID == nil {
		// Annotation claims Running but there is no pod: the run died with
		// its pod. Correct the record and release the launch path.
		stamp("Failed")
		return none, nil
	}

	// One round-trip: newest <prefix>-* dir → running / finished verdict
	// plus exit code.
	// The busy verdict is sandbox-WIDE: one task per sandbox is the
	// invariant (tasks share a workspace), so ANY live task — regardless
	// of type — makes every launcher skip; the next reconcile requeues.
	// Orphan correction below stays prefix-scoped: only our own task
	// type's leftovers are ours to settle.
	script := fmt.Sprintf(`is_alive() {
  t="$1"
  pid=$(cat "$t/pid" 2>/dev/null)
  [ -n "$pid" ] || return 1
  stat=$(ps -o stat= -p "$pid" 2>/dev/null | cut -c 1)
  want=$(cat "$t/start_time" 2>/dev/null | xargs)
  got=$(ps -p "$pid" -o lstart= 2>/dev/null | xargs)
  kill -0 "$pid" 2>/dev/null && [ -n "$stat" ] && [ "$stat" != "Z" ] && [ -n "$got" ] && { [ -z "$want" ] || [ "$want" = "$got" ]; }
}
for p in /workspaces/tasks/*/pid; do
  [ -f "$p" ] || continue
  t=$(dirname "$p")
  [ -s "$t/exit_code" ] && continue
  if is_alive "$t"; then echo "running|"; exit 0; fi
done
d=$(ls -dt /workspaces/tasks/%s-* 2>/dev/null | head -1)
if [ -z "$d" ]; then echo "none|"; exit 0; fi
if [ -s "$d/exit_code" ]; then
  echo "finished|$(cat "$d/exit_code")"
elif [ -f "$d/pid" ] && is_alive "$d"; then
  echo "running|"
else
  echo 137 > "$d/exit_code" 2>/dev/null || true
  echo "dead|"
fi`, prefix)
	var stdout, stderr bytes.Buffer
	if err := sandbox.ExecInPod(ctx, p.kube, *podID, sandbox.ExecOptions{
		Command: []string{"sh", "-c", script},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}); err != nil {
		return none, fmt.Errorf("probing sandbox %s/%s: %w", namespace, sandboxName, err)
	}

	out := stdout.String()
	head, _, _ := strings.Cut(out, "\n")
	verdict, exitCode, _ := strings.Cut(strings.TrimSpace(head), "|")
	switch verdict {
	case "running":
		return TaskProbe{State: ProbeRunning}, nil
	case "finished":
		if !own {
			return none, nil
		}
		state := "Completed"
		if exitCode != "0" {
			state = "Failed"
		}
		stamp(state)
		return TaskProbe{State: ProbeOrphanCompleted, ExitCode: exitCode}, nil
	case "dead":
		// Launched but died without an exit code (pod restart mid-task).
		stamp("Failed")
		return none, nil
	default:
		return none, nil
	}
}

// stampTaskState is the watch IsTaskRunning side effect: the invocation
// that should have stamped the final state died with the old controller,
// so the prober corrects the record — the board and the pause pass see
// truth, and later probes are cheap.
// runningClaims reads the Running claims on a sandbox for a launch of
// prefix: own, its own (side, under its side-task key); other, another
// type's — a triage while a fix launches, a fix while a triage does, now
// that they share the issue's sandbox — which only asks for the
// sandbox-wide busy check, its leftovers not being ours.
func runningClaims(annotations map[string]string, prefix string) (own, side, other bool) {
	side = annotations[sideTaskStateKey(prefix)] == "Running"
	own = side || (annotations["sandbox.gemini.google.com/last-task-state"] == "Running" &&
		annotations["sandbox.gemini.google.com/last-task-type"] == prefix)
	for k, v := range annotations {
		if v != "Running" || !strings.HasPrefix(k, "sandbox.gemini.google.com/") || !strings.HasSuffix(k, "task-state") {
			continue
		}
		if k == sideTaskStateKey(prefix) || (own && !side && k == "sandbox.gemini.google.com/last-task-state") {
			continue
		}
		other = true
	}
	return own, side, other
}

func sideTaskStateKey(taskType string) string {
	return "sandbox.gemini.google.com/" + taskType + "-task-state"
}

func (p *PodTaskProber) stampTaskState(ctx context.Context, namespace, sandboxName, taskType, state string, side bool) {
	sb, err := p.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		klog.Warningf("factorycli: cannot stamp adopted task state on %s/%s: %v", namespace, sandboxName, err)
		return
	}
	annotations := sb.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if side {
		annotations[sideTaskStateKey(taskType)] = state
	} else {
		annotations["sandbox.gemini.google.com/last-task-state"] = state
		annotations["sandbox.gemini.google.com/last-task-type"] = taskType
	}
	annotations["sandbox.gemini.google.com/last-task-time"] = now
	annotations["sandbox.gemini.google.com/completion-time"] = now
	sb.SetAnnotations(annotations)
	if _, err := p.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, sb, metav1.UpdateOptions{}); err != nil {
		klog.Warningf("factorycli: cannot stamp adopted task state on %s/%s: %v", namespace, sandboxName, err)
	}
}
