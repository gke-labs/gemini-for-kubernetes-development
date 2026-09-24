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
//     adopt the result, correct the annotation)
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

func (p *PodTaskProber) Probe(ctx context.Context, namespace, sandboxName, prefix, outputFile string) (TaskProbe, error) {
	none := TaskProbe{State: ProbeNone}

	sb, err := p.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		return none, nil // no sandbox yet: nothing to duplicate
	}
	annotations := sb.GetAnnotations()
	if annotations["sandbox.gemini.google.com/last-task-state"] != "Running" ||
		annotations["sandbox.gemini.google.com/last-task-type"] != prefix {
		// A live invocation owns (or owned) this sandbox's story; only a
		// stale Running claim marks a recovered orphan.
		return none, nil
	}

	podID, err := sandbox.FindSandboxPodInNamespace(ctx, sandboxName, namespace)
	if err != nil || podID == nil {
		// Annotation claims Running but there is no pod: the run died with
		// its pod. Correct the record and release the launch path.
		p.stampTaskState(ctx, namespace, sandboxName, prefix, "Failed")
		return none, nil
	}

	// One round-trip: newest <prefix>-* dir → running / finished verdict
	// plus exit code and (when finished) the requested output file.
	// The busy verdict is sandbox-WIDE: one task per sandbox is the
	// invariant (tasks share a workspace), so ANY live task — regardless
	// of type — makes every launcher skip; the next reconcile requeues.
	// Orphan adoption below stays prefix-scoped: only our own task type's
	// leftovers are ours to harvest.
	script := fmt.Sprintf(`for p in /workspaces/tasks/*/pid; do
  [ -f "$p" ] || continue
  t=$(dirname "$p")
  [ -f "$t/exit_code" ] && continue
  if kill -0 "$(cat "$p" 2>/dev/null)" 2>/dev/null; then echo "running|"; exit 0; fi
done
d=$(ls -dt /workspaces/tasks/%s-* 2>/dev/null | head -1)
if [ -z "$d" ]; then echo "none|"; exit 0; fi
if [ -f "$d/exit_code" ]; then
  echo "finished|$(cat "$d/exit_code")"
  %s
elif [ -f "$d/pid" ] && kill -0 "$(cat "$d/pid" 2>/dev/null)" 2>/dev/null; then
  echo "running|"
else
  echo "dead|"
fi`, prefix, collectCmd(outputFile))
	var stdout, stderr bytes.Buffer
	if err := sandbox.ExecInPod(ctx, p.kube, *podID, sandbox.ExecOptions{
		Command: []string{"sh", "-c", script},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}); err != nil {
		return none, fmt.Errorf("probing sandbox %s/%s: %w", namespace, sandboxName, err)
	}

	out := stdout.String()
	head, rest, _ := strings.Cut(out, "\n")
	verdict, exitCode, _ := strings.Cut(strings.TrimSpace(head), "|")
	switch verdict {
	case "running":
		return TaskProbe{State: ProbeRunning}, nil
	case "finished":
		state := "Completed"
		if exitCode != "0" {
			state = "Failed"
		}
		p.stampTaskState(ctx, namespace, sandboxName, prefix, state)
		return TaskProbe{State: ProbeOrphanCompleted, ExitCode: exitCode, Output: strings.TrimSpace(rest)}, nil
	case "dead":
		// Launched but died without an exit code (pod restart mid-task).
		p.stampTaskState(ctx, namespace, sandboxName, prefix, "Failed")
		return none, nil
	default:
		return none, nil
	}
}

// ObserveTask reports the newest <prefix>-* task's own record. Unlike
// Probe it consults no annotation and corrects nothing: a Run knows
// which sandbox and task type it launched, and wants the facts.
//
// The exit code is read before liveness, because a zombie answers
// kill -0 — the bug that once reported a finished deployment as
// "running · 3h53m" and disabled Tear down.
func (p *PodTaskProber) ObserveTask(ctx context.Context, namespace, sandboxName, prefix string) (TaskObservation, error) {
	none := TaskObservation{State: ObserveNone}

	podID, err := sandbox.FindSandboxPodInNamespace(ctx, sandboxName, namespace)
	if err != nil || podID == nil {
		// No pod: whatever ran is not running now, and its record is
		// unreachable. The caller decides what that means.
		return none, nil
	}

	script := fmt.Sprintf(`d=$(ls -dt /workspaces/tasks/%s-* 2>/dev/null | head -1)
if [ -z "$d" ]; then echo "none||"; exit 0; fi
echo "DIR=$(basename "$d")"
echo "START=$(cat "$d/start_time" 2>/dev/null)"
if [ -f "$d/exit_code" ]; then
  echo "finished|$(cat "$d/exit_code")"
elif [ -f "$d/pid" ] && kill -0 "$(cat "$d/pid" 2>/dev/null)" 2>/dev/null &&
     [ "$(cat /proc/$(cat "$d/pid")/stat 2>/dev/null | awk '{print $3}')" != "Z" ]; then
  echo "running|"
else
  echo "dead|"
fi`, prefix)

	var stdout, stderr bytes.Buffer
	if err := sandbox.ExecInPod(ctx, p.kube, *podID, sandbox.ExecOptions{
		Command: []string{"sh", "-c", script},
		Stdout:  &stdout,
		Stderr:  &stderr,
	}); err != nil {
		return none, fmt.Errorf("observing %s/%s task %s: %w", namespace, sandboxName, prefix, err)
	}

	obs := TaskObservation{State: ObserveNone}
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "DIR="):
			obs.Dir = strings.TrimPrefix(line, "DIR=")
		case strings.HasPrefix(line, "START="):
			if t, err := time.Parse(time.ANSIC, strings.TrimPrefix(line, "START=")); err == nil {
				obs.StartedAt = t
			}
		case strings.HasPrefix(line, "finished|"):
			obs.State = ObserveFinished
			fmt.Sscanf(strings.TrimPrefix(line, "finished|"), "%d", &obs.ExitCode)
		case strings.HasPrefix(line, "running|"):
			obs.State = ObserveRunning
		case strings.HasPrefix(line, "dead|"):
			obs.State = ObserveDead
		}
	}
	return obs, nil
}

func collectCmd(outputFile string) string {
	if outputFile == "" {
		return ""
	}
	return fmt.Sprintf(`cat "$d/%s" 2>/dev/null`, outputFile)
}

// stampTaskState is the watch IsTaskRunning side effect: the invocation
// that should have stamped the final state died with the old controller,
// so the prober corrects the record — the board and the pause pass see
// truth, and later probes are cheap.
func (p *PodTaskProber) stampTaskState(ctx context.Context, namespace, sandboxName, taskType, state string) {
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
	annotations["sandbox.gemini.google.com/last-task-state"] = state
	annotations["sandbox.gemini.google.com/last-task-type"] = taskType
	annotations["sandbox.gemini.google.com/last-task-time"] = now
	annotations["sandbox.gemini.google.com/completion-time"] = now
	sb.SetAnnotations(annotations)
	if _, err := p.kube.DynamicClient.Resource(k8s.SandboxGVR).Namespace(namespace).Update(ctx, sb, metav1.UpdateOptions{}); err != nil {
		klog.Warningf("factorycli: cannot stamp adopted task state on %s/%s: %v", namespace, sandboxName, err)
	}
}
