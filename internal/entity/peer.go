package entity

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"p2p-messenger/internal/crypto"

	"github.com/gorilla/websocket"
)

var ErrPeerIsDeleted = errors.New("peer disconnected")

// ConnectionType represents how a peer is connected
type ConnectionType int

const (
	ConnectionBLE ConnectionType = iota
	ConnectionNAT
	ConnectionInternet
)

// String returns a human-readable name for the connection type
func (ct ConnectionType) String() string {
	switch ct {
	case ConnectionBLE:
		return "BLE"
	case ConnectionNAT:
		return "NAT"
	case ConnectionInternet:
		return "Internet"
	default:
		return "Unknown"
	}
}

// Peer represents a discovered peer with minimal metadata for privacy
type Peer struct {
	PeerID                string
	PublicKey             []byte
	Messages              []*Message
	AddrIP                string
	Port                  string
	BLEAddr               string
	Username              string
	ConnectionTypes       []ConnectionType
	PrimaryConnectionType ConnectionType
	Session               *crypto.Session
	conn                  *websocket.Conn
	connLock              sync.Mutex
	sendLock              sync.Mutex // Serializes encryption and writing to socket
	pendingMessage        string     // Message waiting for handshake to complete
	pendingMessageLock    sync.Mutex
}

func (p *Peer) AddMessage(text, author string) {
	p.Messages = append(p.Messages, &Message{
		Time:   time.Now(),
		Text:   text,
		Author: author,
	})
}

func (p *Peer) EstablishConnection(privateKey crypto.NoiseKeypair) error {
	// Get preferred address based on primary connection type
	// Note: BLE is only for discovery - actual transport uses websocket
	ip, port, err := p.GetPreferredAddress()
	if err != nil {
		return err
	}

	// Check if connection already exists (with lock)
	p.connLock.Lock()
	if p.conn != nil && p.Session != nil {
		p.connLock.Unlock()
		return nil
	}
	p.connLock.Unlock()

	// Establish connection outside of lock to avoid deadlock
	// All connection types (BLE discovery, NAT, Internet) use websocket transport
	u := url.URL{Scheme: "ws", Host: fmt.Sprintf("%s:%s", ip, port), Path: "/chat"}
	log.Printf("peer %s: establishing connection via %s to %s:%s", p.PeerID, p.PrimaryConnectionType.String(), ip, port)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to dial websocket: %w", err)
	}
	session, err := crypto.NewInitiatorSession(privateKey)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to create initiator session: %w", err)
	}

	// Lock again to set connection
	p.connLock.Lock()
	defer p.connLock.Unlock()

	// Double-check in case another goroutine established connection
	if p.conn != nil && p.Session != nil {
		conn.Close()
		return nil
	}

	p.Session = session
	p.conn = conn
	go p.readMessages()
	return nil
}

func (p *Peer) readMessages() {
	for {
		p.connLock.Lock()
		conn := p.conn
		session := p.Session
		p.connLock.Unlock()
		if conn == nil || session == nil {
			break
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("peer %s: read error: %v", p.PeerID, err)
			p.connLock.Lock()
			if p.conn == conn {
				conn.Close()
				p.conn = nil
				p.Session = nil
			}
			p.connLock.Unlock()
			break
		}

		log.Printf("peer %s: received message (len=%d), handshake complete before: %v", p.PeerID, len(msg), session.IsHandshakeComplete())

		// Check if handshake was in progress before reading
		wasHandshaking := !session.IsHandshakeComplete()

		decrypted, err := session.ReadMessage(msg)
		if err != nil {
			log.Printf("peer %s: decrypt error: %v", p.PeerID, err)
			continue
		}

		// Check if handshake just completed
		isHandshakeComplete := session.IsHandshakeComplete()
		if wasHandshaking && isHandshakeComplete {
			log.Printf("peer %s: handshake just completed!", p.PeerID)
		}

		if len(decrypted) == 0 {
			log.Printf("peer %s: empty decrypted message (handshake message), handshake complete: %v", p.PeerID, isHandshakeComplete)
			// If handshake just completed, we might need to send message 3 with payload
			// But the noise library should handle this automatically on the next WriteMessage call
			// For now, just continue - the payload will be sent when user sends next message
			// or we can check for pending message here
			if wasHandshaking && isHandshakeComplete {
				log.Printf("peer %s: handshake completed after receiving handshake message!", p.PeerID)
				// Check if there's a pending message to send in message 3
				p.pendingMessageLock.Lock()
				pending := p.pendingMessage
				if pending != "" {
					p.pendingMessage = ""
					p.pendingMessageLock.Unlock()
					log.Printf("peer %s: sending pending message in message 3", p.PeerID)
					// Send message 3 with the pending payload
					go func() {
						if err := p.sendMessageInternal(pending); err != nil {
							log.Printf("peer %s: failed to send pending message: %v", p.PeerID, err)
						}
					}()
				} else {
					p.pendingMessageLock.Unlock()
				}
			}
			continue
		}

		log.Printf("peer %s: decrypted message: %s", p.PeerID, string(decrypted))

		author := p.Username
		if author == "" {
			author = p.PeerID
		}
		p.AddMessage(string(decrypted), author)
	}
}

// sendMessageInternal sends a message without establishing connection (assumes it exists)
func (p *Peer) sendMessageInternal(message string) error {
	p.connLock.Lock()
	session := p.Session
	conn := p.conn
	p.connLock.Unlock()

	if session == nil || conn == nil {
		return fmt.Errorf("connection not established")
	}

	// CRITICAL: Lock sending to ensure atomicity
	p.sendLock.Lock()
	defer p.sendLock.Unlock()

	encrypted, err := session.WriteMessage([]byte(message))
	if err != nil {
		return fmt.Errorf("failed to encrypt message: %w", err)
	}

	if len(encrypted) == 0 {
		return fmt.Errorf("empty encrypted message")
	}

	// Verify connection is still same
	p.connLock.Lock()
	if p.conn != conn {
		p.connLock.Unlock()
		return fmt.Errorf("connection was closed")
	}
	p.connLock.Unlock()

	if err := conn.WriteMessage(websocket.BinaryMessage, encrypted); err != nil {
		p.connLock.Lock()
		if p.conn == conn {
			conn.Close()
			p.conn = nil
			p.Session = nil
		}
		p.connLock.Unlock()
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

// SendMessage sends an encrypted message using Noise Protocol
func (p *Peer) SendMessage(message string, privateKey crypto.NoiseKeypair) error {
	p.connLock.Lock()
	if p.conn == nil || p.Session == nil {
		p.connLock.Unlock()
		if err := p.EstablishConnection(privateKey); err != nil {
			log.Printf("peer %s: failed to establish connection: %v", p.PeerID, err)
			return fmt.Errorf("failed to establish connection: %w", err)
		}
		p.connLock.Lock()
	}

	session := p.Session
	conn := p.conn
	p.connLock.Unlock()

	if session == nil || conn == nil {
		return fmt.Errorf("connection not established")
	}

	// CRITICAL: Lock sending to ensure encryption and writing happen atomically
	// This prevents race conditions where message A is encrypted before B,
	// but B is written to the socket before A, ensuring nonce synchronization.
	p.sendLock.Lock()
	defer p.sendLock.Unlock()

	// Check handshake state with lock to ensure we have latest state
	// Note: We don't need connLock here because sendLock serializes all sends
	// and we have local references to session/conn.
	// If connection closes relative to us, the WriteMessage below will fail.

	handshakeComplete := session.IsHandshakeComplete()
	log.Printf("peer %s: sending message (len=%d), handshake complete: %v", p.PeerID, len(message), handshakeComplete)

	// WriteMessage handles handshake automatically:
	// - During handshake: includes payload in handshake message (message 1 or 3)
	// - After handshake: encrypts and returns the message
	encrypted, err := session.WriteMessage([]byte(message))
	if err != nil {
		log.Printf("peer %s: failed to encrypt message: %v", p.PeerID, err)
		return fmt.Errorf("failed to encrypt message: %w", err)
	}

	// After WriteMessage, check if handshake completed (it might have just completed)
	handshakeCompleteAfter := session.IsHandshakeComplete()
	if !handshakeComplete && handshakeCompleteAfter {
		log.Printf("peer %s: handshake completed during WriteMessage!", p.PeerID)
	}

	// WriteMessage should always return data (either handshake message or encrypted message)
	// If it's empty, the payload is queued for message 3 (happens in Noise XX when payload
	// can't fit in message 1)
	if len(encrypted) == 0 {
		log.Printf("peer %s: payload queued for message 3, storing for later", p.PeerID)
		// Store message to send in message 3 after handshake completes
		p.pendingMessageLock.Lock()
		p.pendingMessage = message
		p.pendingMessageLock.Unlock()
		// Message will be sent in message 3 when handshake completes (handled in readMessages)
		return nil
	}

	log.Printf("peer %s: sending encrypted/handshake message (len=%d), handshake complete: %v", p.PeerID, len(encrypted), session.IsHandshakeComplete())

	// Verify connection is still same (just in case)
	p.connLock.Lock()
	if p.conn != conn {
		p.connLock.Unlock()
		return fmt.Errorf("connection was closed")
	}
	p.connLock.Unlock()

	if err := conn.WriteMessage(websocket.BinaryMessage, encrypted); err != nil {
		log.Printf("peer %s: failed to send websocket message: %v", p.PeerID, err)

		// If send fails, force close connection to reset state
		// This is important because we might have incremented nonce but failed to send
		p.connLock.Lock()
		if p.conn == conn { // Only close if it's still the same connection we used
			conn.Close()
			p.conn = nil
			p.Session = nil
		}
		p.connLock.Unlock()

		return fmt.Errorf("failed to send message: %w", err)
	}

	log.Printf("peer %s: message sent successfully", p.PeerID)
	return nil
}

func (p *Peer) Close() {
	p.connLock.Lock()
	defer p.connLock.Unlock()
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
		p.Session = nil
	}
}

func (p *Peer) AddConnectionType(ct ConnectionType) {
	for _, existing := range p.ConnectionTypes {
		if existing == ct {
			return
		}
	}
	p.ConnectionTypes = append(p.ConnectionTypes, ct)
	// Set primary to the lowest value (BLE=0, NAT=1, Internet=2)
	// This ensures BLE is preferred over NAT over Internet
	if len(p.ConnectionTypes) == 1 || ct < p.PrimaryConnectionType {
		p.PrimaryConnectionType = ct
	}
}

// GetPreferredAddress returns the address to use based on primary connection type
// BLE is only for discovery - actual connections use websocket (NAT/Internet)
func (p *Peer) GetPreferredAddress() (ip, port string, err error) {
	// If primary is BLE but we have NAT/Internet address, use that (BLE is discovery only)
	// Otherwise use the primary connection type's address
	switch p.PrimaryConnectionType {
	case ConnectionBLE:
		// BLE is discovery only - need AddrIP/Port for websocket connection
		// If we have AddrIP/Port, use it (peer was also discovered via NAT/Internet)
		if p.AddrIP != "" && p.Port != "" {
			return p.AddrIP, p.Port, nil
		}
		return "", "", errors.New("BLE peer needs NAT/Internet address for connection")
	case ConnectionNAT, ConnectionInternet:
		// NAT and Internet both use websocket with AddrIP/Port
		if p.AddrIP == "" || p.Port == "" {
			return "", "", errors.New("peer address not available")
		}
		return p.AddrIP, p.Port, nil
	default:
		return "", "", errors.New("unknown connection type")
	}
}

// HasActiveConnection returns true if the peer has an active websocket connection
func (p *Peer) HasActiveConnection() bool {
	p.connLock.Lock()
	defer p.connLock.Unlock()
	return p.conn != nil && p.Session != nil
}
