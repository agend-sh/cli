package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/agend-sh/cli/internal/api"
	"github.com/agend-sh/cli/internal/auth"
	agentgrpc "github.com/agend-sh/cli/internal/grpc"
)

var environmentIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func validateEnvironmentID(id string) error {
	if !environmentIDPattern.MatchString(id) {
		return fmt.Errorf("expected an environment ID from 'agend env list'")
	}
	return nil
}

func requireEnvironment(cmd *cobra.Command) {
	name, args, _ := strings.Cut(cmd.Use, " ")
	cmd.Use = strings.TrimSpace(name + " --env <env-id> " + args)
	cmd.Flags().String("env", "", "environment ID (see agend env list)")
	_ = cmd.MarkFlagRequired("env")
}

func environmentArg(cmd *cobra.Command, args []string) error {
	if err := cobra.ExactArgs(1)(cmd, args); err != nil {
		return err
	}
	return validateEnvironmentID(args[0])
}

type environmentTarget struct {
	id                        string
	api                       *api.Client
	endpoint, secret, session string
}

func (t *environmentTarget) status(ctx context.Context) (*api.EnvStatusResponse, error) {
	env, err := t.api.GetEnvironmentContext(ctx, t.id)
	if err != nil {
		return nil, err
	}
	if env.EnvID != "" && env.EnvID != t.id {
		return nil, fmt.Errorf("status returned environment %q, expected %q", env.EnvID, t.id)
	}
	return env, nil
}

func (t *environmentTarget) resolve(ctx context.Context) error {
	env, err := t.status(ctx)
	if err != nil {
		// Directly connected workers can remain reachable while the control
		// plane is offline. Only a dial failure permits using cached authority.
		var failure *net.OpError
		if ctx.Err() == nil && errors.As(err, &failure) && failure.Op == "dial" {
			_, endpoint, secret, session, loadErr := auth.LoadEnvironment(t.id)
			if loadErr == nil && endpoint != "" && (secret != "" || session != "") {
				t.endpoint, t.secret, t.session = endpoint, secret, session
				return nil
			}
		}
		return err
	}
	switch env.State {
	case "running":
		if env.Endpoint == "" {
			return fmt.Errorf("environment %s is running without an endpoint", t.id)
		}
		t.endpoint = env.Endpoint
		secret, session, err := auth.EnvironmentAuth(t.id, env.Endpoint, env.Secret)
		if err != nil {
			return err
		}
		if secret != "" || session != "" {
			t.secret, t.session = secret, session
			return nil
		}
		return t.reauth(ctx)
	case "sleeping":
		wake, err := t.api.WakeEnvironmentContext(ctx, t.id)
		if err != nil {
			return fmt.Errorf("wake environment: %w", err)
		}
		if wake.EnvID != t.id || wake.Secret == "" {
			return fmt.Errorf("wake returned invalid credentials for environment %q", t.id)
		}
		t.secret, t.endpoint = wake.Secret, wake.Endpoint
		for i := 0; t.endpoint == "" && i < 30; i++ {
			if err := waitForRetry(ctx, time.Second); err != nil {
				return err
			}
			current, err := t.api.GetEnvironmentContext(ctx, t.id)
			if err != nil {
				continue // A transient status failure can clear while the tunnel starts.
			}
			if current.EnvID != "" && current.EnvID != t.id {
				return fmt.Errorf("status returned environment %q, expected %q", current.EnvID, t.id)
			}
			if current.State == "running" {
				t.endpoint = current.Endpoint
			}
		}
		if t.endpoint == "" {
			return fmt.Errorf("environment %s woke without an endpoint", t.id)
		}
		return auth.SaveEnvironment(t.id, t.endpoint, t.secret)
	default:
		return fmt.Errorf("cannot select environment %s from state %s", t.id, env.State)
	}
}

func (t *environmentTarget) refresh(ctx context.Context) error {
	env, err := t.status(ctx)
	if err != nil {
		return err
	}
	if env.Endpoint == "" {
		return fmt.Errorf("environment %s has no endpoint (state=%s)", t.id, env.State)
	}
	t.endpoint = env.Endpoint
	return nil
}

func (t *environmentTarget) reauth(ctx context.Context) error {
	resp, err := t.api.ReauthEnvironmentContext(ctx, t.id)
	if err != nil {
		return err
	}
	if resp.EnvID != t.id || resp.Secret == "" {
		return fmt.Errorf("reauth returned invalid credentials for environment %q", t.id)
	}
	t.secret, t.session = resp.Secret, ""
	return auth.SaveEnvironment(t.id, t.endpoint, t.secret)
}

func (t *environmentTarget) dial(ctx context.Context) (*agentgrpc.Client, error) {
	_, _, secret, session, err := auth.LoadEnvironment(t.id)
	if err != nil {
		return nil, err
	}
	if secret == "" && session == "" {
		secret, session = t.secret, t.session
	}
	client, err := agentgrpc.Dial(ctx, t.endpoint, secret, session)
	if err != nil {
		return nil, err
	}
	client.OnTokenReceived = func(token string) {
		_, _ = auth.SaveSessionTokenForEnvironment(t.id, secret, session, token)
	}
	return client, nil
}
