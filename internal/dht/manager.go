package dht

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/multiformats/go-multiaddr"
)

const (
	dhtBootstrapInterval = 5 * time.Minute
	mdnsServiceName      = "p2p-chat"
)

// PeerFoundCallback is called when a peer is discovered
type PeerFoundCallback func(peerID string, addrs []multiaddr.Multiaddr)

// Manager handles DHT-based peer discovery
type Manager struct {
	host        host.Host
	dht         *dht.IpfsDHT
	mdns        mdns.Service
	ctx         context.Context
	cancel      context.CancelFunc
	peerFoundCb PeerFoundCallback
}

// NewManager creates a new DHT manager
func NewManager(port int, peerFoundCb PeerFoundCallback) (*Manager, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Generate a keypair for libp2p (ed25519 for privacy)
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to generate keypair: %w", err)
	}

	// Create libp2p host
	listenAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create listen addr: %w", err)
	}

	h, err := libp2p.New(
		libp2p.ListenAddrs(listenAddr),
		libp2p.Identity(priv),
		libp2p.NATPortMap(),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	// Create DHT
	dhtInstance, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		cancel()
		h.Close()
		return nil, fmt.Errorf("failed to create DHT: %w", err)
	}

	// Bootstrap DHT
	if err := dhtInstance.Bootstrap(ctx); err != nil {
		log.Printf("dht: bootstrap warning: %v", err)
	}

	// Create mDNS service for local discovery
	peerHandler := &peerHandler{
		host:        h,
		peerFoundCb: peerFoundCb,
	}
	mdnsService := mdns.NewMdnsService(h, mdnsServiceName, peerHandler)

	return &Manager{
		host:        h,
		dht:         dhtInstance,
		mdns:        mdnsService,
		ctx:         ctx,
		cancel:      cancel,
		peerFoundCb: peerFoundCb,
	}, nil
}

// Start begins DHT operations
func (m *Manager) Start() {
	go m.bootstrapPeriodically()
	m.mdns.Start()
}

// Stop stops DHT operations
func (m *Manager) Stop() {
	m.cancel()
	m.mdns.Close()
	m.dht.Close()
	m.host.Close()
}

// FindPeers searches for peers with the given peer ID
func (m *Manager) FindPeers(peerID string) ([]peer.AddrInfo, error) {
	pid, err := peer.Decode(peerID)
	if err != nil {
		return nil, fmt.Errorf("invalid peer ID: %w", err)
	}

	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()

	peerInfo, err := m.dht.FindPeer(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("peer not found: %w", err)
	}

	return []peer.AddrInfo{peerInfo}, nil
}

// GetPeerID returns this node's peer ID
func (m *Manager) GetPeerID() string {
	return m.host.ID().String()
}

// GetAddresses returns this node's addresses
func (m *Manager) GetAddresses() []multiaddr.Multiaddr {
	return m.host.Addrs()
}

func (m *Manager) bootstrapPeriodically() {
	ticker := time.NewTicker(dhtBootstrapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := m.dht.Bootstrap(m.ctx); err != nil {
				log.Printf("dht: periodic bootstrap error: %v", err)
			}
		case <-m.ctx.Done():
			return
		}
	}
}

type peerHandler struct {
	host        host.Host
	peerFoundCb PeerFoundCallback
}

func (h *peerHandler) HandlePeerFound(info peer.AddrInfo) {
	log.Printf("dht: mdns discovered peer %s addrs %v", info.ID.String(), info.Addrs)
	h.host.Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.PermanentAddrTTL)
	if h.peerFoundCb != nil {
		h.peerFoundCb(info.ID.String(), info.Addrs)
	}
}
