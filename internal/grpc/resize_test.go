package grpc

import (
	"context"
	"testing"

	gogrpc "google.golang.org/grpc"

	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

type resizeAgentClient struct {
	pb.AgentServiceClient
	request *pb.ResizeRequest
	calls   int
}

func (f *resizeAgentClient) Resize(_ context.Context, request *pb.ResizeRequest, _ ...gogrpc.CallOption) (*pb.ResizeResponse, error) {
	f.calls++
	f.request = request
	return &pb.ResizeResponse{}, nil
}

func TestClientResizeValidatesAndForwards(t *testing.T) {
	agent := &resizeAgentClient{}
	client := &Client{Agent: agent}

	if err := client.Resize(context.Background(), 132, 43); err != nil {
		t.Fatal(err)
	}
	if agent.calls != 1 || agent.request.GetColumns() != 132 || agent.request.GetRows() != 43 {
		t.Fatalf("resize request = %#v, calls=%d", agent.request, agent.calls)
	}
	for _, dimensions := range [][2]uint32{{0, 24}, {80, 0}, {MaxPTYDimension + 1, 24}, {80, MaxPTYDimension + 1}} {
		if err := client.Resize(context.Background(), dimensions[0], dimensions[1]); err == nil {
			t.Fatalf("Resize(%d, %d) accepted", dimensions[0], dimensions[1])
		}
	}
	if agent.calls != 1 {
		t.Fatalf("invalid dimensions reached RPC client: %d calls", agent.calls)
	}
}
