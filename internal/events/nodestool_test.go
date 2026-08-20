package events

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeLister struct {
	nodes []Node
}

func (f *fakeLister) Nodes() []Node { return f.nodes }

func TestNodesToolListsNodes(t *testing.T) {
	lister := &fakeLister{nodes: []Node{
		{Name: "node-a", Address: "10.0.0.1:8848", Self: true},
		{Name: "node-b", Address: "10.0.0.2:8848"},
	}}
	tool, err := NewNodesTool(lister)
	if err != nil {
		t.Fatalf("NewNodesTool: %v", err)
	}
	if tool.Spec().Name != "ListNodes" {
		t.Fatalf("spec name = %q", tool.Spec().Name)
	}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var nodes []Node
	if err := json.Unmarshal([]byte(result.Content), &nodes); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(nodes) != 2 || nodes[0].Name != "node-a" || nodes[1].Name != "node-b" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if !nodes[0].Self || nodes[1].Self {
		t.Fatalf("self flags = %+v", nodes)
	}
}
