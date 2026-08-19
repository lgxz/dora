package events

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/hashicorp/memberlist"
)

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
	got, err := receiver.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got.ID != "evt-1" || got.Type != "test" || got.Sender != "sender" || got.Msg != "hello" || got.Meta["n"] != "1" {
		t.Fatalf("unexpected event: %+v", got)
	}
}
