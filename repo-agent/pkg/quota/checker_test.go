// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package quota

import (
	"testing"
	"time"
)

func TestMidnightInTimezone(t *testing.T) {
	location, _ := time.LoadLocation("America/Los_Angeles")

	tests := []struct {
		name string
		t    time.Time
		want time.Time
	}{
		{
			name: "morning",
			t:    time.Date(2026, 5, 11, 10, 30, 45, 0, location),
			want: time.Date(2026, 5, 11, 0, 0, 0, 0, location),
		},
		{
			name: "afternoon",
			t:    time.Date(2026, 5, 11, 15, 0, 0, 0, location),
			want: time.Date(2026, 5, 11, 0, 0, 0, 0, location),
		},
		{
			name: "midnight",
			t:    time.Date(2026, 5, 11, 0, 0, 0, 0, location),
			want: time.Date(2026, 5, 11, 0, 0, 0, 0, location),
		},
		{
			name: "UTC time",
			t:    time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC),
			want: time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MidnightInTimezone(tt.t)
			if !got.Equal(tt.want) {
				t.Errorf("MidnightInTimezone() = %v, want %v", got, tt.want)
			}
			if got.Location() != tt.want.Location() {
				t.Errorf("MidnightInTimezone() location = %v, want %v", got.Location(), tt.want.Location())
			}
		})
	}
}
