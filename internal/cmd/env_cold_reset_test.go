package cmd

import (
	"strings"
	"testing"
)

func TestEnvColdResetRequiresAuditableReason(t *testing.T) {
	command := newEnvColdResetCmd()
	command.SetArgs([]string{"env-123"})
	command.SilenceUsage = true
	command.SilenceErrors = true

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), `required flag(s) "reason" not set`) {
		t.Fatalf("error = %v, want required reason flag", err)
	}
}

func TestEnvColdResetAcceptsAtMostOneEnvironment(t *testing.T) {
	command := newEnvColdResetCmd()
	command.SetArgs([]string{"env-1", "env-2", "--reason", "guest is stuck"})
	command.SilenceUsage = true
	command.SilenceErrors = true

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "accepts at most 1 arg") {
		t.Fatalf("error = %v, want maximum-argument error", err)
	}
}
