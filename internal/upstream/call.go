package upstream

import (
	"context"
	"encoding/json"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The simple forms of Forward (forward.go): a call that declares no
// capability and carries no progress or trace context. A server's
// input_required answer is an error here: nothing can answer it.

var errInputWithoutCapability = &Error{Class: ClassBadResponse, Message: "The server asked for input, which this call can't give."}

// CallTool calls a tool on the server. args is the client's arguments
// object (nil: none). A tool's own failure is a result with IsError set;
// failures to get a result are *Error.
func (c *Client) CallTool(ctx context.Context, t Target, name string, args json.RawMessage) (*mcp.CallToolResult, time.Duration, error) {
	res, err := c.Forward(ctx, t, ForwardRequest{Method: MethodToolsCall, Name: name, Arguments: args})
	if err != nil {
		return nil, 0, err
	}
	if res.Input != nil {
		return nil, res.Latency, errInputWithoutCapability
	}
	return res.Tool, res.Latency, nil
}

// ReadResource reads a resource (by its URI on the server).
func (c *Client) ReadResource(ctx context.Context, t Target, uri string) (*mcp.ReadResourceResult, time.Duration, error) {
	res, err := c.Forward(ctx, t, ForwardRequest{Method: MethodResourcesRead, Name: uri})
	if err != nil {
		return nil, 0, err
	}
	if res.Input != nil {
		return nil, res.Latency, errInputWithoutCapability
	}
	return res.Read, res.Latency, nil
}

// GetPrompt gets a prompt from the server with the client's arguments.
func (c *Client) GetPrompt(ctx context.Context, t Target, name string, args map[string]string) (*mcp.GetPromptResult, time.Duration, error) {
	raw, _ := json.Marshal(args)
	res, err := c.Forward(ctx, t, ForwardRequest{Method: MethodPromptsGet, Name: name, Arguments: raw})
	if err != nil {
		return nil, 0, err
	}
	if res.Input != nil {
		return nil, res.Latency, errInputWithoutCapability
	}
	return res.Prompt, res.Latency, nil
}
