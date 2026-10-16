package upstream

import (
	"context"
	"encoding/json"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Forwarding a gateway's calls (docs/phase3-gateways.md, "Runtime"): one
// exchange per call, through the same guard, timeout and response bound as
// discovery. The gateway advertises no client capabilities, so a server
// can't ask it for sampling, elicitation or roots.

// CallTool calls a tool on the server. args is the client's arguments
// object (nil: none). A tool's own failure is a result with IsError set;
// failures to get a result are *Error.
func (c *Client) CallTool(ctx context.Context, t Target, name string, args json.RawMessage) (*mcp.CallToolResult, time.Duration, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()
	sess, err := c.open(ctx, t)
	if err != nil {
		return nil, time.Since(start), err
	}
	defer sess.close()
	var arguments any = map[string]any{}
	if len(args) > 0 && string(args) != "null" {
		arguments = args
	}
	res, err := sess.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, time.Since(start), c.classify(ctx, t, sess.ex, err)
	}
	if res.Content == nil {
		res.Content = []mcp.Content{}
	}
	return res, time.Since(start), nil
}

// GetPrompt gets a prompt from the server with the client's arguments.
func (c *Client) GetPrompt(ctx context.Context, t Target, name string, args map[string]string) (*mcp.GetPromptResult, time.Duration, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()
	sess, err := c.open(ctx, t)
	if err != nil {
		return nil, time.Since(start), err
	}
	defer sess.close()
	res, err := sess.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: name, Arguments: args})
	if err != nil {
		return nil, time.Since(start), c.classify(ctx, t, sess.ex, err)
	}
	if res.Messages == nil {
		res.Messages = []*mcp.PromptMessage{}
	}
	return res, time.Since(start), nil
}
