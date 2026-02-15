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
			if runtimeClassName != nil {
				t.Errorf("expected runtimeClassName nil, got %v", runtimeClassName)
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
				if securityContext["privileged"] != true {
					t.Errorf("expected privileged true, got %v", securityContext["privileged"])
				}
			} else {
				if securityContext["privileged"] != nil {
					t.Errorf("expected privileged nil, got %v", securityContext["privileged"])
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
