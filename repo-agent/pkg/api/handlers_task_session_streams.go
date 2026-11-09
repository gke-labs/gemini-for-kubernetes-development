package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/pkg/acpd"
)

// A factory task holds one conversation the daemon hosts - its own,
// session/, the one the board follows and continues - and, for the asks
// of a recipe step that names a session (fix's `session: review`), one
// more per name, session-<name>/, started by the recipe itself. Those are
// the task's side sessions: read-only here, served from their transcript
// files.

// sideSessionRE is a side session's name, as factory names its directory
// (session-<name>).
var sideSessionRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// maxSideEvents bounds one answer of getTaskSessionStream: the client asks
// again from the last seq it got.
const maxSideEvents = 2000

// TaskSessionStreams is what GET .../streams answers.
type TaskSessionStreams struct {
	// Streams are the task's side sessions, by name.
	Streams []string `json:"streams"`
	// Step is the step the task is at: the runner's last "::step" line,
	// such as "10:ask (ask)" - empty before the first.
	Step string `json:"step,omitempty"`
}

// getTaskSessionStreams lists a task's side sessions and the step it is
// at, so the board can show that a quiet conversation is not a stalled
// task.
func (s *Server) getTaskSessionStreams(c *gin.Context) {
	conn, ok := s.resolveTaskSession(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	out := TaskSessionStreams{Streams: []string{}}
	names, err := conn.client.TaskFiles(ctx, conn.task)
	if err != nil && !errors.Is(err, acpd.ErrNotFound) {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	out.Streams = sideSessions(names)
	if log, err := conn.client.ReadTaskFile(ctx, conn.task, "execution.log"); err == nil {
		out.Step = lastStep(log)
	}
	c.JSON(http.StatusOK, out)
}

// getTaskSessionStream is a side session's transcript events after seq
// ?after (all of them without), at most maxSideEvents; "more" says there
// are others to ask for.
func (s *Server) getTaskSessionStream(c *gin.Context) {
	name := c.Param("name")
	if !sideSessionRE.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid session name"})
		return
	}
	after := int64(-1)
	if v := c.Query("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "after must be a number"})
			return
		}
		after = n
	}
	conn, ok := s.resolveTaskSession(c)
	if !ok {
		return
	}
	data, err := conn.client.ReadTaskFileUpTo(c.Request.Context(), conn.task, "session-"+name+"/stream.ndjson", acpd.MaxTranscript)
	switch {
	case errors.Is(err, acpd.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "no session " + name})
		return
	case err != nil:
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	events, more := eventsAfter(data, after, maxSideEvents)
	c.JSON(http.StatusOK, gin.H{"name": name, "events": events, "more": more})
}

// sideSessions are the side session names in a task's file list: the
// directories session-<name> that hold a stream.ndjson.
func sideSessions(files []string) []string {
	out := []string{}
	for _, f := range files {
		dir, file, ok := strings.Cut(f, "/")
		if !ok || file != "stream.ndjson" {
			continue
		}
		name, ok := strings.CutPrefix(dir, "session-")
		if ok && sideSessionRE.MatchString(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// lastStep is the runner's last "::step <n>:<kind> (<kind>)" line of an
// execution.log, without its prefix.
func lastStep(log []byte) string {
	step := ""
	sc := bufio.NewScanner(bytes.NewReader(log))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "::step "); ok {
			step = strings.TrimSpace(rest)
		}
	}
	return step
}

// eventsAfter are the transcript lines of data with a seq over after, as
// raw JSON, at most limit of them; more reports that some were left out.
// A line that does not parse is skipped.
func eventsAfter(data []byte, after int64, limit int) ([]json.RawMessage, bool) {
	out := []json.RawMessage{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var head struct {
			Seq int64 `json:"seq"`
		}
		if json.Unmarshal(line, &head) != nil || head.Seq <= after {
			continue
		}
		if len(out) == limit {
			return out, true
		}
		out = append(out, json.RawMessage(append([]byte(nil), line...)))
	}
	return out, false
}
