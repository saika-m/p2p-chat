package crypto

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/flynn/noise"
)

const (
	// Noise_XX_25519_ChaChaPoly_BLAKE2b provides mutual authentication and forward secrecy
	noisePattern = "Noise_XX_25519_ChaChaPoly_BLAKE2b"
)

var (
	ErrInvalidMessage = errors.New("invalid encrypted message")
)

// Session represents an encrypted Noise Protocol session
type Session struct {
	handshakeState *noise.HandshakeState
	cs1            *noise.CipherState // for sending
	cs2            *noise.CipherState // for receiving
	initiator      bool
}

// NewInitiatorSession creates a new Noise session as the initiator with a static keypair
func NewInitiatorSession(keypair NoiseKeypair) (*Session, error) {
	config := noise.Config{
		CipherSuite:   noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2b),
		Random:        rand.Reader,
		Pattern:       noise.HandshakeXX,
		Initiator:     true,
		StaticKeypair: keypair,
	}

	hs, err := noise.NewHandshakeState(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create handshake state: %w", err)
	}

	return &Session{
		handshakeState: hs,
		initiator:      true,
	}, nil
}

// NewResponderSession creates a new Noise session as the responder
func NewResponderSession(keypair noise.DHKey) (*Session, error) {
	config := noise.Config{
		CipherSuite:   noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2b),
		Random:        rand.Reader,
		Pattern:       noise.HandshakeXX,
		Initiator:     false,
		StaticKeypair: keypair,
	}

	hs, err := noise.NewHandshakeState(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create handshake state: %w", err)
	}

	return &Session{
		handshakeState: hs,
		initiator:      false,
	}, nil
}

// WriteMessage performs handshake and encrypts a message
func (s *Session) WriteMessage(message []byte) ([]byte, error) {
	if s.cs1 == nil {
		// Still in handshake phase
		// fmt.Println("crypto: trace - WriteMessage (Handshake)")
		out, cs1, cs2, err := s.handshakeState.WriteMessage(nil, message)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("handshake write failed: %w", err)
		}
		if cs1 != nil {
			if s.initiator {
				s.cs1 = cs1 // I->R (Send)
				s.cs2 = cs2 // R->I (Recv)
			} else {
				s.cs1 = cs2 // R->I (Send)
				s.cs2 = cs1 // I->R (Recv)
			}
			// fmt.Println("crypto: trace - Handshake Complete! CipherStates initialized.")
		}
		return out, nil
	}

	// Handshake complete, encrypt message
	// fmt.Println("crypto: trace - WriteMessage (Transport)")
	return s.cs1.Encrypt(nil, nil, message)
}

// ReadMessage performs handshake and decrypts a message
func (s *Session) ReadMessage(message []byte) ([]byte, error) {
	if s.cs2 == nil {
		// Still in handshake phase
		// fmt.Println("crypto: trace - ReadMessage (Handshake)")
		plaintext, cs1, cs2, err := s.handshakeState.ReadMessage(nil, message)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("handshake read failed: %w", err)
		}
		if cs1 != nil {
			if s.initiator {
				s.cs1 = cs1 // I->R (Send)
				s.cs2 = cs2 // R->I (Recv)
			} else {
				s.cs1 = cs2 // R->I (Send)
				s.cs2 = cs1 // I->R (Recv)
			}
			// fmt.Println("crypto: trace - Handshake Complete! CipherStates initialized.")
		}
		return plaintext, nil
	}

	// Handshake complete, decrypt message
	// fmt.Println("crypto: trace - ReadMessage (Transport)")
	plaintext, err := s.cs2.Decrypt(nil, nil, message)
	if err != nil {
		return nil, fmt.Errorf("decrypt failed: %w", err)
	}
	return plaintext, nil
}

// GetRemotePublicKey returns the remote peer's public key after handshake completes
func (s *Session) GetRemotePublicKey() ([]byte, error) {
	if s.handshakeState == nil {
		return nil, fmt.Errorf("handshake state not initialized")
	}

	// Get remote peer's static public key from handshake state
	remotePubKey := s.handshakeState.PeerStatic()
	if remotePubKey == nil {
		return nil, fmt.Errorf("remote public key not available (handshake may not be complete)")
	}

	return remotePubKey, nil
}

// IsHandshakeComplete returns true if the handshake has completed
func (s *Session) IsHandshakeComplete() bool {
	return s.cs1 != nil && s.cs2 != nil
}

// PeerID generates a privacy-preserving peer ID from a public key
func PeerID(publicKey []byte) string {
	// Use first 16 bytes of public key hash as identifier
	// This provides privacy while allowing identification
	h := noise.HashBLAKE2b.Hash()
	h.Write(publicKey)
	sum := h.Sum(nil)
	return fmt.Sprintf("%x", sum[:16])
}

// NoiseKeypair is a type alias for noise.DHKey
type NoiseKeypair = noise.DHKey

// GenerateKeypair generates a Noise Protocol keypair
func GenerateKeypair() (NoiseKeypair, []byte, error) {
	keypair, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return noise.DHKey{}, nil, err
	}
	return keypair, keypair.Public, nil
}
