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
