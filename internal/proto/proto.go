package proto

import (
	"encoding/base64"
	
	"p2p-messenger/internal/crypto"
	"p2p-messenger/internal/repository"
)

type Proto struct {
	// PublicKeyStr is the Noise Protocol public key as string (for metadata)
	PublicKeyStr string
	// PublicKey is the Noise Protocol public key bytes
	PublicKey []byte
	// PrivateKey is the Noise Protocol private key (for responder sessions)
	PrivateKey crypto.NoiseKeypair
	Peers      *repository.PeerRepository
	Port       string
	// Username is the display name for this peer
	Username string
	// NetworkManager is set after creation to allow UI access
	NetworkManager interface {
		GetAvailableModes() (bleAvailable, natAvailable, internetAvailable bool)
	}
}

func NewProto(port string) (*Proto, error) {
	keypair, pubKey, err := crypto.GenerateKeypair()
	if err != nil {
		return nil, err
	}

	// Generate a default username from peer ID (first 8 chars)
	peerID := crypto.PeerID(pubKey)
	username := peerID
	if len(username) > 8 {
		username = username[:8]
	}

	// Encode public key as base64 to avoid special characters breaking message parsing
	return &Proto{
		PublicKeyStr: base64.StdEncoding.EncodeToString(pubKey),
		PublicKey:    pubKey,
		PrivateKey:   keypair,
		Peers:        repository.NewPeerRepository(),
		Port:         port,
		Username:     username,
	}, nil
}

// SetUsername sets the display username for this peer
func (p *Proto) SetUsername(username string) {
	if username != "" {
		p.Username = username
	}
}
