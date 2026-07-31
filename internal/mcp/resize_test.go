package mcp

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

type resizeAgentClient struct {
	pb.AgentServiceClient
	request *pb.ResizeRequest
	calls   int
}

func (f *resizeAgentClient) Resize(_ context.Context, request *pb.ResizeRequest, _ ...grpc.CallOption) (*pb.ResizeResponse, error) {
	f.calls++
	f.request = request
	return &pb.ResizeResponse{}, nil
}

func TestCallResizeValidatesAndForwards(t *testing.T) {
	agent := &resizeAgentClient{}
	client := &agentgrpc.Client{Agent: agent}
	result, isError := callResize(context.Background(), client, map[string]any{
		"columns": float64(132), "rows": "43",
	})
	if isError || result != "resized: 132x43" {
		t.Fatalf("callResize() = %q, isError=%v", result, isError)
	}
	if agent.calls != 1 || agent.request.GetColumns() != 132 || agent.request.GetRows() != 43 {
		t.Fatalf("resize request = %#v, calls=%d", agent.request, agent.calls)
	}

	for _, args := range []map[string]any{
		{"columns": 0.0, "rows": 24.0},
		{"columns": 80.5, "rows": 24.0},
		{"columns": 80.0, "rows": -1.0},
		{"columns": 1001.0, "rows": 24.0},
	} {
		if _, isError := callResize(context.Background(), client, args); !isError {
			t.Fatalf("invalid resize accepted: %#v", args)
		}
	}
	if agent.calls != 1 {
		t.Fatalf("invalid dimensions reached RPC client: %d calls", agent.calls)
	}
}

func TestShellResizeToolDefinition(t *testing.T) {
	for _, definition := range toolDefinitions() {
		if definition["name"] != "shell_resize" {
			continue
		}
		schema, ok := definition["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("input schema = %#v", definition["inputSchema"])
		}
		required, ok := schema["required"].([]string)
		if !ok || len(required) != 3 || required[0] != "environment" || required[1] != "columns" || required[2] != "rows" {
			t.Fatalf("required = %#v", schema["required"])
		}
		properties := schema["properties"].(map[string]any)
		for _, name := range []string{"columns", "rows"} {
			property := properties[name].(map[string]any)
			if property["minimum"] != 1 || property["maximum"] != 1000 {
				t.Fatalf("%s bounds = %#v", name, property)
			}
		}
		return
	}
	t.Fatal("shell_resize tool definition is missing")
}
