package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-github/v39/github"
)

func TestGetSuggestedLabels(t *testing.T) {
	// Mock GitHub API
	mux := http.NewServeMux()
	mux.HandleFunc("/search/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		expectedQ := "repo:owner/repo involves:@me is:pr"
		if q != expectedQ {
			t.Errorf("Expected query %q, got %q", expectedQ, q)
		}
		// Return mock response with duplicates to test deduplication
		fmt.Fprint(w, `{"items": [{"number": 1, "labels": [{"name": "kind/bug"}, {"name": "kind/feature"}]}, {"number": 2, "labels": [{"name": "kind/bug"}, {"name": "kind/documentation"}]}]}`)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// Configure client to use mock server
	client := github.NewClient(nil)
	url, _ := url.Parse(server.URL + "/")
	client.BaseURL = url
	client.UploadURL = url

	labels, err := getSuggestedLabels(context.Background(), client, "owner", "repo")
	if err != nil {
		t.Fatalf("getSuggestedLabels failed: %v", err)
	}

	// Expecting both combinations (count 1 each). Sorted alphabetically by implementation.
	expectedLabels := [][]string{{"kind/bug", "kind/documentation"}, {"kind/bug", "kind/feature"}}

	if len(labels) != len(expectedLabels) {
		t.Fatalf("Expected %d label sets, got %d", len(expectedLabels), len(labels))
	}

	for i, expected := range expectedLabels {
		sort.Strings(expected)
		got := labels[i]
		sort.Strings(got)
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("Index %d: Expected %v, got %v", i, expected, got)
		}
	}
}

func TestFindMostCommonCoOccurringLabels(t *testing.T) {
	tests := []struct {
		name           string
		itemLabels     [][]string
		expectedLabels [][]string
		expectedCount  int
	}{

		{
			name:           "empty input",
			itemLabels:     [][]string{},
			expectedLabels: nil,
			expectedCount:  0,
		},
		{
			name: "single item, not enough labels",
			itemLabels: [][]string{
				{"bug"},
			},
			expectedLabels: nil,
			expectedCount:  0,
		},
		{
			name: "single item, exact target size",
			itemLabels: [][]string{
				{"bug", "feature"},
			},
			expectedLabels: [][]string{{"bug", "feature"}},
			expectedCount:  1,
		},
		{
			name: "single item, more than target size",
			itemLabels: [][]string{
				{"bug", "feature", "documentation"},
			},
			// Combinations: (bug, doc), (bug, feat), (doc, feat). All count 1.
			// Sorted by key: bug||documentation, bug||feature, documentation||feature
			expectedLabels: [][]string{{"bug", "documentation"}, {"bug", "feature"}, {"documentation", "feature"}},
			expectedCount:  1,
		},
		{
			name: "multiple items, no common couplets",
			itemLabels: [][]string{
				{"bug", "feature"},
				{"documentation", "enhancement"},
			},
			// (bug, feat): 1, (doc, enh): 1.
			expectedLabels: [][]string{{"bug", "feature"}, {"documentation", "enhancement"}},
			expectedCount:  1,
		},
		{
			name: "multiple items, one common couplet",
			itemLabels: [][]string{
				{"bug", "feature"},
				{"bug", "feature", "documentation"},
				{"feature", "enhancement"},
			},
			// (bug, feat): 2. Others 1.
			expectedLabels: [][]string{{"bug", "feature"}, {"bug", "documentation"}, {"documentation", "feature"}, {"enhancement", "feature"}},
			expectedCount:  2,
		},
		{
			name: "multiple items, common couplet with varying order",
			itemLabels: [][]string{
				{"bug", "feature"},
				{"feature", "bug"}, // Order should not matter due to sorting
			},
			expectedLabels: [][]string{{"bug", "feature"}},
			expectedCount:  2,
		},
		{
			name: "multiple items, multiple common couplets, find most common",
			itemLabels: [][]string{
				{"bug", "feature"},
				{"bug", "feature", "documentation"},
				{"feature", "enhancement"},
				{"feature", "enhancement", "ui"},
			},
			// (bug, feat): 2, (feat, enh): 2.
			// Sorted alpha: bug||feature < enhancement||feature
			expectedLabels: [][]string{{"bug", "feature"}, {"enhancement", "feature"}, {"bug", "documentation"}, {"documentation", "feature"}, {"enhancement", "ui"}, {"feature", "ui"}},
			expectedCount:  2,
		},
		{
			name:           "no labels in any item",
			itemLabels:     [][]string{{}, {}},
			expectedLabels: nil,
			expectedCount:  0,
		},
		{
			name: "labels with numbers and special characters",
			itemLabels: [][]string{
				{"v1.0", "bug-fix"},
				{"v1.0", "bug-fix"},
				{"bug-fix", "feature-2.0"},
			},
			// (bug-fix, v1.0): 2.
			expectedLabels: [][]string{{"bug-fix", "v1.0"}, {"bug-fix", "feature-2.0"}},
			expectedCount:  2,
		},
		{
			name: "More than two co-occurring labels, should pick two",
			itemLabels: [][]string{
				{"a", "b", "c"},
				{"a", "b", "d"},
			},
			// (a,b): 2. (a,c): 1, (b,c): 1, (a,d): 1, (b,d): 1
			expectedLabels: [][]string{{"a", "b"}, {"a", "c"}, {"a", "d"}, {"b", "c"}, {"b", "d"}},
			expectedCount:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels, count := findMostCommonCoOccurringLabels(tt.itemLabels)

			if count != tt.expectedCount {
				t.Errorf("Expected count %d, got %d", tt.expectedCount, count)
			}

			// Normalize labels for comparison (TopN limit applies, so we might check prefix)
			// The test cases above list ALL combinations in sorted order.
			// We should check if `labels` (which is TopN=5) matches the prefix of expected.

			// Check length
			expectedLen := len(tt.expectedLabels)
			if expectedLen > 5 {
				expectedLen = 5
			}
			if len(labels) != expectedLen {
				t.Errorf("Expected %d label sets, got %d", expectedLen, len(labels))
				return
			}

			for i := range labels {
				got := labels[i]
				sort.Strings(got)
				expected := tt.expectedLabels[i]
				sort.Strings(expected)
				if !reflect.DeepEqual(got, expected) {
					t.Errorf("Index %d: Expected %v, got %v", i, expected, got)
				}
			}
		})
	}
}

func TestCombinations(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		k        int
		expected [][]string
	}{
		{
			name:     "empty input labels",
			labels:   []string{},
			k:        0,
			expected: [][]string{{}}, // Expect a single empty combination for k=0
		},
		{
			name:     "k is zero, non-empty labels",
			labels:   []string{"a", "b", "c"},
			k:        0,
			expected: [][]string{{}},
		},
		{
			name:     "k greater than labels length",
			labels:   []string{"a", "b"},
			k:        3,
			expected: nil,
		},
		{
			name:     "single element, k=1",
			labels:   []string{"a"},
			k:        1,
			expected: [][]string{{"a"}},
		},
		{
			name:     "two elements, k=1",
			labels:   []string{"a", "b"},
			k:        1,
			expected: [][]string{{"a"}, {"b"}},
		},
		{
			name:     "two elements, k=2",
			labels:   []string{"a", "b"},
			k:        2,
			expected: [][]string{{"a", "b"}},
		},
		{
			name:     "three elements, k=2",
			labels:   []string{"a", "b", "c"},
			k:        2,
			expected: [][]string{{"a", "b"}, {"a", "c"}, {"b", "c"}},
		},
		{
			name:     "three elements, k=3",
			labels:   []string{"a", "b", "c"},
			k:        3,
			expected: [][]string{{"a", "b", "c"}},
		},
		{
			name:     "four elements, k=2",
			labels:   []string{"a", "b", "c", "d"},
			k:        2,
			expected: [][]string{{"a", "b"}, {"a", "c"}, {"a", "d"}, {"b", "c"}, {"b", "d"}, {"c", "d"}},
		},
		{
			name:     "labels with duplicates (should treat as unique after sorting if done before calling combinations)",
			labels:   []string{"a", "a", "b"},
			k:        2,
			expected: [][]string{{"a", "a"}, {"a", "b"}, {"a", "b"}},
		}, {
			name:   "more complex example",
			labels: []string{"apple", "banana", "cherry", "date"},
			k:      3,
			expected: [][]string{
				{"apple", "banana", "cherry"},
				{"apple", "banana", "date"},
				{"apple", "cherry", "date"},
				{"banana", "cherry", "date"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := combinations(tt.labels, tt.k)

			// Sort inner combinations and the slice of combinations for consistent comparison
			for _, combo := range result {
				sort.Strings(combo)
			}
			sort.Slice(result, func(i, j int) bool {
				s1 := strings.Join(result[i], "")
				s2 := strings.Join(result[j], "")
				return s1 < s2
			})

			for _, combo := range tt.expected {
				sort.Strings(combo)
			}
			sort.Slice(tt.expected, func(i, j int) bool {
				s1 := strings.Join(tt.expected[i], "")
				s2 := strings.Join(tt.expected[j], "")
				return s1 < s2
			})

			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("For labels %v and k=%d, expected %v, got %v", tt.labels, tt.k, tt.expected, result)
			}
		})
	}
}
