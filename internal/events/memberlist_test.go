package events

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/hashicorp/memberlist"
)

// nextEvent returns the next non-membership event, skipping memberlist
// membership events that arrive as nodes join the test cluster.
func nextEvent(t *testing.T, ctx context.Context, transport *memberlistTransport) Event {
	t.Helper()
	for {
		ev, err := transport.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if ev.Type != TypeMemberlist {
			return ev
		}
	}
}

// freePort returns a free TCP port on localhost and its string form. Ports are
// bound then released so the test may reuse them; there is a small race window
// but it is acceptable for tests.
func freePort(t *testing.T) (string, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return l.Addr().String(), port
}

func TestMemberlistReceive(t *testing.T) {
	_, receiverPort := freePort(t)
	_, senderPort := freePort(t)

	receiver, err := newMemberlistTransport(MemberlistConfig{
		Bind:       net.JoinHostPort("127.0.0.1", strconv.Itoa(receiverPort)),
		Name:       "receiver-node",
		BufferSize: 64,
	})
	if err != nil {
		t.Fatalf("newMemberlistTransport receiver: %v", err)
	}
	defer receiver.Close()

	senderConf := memberlist.DefaultLANConfig()
	senderConf.Name = "sender-node"
	senderConf.BindAddr = "127.0.0.1"
	senderConf.BindPort = senderPort
	senderConf.LogOutput = discardWriter{}
	sender, err := memberlist.Create(senderConf)
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	defer sender.Shutdown()

	receiverAddr := receiver.list.LocalNode().Address()
	if _, err := sender.Join([]string{receiverAddr}); err != nil {
		t.Fatalf("sender join: %v", err)
	}

	// Wait for convergence.
	for i := 0; i < 100 && sender.NumMembers() < 2; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if n := sender.NumMembers(); n != 2 {
		t.Fatalf("expected 2 members, got %d", n)
	}

	encoded, err := encodeEvent(Event{ID: "evt-1", Type: "test", Sender: "sender", Msg: "hello", Meta: map[string]string{"n": "1"}})
	if err != nil {
		t.Fatalf("encodeEvent: %v", err)
	}
	// Target any other member (the receiver).
	var target *memberlist.Node
	for _, m := range sender.Members() {
		if m.Name != sender.LocalNode().Name {
			target = m
			break
		}
	}
	if target == nil {
		t.Fatal("no peer member found")
	}
	sender.SendReliable(target, encoded)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := nextEvent(t, ctx, receiver)
	if got.ID != "evt-1" || got.Type != "test" || got.Sender != "sender" || got.Msg != "hello" || got.Meta["n"] != "1" {
		t.Fatalf("unexpected event: %+v", got)
	}
}

func TestEventsSendBroadcastFillsSenderAndID(t *testing.T) {
	_, receiverPort := freePort(t)
	_, senderPort := freePort(t)

	receiver, err := New(Config{
		Enabled: true,
		Transports: Transports{
			Memberlist: MemberlistConfig{
				Bind: net.JoinHostPort("127.0.0.1", strconv.Itoa(receiverPort)),
				Name: "receiver-node",
			},
		},
	})
	if err != nil {
		t.Fatalf("New receiver: %v", err)
	}
	defer receiver.Close()

	sender, err := New(Config{
		Enabled: true,
		Transports: Transports{
			Memberlist: MemberlistConfig{
				Bind: net.JoinHostPort("127.0.0.1", strconv.Itoa(senderPort)),
				Name: "sender-node",
			},
		},
	})
	if err != nil {
		t.Fatalf("New sender: %v", err)
	}
	defer sender.Close()

	// Join the sender to the receiver's cluster.
	receiverAddr := receiver.transport.(*memberlistTransport).list.LocalNode().Address()
	senderList := sender.transport.(*memberlistTransport).list
	if _, err := senderList.Join([]string{receiverAddr}); err != nil {
		t.Fatalf("join: %v", err)
	}
	for i := 0; i < 100 && senderList.NumMembers() < 2; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if n := senderList.NumMembers(); n != 2 {
		t.Fatalf("expected 2 members, got %d", n)
	}

	// Broadcast an event with no sender/ID; the facade fills them.
	sent, err := sender.Send(Event{Type: "test", Msg: "broadcast"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sent.Sender != "sender-node" || sent.ID == "" {
		t.Fatalf("filled event = %+v", sent)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := nextEvent(t, ctx, receiver.transport.(*memberlistTransport))
	if got.Sender != "sender-node" || got.ID != sent.ID || got.Msg != "broadcast" {
		t.Fatalf("received event = %+v", got)
	}
}

func TestMemberlistJoinAndListNodes(t *testing.T) {
	_, firstPort := freePort(t)
	_, secondPort := freePort(t)

	first, err := New(Config{
		Enabled: true,
		Transports: Transports{
			Memberlist: MemberlistConfig{
				Bind: net.JoinHostPort("127.0.0.1", strconv.Itoa(firstPort)),
				Name: "first-node",
			},
		},
	})
	if err != nil {
		t.Fatalf("New first: %v", err)
	}
	defer first.Close()

	firstAddr := first.transport.(*memberlistTransport).list.LocalNode().Address()

	second, err := New(Config{
		Enabled: true,
		Transports: Transports{
			Memberlist: MemberlistConfig{
				Bind: net.JoinHostPort("127.0.0.1", strconv.Itoa(secondPort)),
				Name: "second-node",
				Join: []string{firstAddr},
			},
		},
	})
	if err != nil {
		t.Fatalf("New second: %v", err)
	}
	defer second.Close()

	// Wait for convergence on both sides.
	secondList := second.transport.(*memberlistTransport).list
	for i := 0; i < 100 && secondList.NumMembers() < 2; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if n := secondList.NumMembers(); n != 2 {
		t.Fatalf("second expected 2 members, got %d", n)
	}

	nodes := second.Nodes()
	if len(nodes) != 2 {
		t.Fatalf("Nodes() = %+v", nodes)
	}
	names := map[string]bool{}
	selfCount := 0
	for _, n := range nodes {
		names[n.Name] = true
		if n.Self {
			selfCount++
		}
	}
	if !names["first-node"] || !names["second-node"] {
		t.Fatalf("expected both node names, got %+v", nodes)
	}
	if selfCount != 1 {
		t.Fatalf("expected exactly one self node, got %d (%+v)", selfCount, nodes)
	}
}

func TestMemberlistJoinEmitsMembershipEvent(t *testing.T) {
	_, firstPort := freePort(t)
	_, secondPort := freePort(t)

	first, err := New(Config{
		Enabled: true,
		Transports: Transports{
			Memberlist: MemberlistConfig{
				Bind: net.JoinHostPort("127.0.0.1", strconv.Itoa(firstPort)),
				Name: "first-node",
			},
		},
	})
	if err != nil {
		t.Fatalf("New first: %v", err)
	}
	defer first.Close()

	firstAddr := first.transport.(*memberlistTransport).list.LocalNode().Address()

	second, err := New(Config{
		Enabled: true,
		Transports: Transports{
			Memberlist: MemberlistConfig{
				Bind: net.JoinHostPort("127.0.0.1", strconv.Itoa(secondPort)),
				Name: "second-node",
				Join: []string{firstAddr},
			},
		},
	})
	if err != nil {
		t.Fatalf("New second: %v", err)
	}
	defer second.Close()

	// The first node observes membership events; the first is its own join,
	// so drain until we see the second node's join.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		got, err := first.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got.Type == TypeMemberlist && got.Meta["node"] == "second-node" {
			if got.Meta["action"] != "join" {
				t.Fatalf("membership event = %+v", got)
			}
			return
		}
	}
}
