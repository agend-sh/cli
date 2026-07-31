package grpc

import (
	"context"
	"fmt"

	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

const MaxPTYDimension uint32 = 1000

// Resize changes the dimensions of the active remote PTY.
func (c *Client) Resize(ctx context.Context, columns, rows uint32) error {
	if columns == 0 || rows == 0 || columns > MaxPTYDimension || rows > MaxPTYDimension {
		return fmt.Errorf("columns and rows must be between 1 and %d", MaxPTYDimension)
	}
	if c == nil || c.Agent == nil {
		return fmt.Errorf("agent client is not connected")
	}
	if _, err := c.Agent.Resize(ctx, &pb.ResizeRequest{Columns: columns, Rows: rows}); err != nil {
		return fmt.Errorf("resize PTY: %w", err)
	}
	return nil
}
