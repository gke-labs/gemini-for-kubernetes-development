package models

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestIntOrString_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    int
		wantErr bool
	}{
		{
			name:    "int",
			data:    `10`,
			want:    10,
			wantErr: false,
		},
		{
			name:    "string int",
			data:    `"20"`,
			want:    20,
			wantErr: false,
		},
		{
			name:    "invalid string",
			data:    `"abc"`,
			wantErr: true,
		},
		{
			name:    "invalid type",
			data:    `true`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ios IntOrString
			if err := json.Unmarshal([]byte(tt.data), &ios); (err != nil) != tt.wantErr {
				t.Errorf("IntOrString.UnmarshalJSON() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && int(ios) != tt.want {
				t.Errorf("IntOrString.UnmarshalJSON() = %v, want %v", ios, tt.want)
			}
		})
	}
}

func TestIntOrString_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    int
		wantErr bool
	}{
		{
			name:    "int",
			data:    `10`,
			want:    10,
			wantErr: false,
		},
		{
			name:    "string int",
			data:    `"20"`,
			want:    20,
			wantErr: false,
		},
		{
			name:    "invalid string",
			data:    `abc`,
			wantErr: true,
		},
		{
			name:    "invalid type",
			data:    `[1, 2, 3]`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ios IntOrString
			if err := yaml.Unmarshal([]byte(tt.data), &ios); (err != nil) != tt.wantErr {
				t.Errorf("IntOrString.UnmarshalYAML() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && int(ios) != tt.want {
				t.Errorf("IntOrString.UnmarshalYAML() = %v, want %v", ios, tt.want)
			}
		})
	}
}
