package entity

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"p2p-messenger/internal/crypto"

	"github.com/gorilla/websocket"
)

// Mock connection and session wouldn't be easy because Peer uses concrete types.
// However, we can test the lock contention if we can set up a Peer with a mocked connection
// or just verify that SendMessage doesn't race on internal state.
// Since Peer uses *websocket.Conn which is a struct, we can't easily mock it without
// starting a real websocket server.

func TestPeer_SendMessage_Concurrency(t *testing.T) {
	// 1. Start a local websocket server to accept the connection
	server := NewMockServer(t)
	defer server.Close()

	// 2. Create a peer and connect to the server
	keypair, _, _ := crypto.GenerateKeypair()
	peer := &Peer{
		PeerID:                "test-peer",
		PrimaryConnectionType: ConnectionInternet,
		AddrIP:                "127.0.0.1",
		Port:                  server.Port,
	}

	// Establish connection
	err := peer.EstablishConnection(keypair)
	if err != nil {
		t.Fatalf("Failed to establish connection: %v", err)
	}
	defer peer.Close()

	// Wait for handshake to complete
	// We need to send at least one message to trigger the handshake start if it hasn't,
	// but EstablishConnection starts it? No, EstablishConnection just dials.
	// Actually, EstablishConnection creates session but doesn't send Msg 1?
	// Peer.SendMessage sends Msg 1.
	// So let's send ONE message to bootstrap handshake.
	if err := peer.SendMessage("init", keypair); err != nil {
		t.Fatalf("Failed to send init message: %v", err)
	}

	// Wait for handshake to complete (server needs to respond with Msg 2, then we need to send Msg 3)
	// Noise XX: Init->Resp (Msg1), Resp->Init (Msg2), Init->Resp (Msg3)
	// We sent Msg 1. We must wait for Msg 2, then Send Msg 3.
	// Since we don't have a callback for "Msg 2 received", we poll by trying to send Msg 3.

	handshakeDone := false
	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		peer.connLock.Lock()
		complete := peer.Session != nil && peer.Session.IsHandshakeComplete()
		peer.connLock.Unlock()

		if complete {
			handshakeDone = true
			break
		}

		// Try to send a message (Msg 3)
		// If it fails with "unexpected call", it means we haven't received Msg 2 yet.
		// If it succeeds, it might be Msg 3 (completing handshake).
		err := peer.SendMessage("handshake-fin", keypair)
		if err == nil {
			// Check if that completed it
			peer.connLock.Lock()
			if peer.Session != nil && peer.Session.IsHandshakeComplete() {
				handshakeDone = true
				peer.connLock.Unlock()
				break
			}
			peer.connLock.Unlock()
		}

		time.Sleep(100 * time.Millisecond)
	}

	if !handshakeDone {
		t.Fatal("Timed out waiting for handshake to complete")
	}

	// 3. Send messages concurrently
	var wg sync.WaitGroup
	count := 100
	errChan := make(chan error, count)

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := fmt.Sprintf("message %d", idx)
			if err := peer.SendMessage(msg, keypair); err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// 4. Verify no errors
	for err := range errChan {
		t.Errorf("SendMessage failed: %v", err)
	}
}

// MockServer helpers
type MockServer struct {
	server *httptest.Server
	Port   string
	Close  func()
}

func NewMockServer(t *testing.T) *MockServer {
	upgrader := websocket.Upgrader{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()

		// Mock responder handshake
		respKeypair, _, _ := crypto.GenerateKeypair()
		session, _ := crypto.NewResponderSession(respKeypair)

		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				break
			}

			// Just try to read/decrypt to keep session alive
			// We don't care about content validation here, just that
			// encryption sequence is maintained (if we were checking that)
			// But mostly we check client doesn't panic/race.

			// To properly verify order, we'd need to decrypt and check nonces,
			// but the client-side lock prevents client-side race.
			// If client sends out of order, the server decryption "might" fail depending on window,
			// but here we just consume.
			_, _ = session.ReadMessage(msg)

			// If handshake needed, send response
			if !session.IsHandshakeComplete() {
				resp, _ := session.WriteMessage(nil)
				c.WriteMessage(websocket.BinaryMessage, resp)
			}
		}
	})

	s := httptest.NewServer(handler)
	addr := s.Listener.Addr().String()
	parts := strings.Split(addr, ":")

	return &MockServer{
		Port:  parts[len(parts)-1],
		Close: s.Close,
	}
}
