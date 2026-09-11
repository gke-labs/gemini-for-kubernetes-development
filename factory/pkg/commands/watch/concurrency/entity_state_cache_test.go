package concurrency

import (
	"fmt"
	"sync"
	"testing"

	githubv39 "github.com/google/go-github/v39/github"
)

func TestEntityStateCache_BasicLifecycle(t *testing.T) {
	c := NewEntityStateCache()

	if c.HasOpenPRs() {
		t.Errorf("expected HasOpenPRs to be false initially")
	}

	// 1. Update Open PRs
	prNum1 := 101
	prNum2 := 102
	body1 := "Fixes #12 and relates to #34"
	headRef := "fix-issue-56"

	pr1 := &githubv39.PullRequest{
		Number: &prNum1,
		Body:   &body1,
		Head:   &githubv39.PullRequestBranch{Ref: &headRef},
	}
	pr2 := &githubv39.PullRequest{
		Number: &prNum2,
	}

	c.UpdateOpenPRs([]*githubv39.PullRequest{pr1, pr2})

	if !c.HasOpenPRs() {
		t.Errorf("expected HasOpenPRs to be true after update")
	}

	if !c.IsOpenPR(101) || !c.IsOpenPR(102) || c.IsOpenPR(999) {
		t.Errorf("IsOpenPR check failed")
	}

	// 2. Referenced issues
	if !c.IsIssueReferenced(12) {
		t.Errorf("expected issue 12 to be referenced")
	}
	if !c.IsIssueReferenced(34) {
		t.Errorf("expected issue 34 to be referenced")
	}
	if !c.IsIssueReferenced(56) {
		t.Errorf("expected issue 56 to be referenced")
	}
	if c.IsIssueReferenced(99) {
		t.Errorf("expected issue 99 to not be referenced")
	}

	refMap := c.GetReferencedIssuesMap()
	if len(refMap) != 3 || !refMap[12] || !refMap[34] || !refMap[56] {
		t.Errorf("unexpected referenced issues map: %v", refMap)
	}

	// Mutating returned referenced issues map must not leak
	refMap[999] = true
	if c.IsIssueReferenced(999) {
		t.Errorf("modifying GetReferencedIssuesMap leaked into cache")
	}

	// 3. Open Issues
	c.SetOpenIssueNumbers([]int{30, 40})
	if !c.IsOpenIssue(30) || !c.IsOpenIssue(40) || c.IsOpenIssue(10) {
		t.Errorf("unexpected open issues after SetOpenIssueNumbers")
	}

	c.SetOpenIssueNumbers([]int{50})
	if c.IsOpenIssue(30) || !c.IsOpenIssue(50) {
		t.Errorf("expected SetOpenIssueNumbers to replace the previous set")
	}
}

// The two halves of the cache are filled by different scans, and consumers gate
// on them independently, so populating one must not claim the other is ready.
func TestEntityStateCache_PopulationIsTrackedPerHalf(t *testing.T) {
	c := NewEntityStateCache()

	if c.HasOpenPRs() || c.HasOpenIssues() {
		t.Fatalf("expected a fresh cache to report both halves unpopulated")
	}

	c.UpdateOpenPRs([]*githubv39.PullRequest{})
	if !c.HasOpenPRs() {
		t.Errorf("expected a PR scan that found nothing to still count as populated")
	}
	if c.HasOpenIssues() {
		t.Errorf("expected a PR scan to leave the issue half unpopulated")
	}

	c.SetOpenIssueNumbers(nil)
	if !c.HasOpenIssues() {
		t.Errorf("expected an issue scan that found nothing to still count as populated")
	}
}

func TestEntityStateCache_ConcurrentAccessStress(t *testing.T) {
	c := NewEntityStateCache()

	numWorkers := 50
	iterations := 100
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for i := 0; i < numWorkers; i++ {
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				num := (workerID*10 + j) % 30

				switch j % 4 {
				case 0:
					prNum := num
					body := fmt.Sprintf("Fixes #%d", num)
					ref := fmt.Sprintf("branch-%d", num)
					c.UpdateOpenPRs([]*githubv39.PullRequest{
						{
							Number: &prNum,
							Body:   &body,
							Head:   &githubv39.PullRequestBranch{Ref: &ref},
						},
					})
				case 1:
					_ = c.IsOpenPR(num)
					_ = c.HasOpenPRs()
				case 2:
					_ = c.IsIssueReferenced(num)
					_ = c.GetReferencedIssuesMap()
				case 3:
					c.SetOpenIssueNumbers([]int{num, num + 1})
					_ = c.IsOpenIssue(num)
					_ = c.HasOpenIssues()
				}
			}
		}(i)
	}

	wg.Wait()
}
