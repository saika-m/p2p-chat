package network

import (
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"p2p-messenger/internal/crypto"
	"p2p-messenger/internal/entity"
	"p2p-messenger/internal/proto"
)

var (
	upgrader = websocket.Upgrader{}
)

type Listener struct {
	proto *proto.Proto
	addr  string
}

func NewListener(addr string, proto *proto.Proto) *Listener {
	return &Listener{
		proto: proto,
		addr:  addr,
	}
}

func (l *Listener) chat(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	log.Printf("listener: new websocket connection from %s", r.RemoteAddr)

	// Establish Noise Protocol session as responder
	session, err := crypto.NewResponderSession(l.proto.PrivateKey)
	if err != nil {
		return
	}

	// Store the peer once we identify it from the handshake
	var peer *entity.Peer
	peerID := ""

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}

		// Decrypt using Noise Protocol (handshake happens on first message)
		log.Printf("listener: received message from %s (len=%d), handshake complete before: %v", r.RemoteAddr, len(message), session.IsHandshakeComplete())
		wasHandshaking := !session.IsHandshakeComplete()

		decryptedMessage, err := session.ReadMessage(message)
		if err != nil {
			// If decryption fails after handshake is complete, this is a serious error
			// It might indicate cipher state mismatch - this could happen if:
			// 1. Messages are out of order
			// 2. Nonce counter is out of sync
			// 3. Session state was corrupted
			if session.IsHandshakeComplete() {
				log.Printf("listener: CRITICAL - decrypt failed after handshake complete: %v (message len=%d, from %s)", err, len(message), r.RemoteAddr)
				// After handshake, decryption failures are fatal - close connection
				// The session state is corrupted and cannot be recovered
				break
			} else {
				log.Printf("listener: decrypt error (handshake in progress): %v", err)
			}
			continue
		}
		handshakeCompleteAfter := session.IsHandshakeComplete()
		if wasHandshaking && handshakeCompleteAfter {
			log.Printf("listener: HANDSHAKE JUST COMPLETED! (decrypted len=%d)", len(decryptedMessage))
		}
		log.Printf("listener: decrypted message (len=%d), handshake complete after: %v", len(decryptedMessage), handshakeCompleteAfter)

		// If handshake was in progress and we just received message 1, we need to send message 2
		// In Noise Protocol XX, the responder must send message 2 after receiving message 1
		if wasHandshaking && !handshakeCompleteAfter {
			log.Printf("listener: sending handshake message 2 to complete handshake")
			// Send message 2 (empty payload for handshake)
			message2, err := session.WriteMessage(nil)
			if err != nil {
				log.Printf("listener: failed to create handshake message 2: %v", err)
			} else if len(message2) > 0 {
				if err := conn.WriteMessage(websocket.BinaryMessage, message2); err != nil {
					log.Printf("listener: failed to send handshake message 2: %v", err)
				} else {
					log.Printf("listener: sent handshake message 2 (len=%d)", len(message2))
					// Check if handshake is now complete
					if session.IsHandshakeComplete() {
						log.Printf("listener: handshake completed after sending message 2")
					}
				}
			}
		}

		// Try to identify peer - check after each message in case handshake just completed
		if peer == nil {
			// First, try to get remote public key from handshake (most reliable)
			// This will only work after handshake completes
			remotePubKey, err := session.GetRemotePublicKey()
			if err == nil && len(remotePubKey) == 32 {
				// Find peer by public key
				remotePeerID := crypto.PeerID(remotePubKey)
				if foundPeer, ok := l.proto.Peers.Get(remotePeerID); ok {
					peer = foundPeer
					peerID = remotePeerID
					// Don't overwrite peer's session - the listener (responder) maintains its own session
					// The peer's session is for the initiator side (sending messages)
					// We just need to identify the peer, not share session state
					log.Printf("listener: discovered peer %s via handshake", peerID)
				} else {
					// Peer not found by public key - might be a new peer or handshake not complete
					// Try to find by IP as fallback
					remoteAddr := r.RemoteAddr
					ip, _, err := net.SplitHostPort(remoteAddr)
					if err != nil {
						ip = remoteAddr
					}

					peers := l.proto.Peers.GetPeers()
					for _, p := range peers {
						if p.AddrIP == ip {
							peer = p
							peerID = p.PeerID
							// Don't overwrite peer's session - listener maintains its own session
							log.Printf("listener: discovered peer %s via address %s (handshake in progress)", peerID, ip)
							break
						}
					}
				}
			} else {
				// Handshake not complete yet - try to find peer by remote address
				// This works even before handshake completes
				remoteAddr := r.RemoteAddr
				ip, _, err := net.SplitHostPort(remoteAddr)
				if err != nil {
					ip = remoteAddr
				}

				// Find peer by IP address
				peers := l.proto.Peers.GetPeers()
				for _, p := range peers {
					if p.AddrIP == ip {
						peer = p
						peerID = p.PeerID
						// Don't overwrite peer's session - listener maintains its own session
						log.Printf("listener: discovered peer %s via address %s (before handshake)", peerID, ip)
						break
					}
				}
			}

			// If still not found, this might be a handshake message - continue to next message
			if peer == nil {
				continue
			}
		}

		// Skip empty messages (handshake-only messages in Noise Protocol XX)
		// Only add messages with actual content
		if len(decryptedMessage) == 0 {
			continue
		}

		// Add message with sender's username (fallback to peer ID if username not available)
		if peer != nil {
			author := peer.Username
			if author == "" {
				author = peerID
			}
			peer.AddMessage(string(decryptedMessage), author)
			log.Printf("listener: message from %s: %s", peerID, string(decryptedMessage))
		}
	}
}

func (l *Listener) meow(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.Close()
}

func (l *Listener) Start() {
	http.HandleFunc("/chat", l.chat)
	http.HandleFunc("/meow", l.meow)

	// Retry server startup if it fails (e.g., due to network changes)
	for {
		server := &http.Server{
			Addr:    l.addr,
			Handler: nil,
		}

		err := server.ListenAndServe()
		if err != nil {
			log.Printf("listener: server error: %v, attempting to restart...", err)
			time.Sleep(2 * time.Second)
			continue
		}
		break
	}
}
