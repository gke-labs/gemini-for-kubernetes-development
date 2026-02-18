package sandbox

import (
	"testing"
)

func TestNewAgentSandbox(t *testing.T) {
	tests := []struct {
		name        string
		dindSupport string
	}{
		{
			name:        "DindSupportNone",
			dindSupport: DindSupportNone,
		},
		{
			name:        "DindSupportEmpty",
			dindSupport: "",
		},
		{
			name:        "DindSupportGvisor",
			dindSupport: DindSupportGvisor,
		},
		{
			name:        "DindSupportPrivileged",
			dindSupport: DindSupportPrivileged,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt := AgentSandboxOptions{
				DevSandboxOptions: DevSandboxOptions{
					Name:      "test",
					Namespace: "default",
				},
				DindSupport: tt.dindSupport,
			}
			sandbox, _ := NewAgentSandbox(opt)

			spec := sandbox.Object["spec"].(map[string]interface{})
			podTemplate := spec["podTemplate"].(map[string]interface{})
			podSpec := podTemplate["spec"].(map[string]interface{})

			// Check runtimeClassName
			runtimeClassName := podSpec["runtimeClassName"]
			if tt.dindSupport == DindSupportGvisor {
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
			isDind := tt.dindSupport == DindSupportGvisor || tt.dindSupport == DindSupportPrivileged
			if isDind {
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

			if tt.dindSupport == DindSupportPrivileged {
				if securityContext["privileged"] != true {
					t.Errorf("expected privileged true, got %v", securityContext["privileged"])
				}
			} else if tt.dindSupport == DindSupportGvisor {
				if securityContext["privileged"] != nil {
					t.Errorf("expected privileged nil, got %v", securityContext["privileged"])
				}
				capabilities := securityContext["capabilities"].(map[string]interface{})
				add := capabilities["add"].([]interface{})
				if len(add) == 0 {
					t.Errorf("expected capabilities.add to be non-empty")
				}
			} else {
				if securityContext["privileged"] != nil {
					t.Errorf("expected privileged nil, got %v", securityContext["privileged"])
				}
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
			if isDind != hasDockerVolume {
				t.Errorf("expected hasDockerVolume %v, got %v", isDind, hasDockerVolume)
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
			if isDind != hasDockerVolumeMount {
				t.Errorf("expected hasDockerVolumeMount %v, got %v", isDind, hasDockerVolumeMount)
			}

			// Check DIND_SUPPORT env var
			env := container["env"].([]interface{})
			foundDindEnv := false
			for _, e := range env {
				envVar := e.(map[string]interface{})
				if envVar["name"] == "DIND_SUPPORT" {
					foundDindEnv = true
					if envVar["value"] != tt.dindSupport {
						t.Errorf("expected DIND_SUPPORT env %v, got %v", tt.dindSupport, envVar["value"])
					}
					break
				}
			}
			if !foundDindEnv {
				t.Errorf("DIND_SUPPORT env var not found")
			}

			// Check Go cache and TMPDIR env vars
			expectedEnv := map[string]string{
				"GOCACHE":    "/workspaces/.cache/go-build",
				"GOMODCACHE": "/workspaces/.cache/mod",
				"TMPDIR":     "/workspaces/.tmp",
			}
			for name, val := range expectedEnv {
				found := false
				for _, e := range env {
					envVar := e.(map[string]interface{})
					if envVar["name"] == name {
						found = true
						if envVar["value"] != val {
							t.Errorf("expected %s env %v, got %v", name, val, envVar["value"])
						}
						break
					}
				}
				if !found {
					t.Errorf("%s env var not found", name)
				}
			}
		})
	}
}
