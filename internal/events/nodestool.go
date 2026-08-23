package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lgxz/dora"
)

// NodesTool lets the model inspect the current memberlist cluster membership.
type NodesTool struct {
	lister NodesLister
}

// NodesLister returns the current node list.
type NodesLister interface {
	Nodes() []Node
}

// NewNodesTool creates a ListenNodes tool backed by lister.
func NewNodesTool(lister NodesLister) (*NodesTool, error) {
	if lister == nil {
		return nil, errors.New("events: node lister is nil")
	}
	return &NodesTool{lister: lister}, nil
}

// Spec implements dora.Tool.
func (t *NodesTool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        "list_nodes",
		Description: "List the nodes currently known to the memberlist cluster, each with its name and address. The `self` field is true for this node itself, so you can tell which node is you.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool.
func (t *NodesTool) Execute(ctx context.Context, raw json.RawMessage) (dora.ToolResult, error) {
	if t == nil || t.lister == nil {
		return dora.ToolResult{}, errors.New("events: node tool is not initialized")
	}
	nodes := t.lister.Nodes()
	encoded, err := json.Marshal(nodes)
	if err != nil {
		return dora.ToolResult{}, fmt.Errorf("events: encode nodes: %w", err)
	}
	return dora.ToolResult{Content: string(encoded)}, nil
}

var _ dora.Tool = (*NodesTool)(nil)
