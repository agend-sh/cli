package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
	"google.golang.org/grpc"
)

func TestFormatExecResponseIncludesInputWaitOnlyForInteractiveCalls(t *testing.T) {
	response := &pb.ExecResponse{Status: "awaiting_input", InputWait: true}
	if got := formatExecResponse(response, true); got != "status: awaiting_input\ninput_wait: true" {
		t.Fatalf("interactive formatExecResponse() = %q", got)
	}
	if got := formatExecResponse(response, false); got != "status: awaiting_input" {
		t.Fatalf("noninteractive formatExecResponse() = %q", got)
	}
	if got := formatExecResponse(&pb.ExecResponse{Status: "session_active"}, true); got != "status: session_active" {
		t.Fatalf("conflicting-session formatExecResponse() = %q", got)
	}
}

func TestFormatInputResponsesExposeLatestInputWaitValue(t *testing.T) {
	input := &pb.InputResponse{Status: "awaiting_input", Stdout: "next> ", InputWait: true}
	if got := formatInputResponse(input); got != "status: awaiting_input\nnext> \ninput_wait: true" {
		t.Fatalf("formatInputResponse() = %q", got)
	}

	raw := &pb.RawInputResponse{Status: "awaiting_input", Screen: "next> ", InputWait: false}
	if got := formatRawInputResponse(raw); got != "status: awaiting_input\nnext> \ninput_wait: false" {
		t.Fatalf("formatRawInputResponse() = %q", got)
	}
}

type inputWaitAgentClient struct {
	pb.AgentServiceClient
	execResponse  *pb.ExecResponse
	inputResponse *pb.InputResponse
	rawResponse   *pb.RawInputResponse
}

func (f *inputWaitAgentClient) Exec(context.Context, *pb.ExecRequest, ...grpc.CallOption) (*pb.ExecResponse, error) {
	return f.execResponse, nil
}

func (f *inputWaitAgentClient) Input(context.Context, *pb.InputRequest, ...grpc.CallOption) (*pb.InputResponse, error) {
	return f.inputResponse, nil
}

func (f *inputWaitAgentClient) RawInput(context.Context, *pb.RawInputRequest, ...grpc.CallOption) (*pb.RawInputResponse, error) {
	return f.rawResponse, nil
}

func TestInteractiveMCPCallsSerializeInputWaitForTheAgent(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]any
		wantText string
	}{
		{
			name:     "shell_exec",
			args:     map[string]any{"command": "python3", "interactive": true},
			wantText: "status: awaiting_input\ninput_wait: true",
		},
		{
			name:     "shell_provide_input",
			args:     map[string]any{"input": "yes"},
			wantText: "status: awaiting_input\ninput_wait: false",
		},
		{
			name:     "shell_send_raw",
			args:     map[string]any{"input": "print(1)\\n"},
			wantText: "status: awaiting_input\nnext> \ninput_wait: true",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			agent := &inputWaitAgentClient{
				execResponse:  &pb.ExecResponse{Status: "awaiting_input", InputWait: true},
				inputResponse: &pb.InputResponse{Status: "awaiting_input", InputWait: false},
				rawResponse:   &pb.RawInputResponse{Status: "awaiting_input", Screen: "next> ", InputWait: true},
			}
			conn := &EnvConn{
				envID:  "env-test",
				state:  StateConnected,
				client: &agentgrpc.Client{Agent: agent},
			}
			pool := &ConnPool{
				conns: map[string]*EnvConn{"env-test": conn},
				ctx:   context.Background(),
			}
			server := &Server{pool: pool, writer: &output, inflight: make(map[string]context.CancelFunc)}
			// Keep this test local: a successful tools/call must not emit telemetry.
			server.funnelOnce.Do(func() {})

			params, err := json.Marshal(map[string]any{
				"name": test.name,
				"arguments": map[string]any{
					"environment": "env-test",
					"command":     test.args["command"],
					"interactive": test.args["interactive"],
					"input":       test.args["input"],
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			server.handleRequest(context.Background(), &jsonrpcRequest{
				JSONRPC: "2.0", ID: json.RawMessage("41"), Method: "tools/call", Params: params,
			})

			var response struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Result  struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			if err := json.Unmarshal(output.Bytes(), &response); err != nil {
				t.Fatalf("decode MCP response %q: %v", output.String(), err)
			}
			if response.JSONRPC != "2.0" || string(response.ID) != "41" || response.Result.IsError || len(response.Result.Content) != 1 {
				t.Fatalf("MCP response envelope = %#v", response)
			}
			if got := response.Result.Content[0].Text; got != test.wantText {
				t.Fatalf("model-visible text = %q, want %q", got, test.wantText)
			}
		})
	}
}
