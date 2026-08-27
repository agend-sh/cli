package mcp

import "testing"

func TestRemoteMCPToolActivationBoundary(t *testing.T) {
	for _, tool := range []string{"shell_exec", "file_write", "port_expose", "env_stats"} {
		if !isRemoteMCPTool(tool) {
			t.Errorf("isRemoteMCPTool(%q) = false, want true", tool)
		}
	}
	for _, tool := range []string{"list_environments", "env_create", "env_status", "env_wake", "env_cold_reset", "reload_config"} {
		if isRemoteMCPTool(tool) {
			t.Errorf("isRemoteMCPTool(%q) = true, want false", tool)
		}
	}
}
