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

package api

import (
	"reflect"
	"testing"
)

func TestFixYAMLIntegers(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want interface{}
	}{
		{
			name: "int to int64",
			in:   int(123),
			want: int64(123),
		},
		{
			name: "string remains string",
			in:   "hello",
			want: "hello",
		},
		{
			name: "map with int",
			in: map[string]interface{}{
				"foo": int(1),
				"bar": "baz",
			},
			want: map[string]interface{}{
				"foo": int64(1),
				"bar": "baz",
			},
		},
		{
			name: "slice with int",
			in:   []interface{}{int(1), "two", int(3)},
			want: []interface{}{int64(1), "two", int64(3)},
		},
		{
			name: "nested map and slice",
			in: map[string]interface{}{
				"list": []interface{}{
					map[string]interface{}{
						"val": int(10),
					},
					int(20),
				},
			},
			want: map[string]interface{}{
				"list": []interface{}{
					map[string]interface{}{
						"val": int64(10),
					},
					int64(20),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fixYAMLIntegers(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("fixYAMLIntegers() = %v, want %v", got, tt.want)
			}
		})
	}
}
