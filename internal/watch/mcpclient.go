package watch

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// MCPClient speaks JSON-RPC over stdio to a local MCP server process.
type MCPClient struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	mu      sync.Mutex
	nextID  atomic.Int64

	// pending tracks in-flight requests by ID.
	pending map[int64]chan json.RawMessage
	pmu     sync.Mutex
}

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      *int64      `json:"id,omitempty"` // nil for notifications
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// NewMCPClient starts an MCP server process and initializes the protocol.
func NewMCPClient(ctx context.Context, command string, args ...string) (*MCPClient, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// Pipe stderr to /dev/null via an open file to prevent buffer blocking.
	devNull, _ := os.Open(os.DevNull)
	cmd.Stderr = devNull

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start MCP server: %w", err)
	}

	c := &MCPClient{
		cmd:     cmd,
		stdin:   stdin,
		scanner: bufio.NewScanner(stdout),
		pending: make(map[int64]chan json.RawMessage),
	}
	// Increase scanner buffer for large responses
	c.scanner.Buffer(make([]byte, 0), 10*1024*1024)

	// Start background reader
	go c.readLoop()

	// Initialize MCP protocol
	if err := c.initialize(); err != nil {
		c.Close()
		return nil, fmt.Errorf("MCP initialize: %w", err)
	}

	return c, nil
}

func (c *MCPClient) initialize() error {
	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "jarvis-watch",
			"version": "1.0",
		},
	}

	var result json.RawMessage
	if err := c.call("initialize", initParams, &result); err != nil {
		return err
	}

	// Send initialized notification
	return c.notify("notifications/initialized", nil)
}

// CallTool invokes an MCP tool and returns the text content.
func (c *MCPClient) CallTool(name string, arguments interface{}) (string, error) {
	argsJSON, err := json.Marshal(arguments)
	if err != nil {
		return "", fmt.Errorf("marshal arguments: %w", err)
	}

	params := mcpToolCallParams{
		Name:      name,
		Arguments: argsJSON,
	}

	var result mcpToolResult
	if err := c.call("tools/call", params, &result); err != nil {
		return "", err
	}

	for _, content := range result.Content {
		if content.Type == "text" {
			return content.Text, nil
		}
	}
	return "", nil
}

func (c *MCPClient) call(method string, params interface{}, result interface{}) error {
	id := c.nextID.Add(1)

	ch := make(chan json.RawMessage, 1)
	c.pmu.Lock()
	c.pending[id] = ch
	c.pmu.Unlock()

	defer func() {
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
	}()

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}

	c.mu.Lock()
	data, _ := json.Marshal(req)
	_, err := c.stdin.Write(append(data, '\n'))
	c.mu.Unlock()

	if err != nil {
		return fmt.Errorf("write request: %w", err)
	}

	// Wait for response with timeout.
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	var raw json.RawMessage
	var ok bool
	select {
	case raw, ok = <-ch:
		if !ok {
			return fmt.Errorf("connection closed")
		}
	case <-timer.C:
		return fmt.Errorf("timeout waiting for response to %s", method)
	}

	if result != nil {
		return json.Unmarshal(raw, result)
	}
	return nil
}

func (c *MCPClient) notify(method string, params interface{}) error {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	data, _ := json.Marshal(req)
	_, err := c.stdin.Write(append(data, '\n'))
	return err
}

func (c *MCPClient) readLoop() {
	for c.scanner.Scan() {
		var resp jsonRPCResponse
		if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
			continue
		}
		if resp.ID == nil {
			continue // notification, ignore
		}

		c.pmu.Lock()
		ch, ok := c.pending[*resp.ID]
		c.pmu.Unlock()

		if !ok {
			continue
		}

		if resp.Error != nil {
			// Send error as a special JSON message
			errJSON, _ := json.Marshal(map[string]string{"error": resp.Error.Message})
			ch <- errJSON
		} else {
			ch <- resp.Result
		}
	}

	// Close all pending channels
	c.pmu.Lock()
	for _, ch := range c.pending {
		close(ch)
	}
	c.pmu.Unlock()
}

// Close stops the MCP server process.
func (c *MCPClient) Close() error {
	c.stdin.Close()
	return c.cmd.Wait()
}
