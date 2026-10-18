// Command upstream-conformance drives the portal's upstream MCP client
// (internal/upstream) for the MCP project's conformance suite, which tests
// clients by running a command against a scenario's server:
//
//	go build -o /tmp/omp-upstream-conformance ./cmd/upstream-conformance
//	npx @modelcontextprotocol/conformance client --command /tmp/omp-upstream-conformance \
//	    --scenario http-standard-headers --spec-version 2026-07-28
//
// The server's URL is the last argument; the scenario is MCP_CONFORMANCE_SCENARIO
// and its context MCP_CONFORMANCE_CONTEXT. The command negotiates the era,
// lists the server's capabilities and calls its tools the way the portal
// does for a gateway (parameter headers from the listed definitions, input
// requests answered by accepting them and echoing the server's requestState).
// It is a development tool, not part of the portal.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ncecere/open-mcp-portal/internal/upstream"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: upstream-conformance <server URL>")
		os.Exit(2)
	}
	url := os.Args[len(os.Args)-1]
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	c := &upstream.Client{AllowPrivate: true, Eras: upstream.NewEraCache(), Log: slog.New(slog.DiscardHandler)}
	t := upstream.Target{URL: url, ServerID: "conformance", Timeout: 20 * time.Second}

	d, err := c.Discover(ctx, t)
	if err != nil {
		fmt.Fprintln(os.Stderr, "discover:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "era %s %s, %d capabilities\n", d.Protocol.Era, d.Protocol.Version, len(d.Capabilities))
	byName := map[string]upstream.Capability{}
	for _, cp := range d.Capabilities {
		if cp.Kind == upstream.KindTool {
			byName[cp.Name] = cp
		}
	}

	type call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	var scenario struct {
		ToolCalls []call `json:"toolCalls"`
	}
	_ = json.Unmarshal([]byte(os.Getenv("MCP_CONFORMANCE_CONTEXT")), &scenario)
	calls := scenario.ToolCalls
	if len(calls) == 0 {
		// Call every tool the server lists, with sample arguments.
		for name, cp := range byName {
			calls = append(calls, call{Name: name, Arguments: sampleArguments(name, cp.Definition, byName)})
		}
	}
	for _, cl := range calls {
		cp, ok := byName[cl.Name]
		if !ok {
			continue
		}
		args, _ := json.Marshal(cl.Arguments)
		tt := t
		tt.ParamHeaders = upstream.ParamHeaders(cp.Definition, args)
		req := upstream.ForwardRequest{Method: upstream.MethodToolsCall, Name: cl.Name, Arguments: args, Elicitation: json.RawMessage(`{"form":{},"url":{}}`)}
		for round := 0; round < 5; round++ {
			res, err := c.Forward(ctx, tt, req)
			if err != nil {
				fmt.Fprintln(os.Stderr, "call", cl.Name+":", err)
				break
			}
			if res.Input == nil {
				fmt.Fprintln(os.Stderr, "call", cl.Name+": ok")
				break
			}
			// Accept every input request and echo the server's state.
			var requests map[string]json.RawMessage
			_ = json.Unmarshal(res.Input.Requests, &requests)
			answers := map[string]any{}
			for k := range requests {
				answers[k] = map[string]any{"action": "accept", "content": map[string]any{}}
			}
			req.InputResponses, _ = json.Marshal(answers)
			req.RequestState = res.Input.RequestState
		}
	}
}

// sampleArguments fills a tool's required arguments: numbers 5 and 3, text
// "test", booleans true; json_schema_echo gets the input schema of the other
// tool the scenario lists, as the client stored it.
func sampleArguments(name string, definition json.RawMessage, tools map[string]upstream.Capability) map[string]any {
	var def struct {
		InputSchema struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"inputSchema"`
	}
	args := map[string]any{}
	if json.Unmarshal(definition, &def) != nil {
		return args
	}
	if name == "json_schema_echo" {
		for other, cp := range tools {
			if other == name {
				continue
			}
			var schema any
			_ = json.Unmarshal(cp.Definition, &struct {
				InputSchema *any `json:"inputSchema"`
			}{&schema})
			args["schema"] = schema
		}
		return args
	}
	n := 5
	for _, key := range def.InputSchema.Required {
		var p struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(def.InputSchema.Properties[key], &p)
		switch p.Type {
		case "number", "integer":
			args[key] = n
			n -= 2
		case "boolean":
			args[key] = true
		default:
			args[key] = "test"
		}
	}
	return args
}
