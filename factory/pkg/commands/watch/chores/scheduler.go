// Package chores owns the scheduled side of the watch daemon: the agent
// definitions declared under .agents/ in the watched repository, and the cron
// schedules deciding when each of them is due.
//
// The Scheduler runs as an autonomous goroutine so that a chore is queued
// within one evaluation interval of coming due, rather than whenever the scan
// cycle that used to host it happened to run. It reaches the rest of the daemon
// through two narrow collaborators - a Source for the definitions and a Queue
// for the tasks it creates - and so depends on neither GitHub nor the cluster
// directly.
package chores

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"

	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/common"
	"github.com/gke-labs/gemini-for-kubernetes-development/factory/pkg/commands/watch/api"
)

const (
	// DefaultInterval is how often chore schedules are evaluated. Evaluation is
	// an in-memory comparison against the cached definitions, so it can run far
	// more often than those definitions are fetched.
	DefaultInterval = 30 * time.Second
	// DefaultRefreshInterval is how long a fetched set of agent definitions is
	// reused before being re-read from the repository. Reading them costs one
	// request per definition plus one to list them, which is the only expensive
	// part of scheduling and the only part that has to hit the network.
	DefaultRefreshInterval = 5 * time.Minute
)

// Source provides the chore agent definitions declared in the watched repository.
//
// It deals in paths and raw file contents only: parsing frontmatter and
// interpreting schedules is the Scheduler's job, which keeps the
// repository-facing adapter free of any chore semantics.
type Source interface {
	// ListAgentFiles returns the repository paths of the candidate agent
	// definitions, e.g. ".agents/triage.md". A repository that declares no
	// agents at all yields no paths and no error.
	ListAgentFiles(ctx context.Context) ([]string, error)
	// ReadAgentFile returns the decoded contents of the definition at path.
	ReadAgentFile(ctx context.Context, path string) (string, error)
}

// Queue is the subset of the task queue the Scheduler needs. It only ever adds
// chore tasks, and only once the previous run of the same chore has left the
// queue, so nothing here can mutate a task that is already in flight.
type Queue interface {
	// TaskExists reports whether a task with the given file name is queued or running.
	TaskExists(filename string) bool
	// Enqueue adds a task to the queue under the given file name.
	Enqueue(filename string, task *api.QueueTask) error
}

// Definition is a scheduled chore agent declared under .agents/.
type Definition struct {
	// Name is the agent name from the definition's frontmatter. It keys the run state.
	Name string
	// Schedule is the cron expression, descriptor, or pause keyword governing the agent.
	Schedule string
	// Path is the repository path of the definition, recorded on the queued task.
	Path string
}

// Config holds the tuning knobs of a Scheduler.
type Config struct {
	// Interval is the delay between schedule evaluation cycles. Defaults to DefaultInterval.
	Interval time.Duration
	// RefreshInterval is how long fetched definitions are reused before being
	// re-read from the repository. Defaults to DefaultRefreshInterval.
	RefreshInterval time.Duration
	// StateDir is the directory holding the record of when each chore last ran.
	// An empty StateDir keeps that record in memory only, which means a restart
	// runs every chore once.
	StateDir string
	// Owner is the GitHub organization or user owning the watched repository.
	Owner string
	// Repo is the name of the watched repository.
	Repo string
	// DryRun reports what would be queued without touching the queue or the run state.
	DryRun bool
}

// Deps holds the collaborators of a Scheduler.
type Deps struct {
	// Queue receives the chore tasks that come due.
	Queue Queue
	// Source provides the agent definitions to schedule.
	Source Source
	// Paused reports whether scheduling must be held off, which is how the
	// watcher propagates drain mode. It is deliberately a plain read-only
	// signal: the scheduler can observe that the queue is draining but has no
	// way to mutate it. A nil Paused never pauses.
	//
	// Shutdown does not arrive through here. Cancelling the context passed to
	// Run is what stops the scheduler, and unlike this signal it also aborts a
	// cycle that has already started.
	Paused func() bool
}

// Scheduler queues the chore agents whose schedule has come due.
//
// It is single-goroutine by construction: the definition cache and the run
// state it keeps are owned by whoever calls ScheduleOnce, which is the Run loop
// in daemon mode and the caller itself in --once mode. Nothing else may call
// into it concurrently.
type Scheduler struct {
	cfg    Config
	queue  Queue
	source Source
	paused func() bool

	// defs is the last successfully fetched set of definitions, and defsFetchedAt
	// when that was; defsLoaded distinguishes "fetched, found nothing" from
	// "never fetched", which an empty slice cannot.
	defs          []Definition
	defsFetchedAt time.Time
	defsLoaded    bool

	// state records when each chore last ran, read from disk on first use.
	state map[string]RunState
}

// New constructs a Scheduler from its configuration and dependencies.
func New(cfg Config, deps Deps) *Scheduler {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = DefaultRefreshInterval
	}
	return &Scheduler{
		cfg:    cfg,
		queue:  deps.Queue,
		source: deps.Source,
		paused: deps.Paused,
	}
}

// Run evaluates chore schedules until ctx is cancelled, returning nil once it
// has stopped.
//
// A cycle runs immediately so that a restart picks up chores that came due
// while the daemon was down, instead of waiting out a full interval.
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()

	s.ScheduleOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			// Cancellation is how this subcontroller is asked to stop, so it is
			// not an error worth propagating to the caller.
			return nil
		case <-ticker.C:
			s.ScheduleOnce(ctx)
		}
	}
}

// ScheduleOnce evaluates every chore schedule once and queues those that are due.
func (s *Scheduler) ScheduleOnce(ctx context.Context) {
	if s.paused != nil && s.paused() {
		klog.V(2).Infof("Skipping chore scheduling because the watcher is draining.")
		return
	}

	defs := s.definitions(ctx)
	if len(defs) == 0 {
		return
	}

	state := s.runState()
	now := time.Now()
	changed := false
	for _, def := range defs {
		if s.scheduleChore(def, state, now) {
			changed = true
		}
	}

	if changed {
		if err := saveRunState(s.statePath(), state); err != nil {
			// The in-memory copy still holds the new timestamps, so the chore is
			// not queued again until the process restarts.
			klog.Errorf("Failed to persist chore run state: %v", err)
		}
	}
}

// scheduleChore queues one chore if it is due, reporting whether it recorded a
// new run in state.
func (s *Scheduler) scheduleChore(def Definition, state map[string]RunState, now time.Time) bool {
	filename := taskFileName(def.Name)

	// A chore still queued or running from an earlier cycle is skipped outright.
	// The queue is keyed by file name, so a second copy could not be told apart
	// from the first in any case.
	if s.queue.TaskExists(filename) {
		return false
	}

	lastRun := state[def.Name].LastRun
	if !shouldRunAt(def.Schedule, lastRun, now) {
		return false
	}

	if s.cfg.DryRun {
		fmt.Printf("[DRYRUN] Would queue chore agent task %s (schedule: %s)\n", def.Name, def.Schedule)
		return false
	}

	fmt.Printf("Queueing chore agent task %s...\n", def.Name)
	if err := s.queue.Enqueue(filename, s.newTask(def, lastRun, now)); err != nil {
		klog.Errorf("Failed to queue chore task %s: %v", def.Name, err)
		return false
	}

	state[def.Name] = RunState{LastRun: now}
	return true
}

// newTask builds the queue task for a chore that has come due.
func (s *Scheduler) newTask(def Definition, lastRun, now time.Time) *api.QueueTask {
	dueTime := dueAt(def.Schedule, lastRun, now)
	lastRunStr := "never"
	if !lastRun.IsZero() {
		lastRunStr = lastRun.Format(time.RFC3339)
	}

	return &api.QueueTask{
		Type:             api.TypeAgentChore,
		URL:              fmt.Sprintf("https://github.com/%s/%s", s.cfg.Owner, s.cfg.Repo),
		Priority:         api.PriorityMedium,
		Phase:            api.PhaseChores,
		CreatedAt:        now,
		EnqueuedAt:       now,
		TriggerEventTime: dueTime,
		TriggerReason:    api.TriggerReasonChoreScheduled,
		TriggerNotes: fmt.Sprintf("Chore agent '%s' (schedule: '%s') due at %s; last run: %s",
			def.Name, def.Schedule, dueTime.Format(time.RFC3339), lastRunStr),
		Status:    api.StatusPending,
		AgentFile: def.Path,
	}
}

// definitions returns the chore definitions declared in the repository,
// re-reading them once the cached copy is older than the refresh interval.
//
// A failed refresh keeps serving the previous copy: schedules are evaluated in
// memory, so a GitHub outage delays picking up edits under .agents/ rather than
// stopping chores from firing at all.
func (s *Scheduler) definitions(ctx context.Context) []Definition {
	if s.defsLoaded && time.Since(s.defsFetchedAt) < s.cfg.RefreshInterval {
		return s.defs
	}

	defs, err := s.fetchDefinitions(ctx)
	if err != nil {
		klog.Errorf("Failed to load chore agent definitions: %v", err)
		return s.defs
	}

	s.defs, s.defsFetchedAt, s.defsLoaded = defs, time.Now(), true
	return defs
}

// fetchDefinitions reads and parses every agent definition in the repository,
// keeping only those that declare a schedule.
//
// A definition that cannot be read or parsed is reported and skipped: one
// malformed file must not stop the remaining chores from being scheduled.
func (s *Scheduler) fetchDefinitions(ctx context.Context) ([]Definition, error) {
	paths, err := s.source.ListAgentFiles(ctx)
	if err != nil {
		return nil, err
	}

	defs := make([]Definition, 0, len(paths))
	for _, path := range paths {
		content, err := s.source.ReadAgentFile(ctx, path)
		if err != nil {
			klog.Errorf("Failed to fetch chore file %s: %v", path, err)
			continue
		}
		agentDef, err := common.ParseAgent([]byte(content))
		if err != nil {
			klog.Errorf("Failed to parse chore agent %s: %v", path, err)
			continue
		}
		if agentDef.Schedule == "" {
			continue
		}
		defs = append(defs, Definition{
			Name:     agentDef.Name,
			Schedule: agentDef.Schedule,
			Path:     path,
		})
	}
	return defs, nil
}

// taskFileName returns the queue file name of a chore agent's task. It is
// derived from the agent name alone, which is what makes a chore that is
// already queued or running detectable by name.
func taskFileName(agentName string) string {
	return fmt.Sprintf("task-chore-%s.yaml", common.Slugify(agentName))
}
