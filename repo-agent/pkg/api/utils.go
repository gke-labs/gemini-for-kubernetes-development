/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	sandboxtaskv1alpha1 "github.com/gke-labs/gemini-for-kubernetes-development/repo-agent/api/sandboxtask/v1alpha1"
	"github.com/google/go-github/v39/github"
	"k8s.io/klog/v2"
)

func SortSandboxTasks(tasks []sandboxtaskv1alpha1.SandboxTask) {
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].CreationTimestamp.Equal(&tasks[j].CreationTimestamp) {
			return tasks[i].Name > tasks[j].Name
		}
		return tasks[i].CreationTimestamp.After(tasks[j].CreationTimestamp.Time)
	})
}

func fixYAMLIntegers(in interface{}) interface{} {
	switch v := in.(type) {
	case int:
		return int64(v)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			out[k] = fixYAMLIntegers(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, val := range v {
			out[i] = fixYAMLIntegers(val)
		}
		return out
	default:
		return v
	}
}

func convInt64SliceToInterfaceSlice(in []int64) []interface{} {
	out := make([]interface{}, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func parseRepoURL(repoURL string) (string, string, error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo url: %s", repoURL)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

// TODO this is k8s specific. we need to generalize it later.
var allowedLabelPrefixes = []string{"area/", "kind/", "priority/", "sig/", "type/"}

func getSuggestedLabels(ctx context.Context, client *github.Client, owner, repo string) ([][]string, error) {
	log := klog.FromContext(ctx)
	query := fmt.Sprintf("repo:%s/%s involves:@me is:pr", owner, repo)
	opts := &github.SearchOptions{
		Sort:        "updated",
		Order:       "desc",
		ListOptions: github.ListOptions{PerPage: 100},
	}

	result, _, err := client.Search.Issues(ctx, query, opts)
	if err != nil {
		return nil, err
	}

	for _, issue := range result.Issues {
		log.Info("Found issue", "issueNumber", *issue.Number)
		for _, label := range issue.Labels {
			if label.Name != nil {
				log.Info("Issue label", "label", *label.Name)
			}
		}
	}

	// Count the label ocurrences
	issueLabels := make([][]string, 0, len(result.Issues))
	unlabelledCount := 0
	for _, issue := range result.Issues {
		issueLabels = append(issueLabels, []string{})
		i := len(issueLabels) - 1
		if len(issue.Labels) == 0 {
			unlabelledCount++
		}
		for _, label := range issue.Labels {
			if label.Name != nil {
				// Only consider labels with allowed prefixes
				allowed := false
				for _, prefix := range allowedLabelPrefixes {
					if strings.HasPrefix(*label.Name, prefix) {
						allowed = true
						break
					}
				}
				if !allowed {
					continue
				}
				issueLabels[i] = append(issueLabels[i], *label.Name)
			}
		}
	}

	labels, count := findMostCommonCoOccurringLabels(issueLabels)

	// If the most common co-occurring labels appear in less than all unlabelled PRs, return no suggestions
	if count < unlabelledCount {
		return make([][]string, 0), nil
	}

	return labels, nil
}

// The size of the co-occurring set we want to find (couplets)
const TargetSize = 2
const TopN = 5

// combinations recursively generates all combinations of size k from the list of labels.
func combinations(labels []string, k int) [][]string {
	var result [][]string

	var generate func(start int, currentCombination []string)
	generate = func(start int, currentCombination []string) {
		// Base case: combination is complete
		if len(currentCombination) == k {
			// Create a copy to append to the result
			combinationCopy := make([]string, k)
			copy(combinationCopy, currentCombination)
			result = append(result, combinationCopy)
			return
		}

		// Recursive step
		for i := start; i < len(labels); i++ {
			// Add the current label and recurse
			generate(i+1, append(currentCombination, labels[i]))
		}
	}

	generate(0, []string{})
	return result
}

// findMostCommonCoOccurringLabels processes the data to find the triplet with the highest support.
func findMostCommonCoOccurringLabels(itemLabels [][]string) ([][]string, int) {
	// 1. Initialize a map to store the counts (support) of each label set
	counts := make(map[string]int)

	// 2. Iterate over all items
	for _, labels := range itemLabels {
		// Skip items that don't have enough labels
		if len(labels) < TargetSize {
			continue
		}

		// It is crucial to sort the labels *before* generating combinations
		// to ensure that combinations like (a, b, c) and (c, b, a) are treated
		// as the same set, ensuring consistent counting.
		sort.Strings(labels)

		// 3. Generate all combinations of size TargetSize
		combos := combinations(labels, TargetSize)

		// 4. Count the support for each combination
		for _, combo := range combos {
			// Create a unique string key for the map from the sorted combination
			// This key represents the unique itemset {L1, L2, L3}
			key := strings.Join(combo, "||")
			counts[key]++
		}
	}

	type labelCount struct {
		labels []string
		count  int
	}

	allCounts := make([]labelCount, 0, len(counts))
	for key, count := range counts {
		allCounts = append(allCounts, labelCount{
			labels: strings.Split(key, "||"),
			count:  count,
		})
	}

	// Sort by count desc, then by label key asc
	sort.Slice(allCounts, func(i, j int) bool {
		if allCounts[i].count != allCounts[j].count {
			return allCounts[i].count > allCounts[j].count
		}
		return strings.Join(allCounts[i].labels, "||") < strings.Join(allCounts[j].labels, "||")
	})

	resultSize := len(allCounts)
	if resultSize > TopN {
		resultSize = TopN
	}
	result := make([][]string, 0, resultSize)
	maxCount := 0
	if len(allCounts) > 0 {
		maxCount = allCounts[0].count
	}

	for i := 0; i < len(allCounts) && i < TopN; i++ {
		result = append(result, allCounts[i].labels)
	}

	return result, maxCount
}
