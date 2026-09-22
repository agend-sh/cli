package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

const connectStdinChunk = 4096

const connectHeartbeatInterval = 60 * time.Second

type connectRequestSender struct {
	ctx context.Context
	out chan<- *pb.ConnectRequest
}

func (s connectRequestSender) Resize(ctx context.Context, columns, rows uint32) error {
	request := &pb.ConnectRequest{Payload: &pb.ConnectRequest_Resize{
		Resize: &pb.ResizeRequest{Columns: columns, Rows: rows},
	}}
	select {
	case s.out <- request:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

type connectExitError struct{ code int }

func (e connectExitError) Error() string {
	return fmt.Sprintf("remote shell exited with code %d", e.code)
}
func (e connectExitError) ExitCode() int { return e.code }

func newConnectCmd() *cobra.Command {
	var addr string
	var shell string

	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Open an interactive shell to the remote environment",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			return callWithRetry(ctx, cmd, addr, false, func(client *agentgrpc.Client) error {
				return runConnectSession(ctx, cmd, client, shell)
			})
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051", "agentd address")
	cmd.Flags().StringVar(&shell, "shell", "bash", "remote shell command to launch")
	return cmd
}

func runConnectSession(ctx context.Context, cmd *cobra.Command, client *agentgrpc.Client, shell string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("connect requires an interactive terminal on stdin")
	}

	columns, rows, _ := normalizedTerminalSize(stdoutTerminalSize)
	stream, err := client.Agent.Connect(ctx)
	if err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("enable raw terminal mode: %w", err)
	}
	defer func() { _ = term.Restore(fd, oldState) }()

	if err := stream.Send(&pb.ConnectRequest{Payload: &pb.ConnectRequest_Start{
		Start: &pb.ConnectStart{Command: shell, Columns: columns, Rows: rows},
	}}); err != nil {
		return fmt.Errorf("connect start failed: %w", err)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	requests := make(chan *pb.ConnectRequest, 64)
	closeSend := make(chan struct{})
	senderDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(connectHeartbeatInterval)
		defer ticker.Stop()
		defer close(senderDone)
		for {
			select {
			case request := <-requests:
				if err := stream.Send(request); err != nil {
					senderDone <- err
					return
				}
			case <-ticker.C:
				if err := stream.Send(&pb.ConnectRequest{Payload: &pb.ConnectRequest_Keepalive{
					Keepalive: &pb.ConnectKeepalive{},
				}}); err != nil {
					senderDone <- err
					return
				}
			case <-closeSend:
				if err := stream.CloseSend(); err != nil {
					senderDone <- err
					return
				}
				senderDone <- nil
				return
			case <-sessionCtx.Done():
				return
			}
		}
	}()

	stopResize := startTerminalResizeForwarding(sessionCtx, connectRequestSender{ctx: sessionCtx, out: requests}, cmd.ErrOrStderr())
	defer stopResize()

	responses := make(chan connectResponseResult, 1)
	go receiveConnectResponses(stream, responses)
	var sanitizer connectStreamSanitizer

	stdinDone := make(chan error, 1)
	go readConnectStdin(sessionCtx, requests, stdinDone)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	for {
		select {
		case result := <-responses:
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return nil
				}
				return fmt.Errorf("connect: %w", result.err)
			}
			if output := sanitizer.sanitize(string(result.response.GetOutput())); output != "" {
				if _, err := os.Stdout.WriteString(output); err != nil {
					return fmt.Errorf("connect: write terminal: %w", err)
				}
			}
			if result.response.GetStatus() == "completed" {
				if code := int(result.response.GetExitCode()); code != 0 {
					return connectExitError{code: code}
				}
				return nil
			}
			if result.response.GetStatus() == "interrupted" {
				return connectExitError{code: 130}
			}

		case err, ok := <-senderDone:
			if !ok {
				return nil
			}
			if err != nil {
				return fmt.Errorf("connect send: %w", err)
			}
			return nil

		case err := <-stdinDone:
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("connect: read stdin: %w", err)
			}
			// Half-close the request side. The server interrupts and cleans up
			// the PTY when it observes EOF, while still being able to finish
			// any response already in flight.
			close(closeSend)
			if err := <-senderDone; err != nil {
				return fmt.Errorf("connect: close input: %w", err)
			}
			senderDone = nil
			stdinDone = nil

		case <-sigCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type connectResponseResult struct {
	response *pb.ConnectResponse
	err      error
}

func receiveConnectResponses(stream pb.AgentService_ConnectClient, responses chan<- connectResponseResult) {
	for {
		response, err := stream.Recv()
		if err != nil {
			responses <- connectResponseResult{err: err}
			return
		}
		responses <- connectResponseResult{response: response}
	}
}

func readConnectStdin(ctx context.Context, requests chan<- *pb.ConnectRequest, done chan<- error) {
	buffer := make([]byte, connectStdinChunk)
	for {
		read, err := os.Stdin.Read(buffer)
		if read > 0 {
			request := &pb.ConnectRequest{Payload: &pb.ConnectRequest_Input{
				Input: append([]byte(nil), buffer[:read]...),
			}}
			select {
			case requests <- request:
			case <-ctx.Done():
				done <- ctx.Err()
				return
			}
		}
		if err != nil {
			done <- err
			return
		}
	}
}
