package bluetooth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"p2p-messenger/internal/proto"
)

func TestManager(t *testing.T) {
	t.Skip("Skipping BLE test due to issues with the test environment")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Create two protos
	proto1, err := proto.NewProto("25045")
	assert.NoError(t, err)
	proto2, err := proto.NewProto("25046")
	assert.NoError(t, err)

	// Create two managers
	manager1 := NewManager(proto1.PublicKeyStr, proto1.Port, proto1.Username, proto1.Peers)
	manager2 := NewManager(proto2.PublicKeyStr, proto2.Port, proto2.Username, proto2.Peers)

	// Start managers
	manager1.Start()
	manager2.Start()

	// Wait for discovery
	select {
	case <-time.After(10 * time.Second):
		// Check if peers are discovered
		peers1 := proto1.Peers.GetPeers()
		peers2 := proto2.Peers.GetPeers()

		assert.Equal(t, 1, len(peers1))
		assert.Equal(t, 1, len(peers2))

		assert.Equal(t, proto2.PublicKeyStr, string(peers1[0].PublicKey))
		assert.Equal(t, proto1.PublicKeyStr, string(peers2[0].PublicKey))
	case <-ctx.Done():
		t.Fatal("Test timed out")
	}
}
