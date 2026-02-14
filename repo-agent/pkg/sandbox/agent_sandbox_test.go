package sandbox

import (
	"testing"
)

func TestNewAgentSandbox(t *testing.T) {
	tests := []struct {
		name          string
		dockerEnabled bool
	}{
		{
			name:          "DockerDisabled",
			dockerEnabled: false,
		},
		{
			name:          "DockerEnabled",
			dockerEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt := AgentSandboxOptions{
				DevSandboxOptions: DevSandboxOptions{
					Name:      "test",
					Namespace: "default",
				},
				DockerEnabled: tt.dockerEnabled,
			}
			sandbox, _ := NewAgentSandbox(opt)

			spec := sandbox.Object["spec"].(map[string]interface{})
			podTemplate := spec["podTemplate"].(map[string]interface{})
			podSpec := podTemplate["spec"].(map[string]interface{})

			// Check runtimeClassName
			runtimeClassName := podSpec["runtimeClassName"]
			if tt.dockerEnabled {
				if runtimeClassName != "gvisor" {
					t.Errorf("expected runtimeClassName gvisor, got %v", runtimeClassName)
				}
			} else {
				if runtimeClassName != nil {
					t.Errorf("expected runtimeClassName nil, got %v", runtimeClassName)
				}
			}

			// Check dnsPolicy and dnsConfig
			dnsPolicy := podSpec["dnsPolicy"]
			dnsConfig := podSpec["dnsConfig"]
			if tt.dockerEnabled {
				if dnsPolicy != "None" {
					t.Errorf("expected dnsPolicy None, got %v", dnsPolicy)
				}
				if dnsConfig == nil {
					t.Errorf("expected dnsConfig not nil")
				}
			} else {
				if dnsPolicy != nil {
					t.Errorf("expected dnsPolicy nil, got %v", dnsPolicy)
				}
				if dnsConfig != nil {
					t.Errorf("expected dnsConfig nil, got %v", dnsConfig)
				}
			}

			// Check container securityContext
			containers := podSpec["containers"].([]interface{})
			container := containers[0].(map[string]interface{})
			securityContext := container["securityContext"].(map[string]interface{})

			if tt.dockerEnabled {
				capabilities := securityContext["capabilities"].(map[string]interface{})
				add := capabilities["add"].([]interface{})
				if len(add) == 0 {
					t.Errorf("expected capabilities.add to be non-empty")
				}
			} else {
				if securityContext["capabilities"] != nil {
					t.Errorf("expected capabilities nil, got %v", securityContext["capabilities"])
				}
			}

			// Check volumes
			volumes := podSpec["volumes"].([]interface{})
			hasDockerVolume := false
			for _, v := range volumes {
				vol := v.(map[string]interface{})
				if vol["name"] == "docker" {
					hasDockerVolume = true
					break
				}
			}
			if tt.dockerEnabled != hasDockerVolume {
				t.Errorf("expected hasDockerVolume %v, got %v", tt.dockerEnabled, hasDockerVolume)
			}

			// Check volumeMounts
			volumeMounts := container["volumeMounts"].([]interface{})
			hasDockerVolumeMount := false
			for _, vm := range volumeMounts {
				vmount := vm.(map[string]interface{})
				if vmount["name"] == "docker" {
					hasDockerVolumeMount = true
					break
				}
			}
			if tt.dockerEnabled != hasDockerVolumeMount {
				t.Errorf("expected hasDockerVolumeMount %v, got %v", tt.dockerEnabled, hasDockerVolumeMount)
			}
		})
	}
}
