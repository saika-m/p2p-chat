package repository

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"p2p-messenger/internal/entity"
)

const (
	peerValidationTimeOut = 10 * time.Second // Check less frequently
	peerValidationRetries = 3                // Number of consecutive failures before deleting
)

type PeerRepository struct {
	rwMutex            *sync.RWMutex
	peers              map[string]*entity.Peer
	failureCounts      map[string]int // Track consecutive validation failures
	failureCountsMutex sync.Mutex
}

func NewPeerRepository() *PeerRepository {
	peerRepository := &PeerRepository{
		rwMutex:       &sync.RWMutex{},
		peers:         make(map[string]*entity.Peer),
		failureCounts: make(map[string]int),
	}

	peerRepository.peersValidator()

	return peerRepository
}

func (p *PeerRepository) Add(peer *entity.Peer) {
	p.rwMutex.Lock()
	defer p.rwMutex.Unlock()

	existing, found := p.peers[peer.PeerID]
	if !found {
		p.peers[peer.PeerID] = peer
	} else {
		// Only use the BEST connection type (either/or, not combined)
		// Priority: BLE (0) > NAT (1) > Internet (2)
		// If new peer has a better connection type, replace existing
		// Otherwise, keep existing (don't add worse connection types)
		if len(peer.ConnectionTypes) > 0 {
			newConnectionType := peer.ConnectionTypes[0] // New peer has one connection type
			if newConnectionType < existing.PrimaryConnectionType {
				// New connection type is better - replace with new one only
				existing.ConnectionTypes = []entity.ConnectionType{newConnectionType}
				existing.PrimaryConnectionType = newConnectionType
				// Update address fields when switching to better connection type
				if peer.AddrIP != "" {
					existing.AddrIP = peer.AddrIP
				}
				if peer.Port != "" {
					existing.Port = peer.Port
				}
				if peer.BLEAddr != "" {
					existing.BLEAddr = peer.BLEAddr
				}
			}
			// If new connection type is same or worse, don't change connection type
			// But still update address fields if missing (for same connection type)
			if newConnectionType == existing.PrimaryConnectionType {
				if existing.AddrIP == "" && peer.AddrIP != "" {
					existing.AddrIP = peer.AddrIP
				}
				if existing.Port == "" && peer.Port != "" {
					existing.Port = peer.Port
				}
				if existing.BLEAddr == "" && peer.BLEAddr != "" {
					existing.BLEAddr = peer.BLEAddr
				}
			}
		}

		if len(existing.PublicKey) == 0 && len(peer.PublicKey) > 0 {
			existing.PublicKey = peer.PublicKey
		}
		// Update username if provided (can change)
		if peer.Username != "" {
			existing.Username = peer.Username
		}
	}
}

func (p *PeerRepository) Delete(peerID string) {
	p.rwMutex.Lock()
	defer p.rwMutex.Unlock()

	peer, found := p.peers[peerID]
	if found && peer != nil {
		// Close connection if it exists
		peer.Close()
	}
	delete(p.peers, peerID)
}

func (p *PeerRepository) Get(peerID string) (*entity.Peer, bool) {
	p.rwMutex.RLock()
	defer p.rwMutex.RUnlock()

	peer, found := p.peers[peerID]
	return peer, found
}

func (p *PeerRepository) GetPeers() []*entity.Peer {
	peersSlice := make([]*entity.Peer, 0, len(p.peers))

	for _, peer := range p.peers {
		peersSlice = append(peersSlice, peer)
	}

	sort.Slice(peersSlice, func(i, j int) bool {
		return peersSlice[i].PeerID < peersSlice[j].PeerID
	})

	return peersSlice
}

func (p *PeerRepository) peersValidator() {
	ticker := time.NewTicker(peerValidationTimeOut)

	go func() {
		for {
			<-ticker.C
			// Make a copy of peers to avoid holding lock during network operations
			p.rwMutex.RLock()
			peersCopy := make([]*entity.Peer, 0, len(p.peers))
			for _, peer := range p.peers {
				peersCopy = append(peersCopy, peer)
			}
			p.rwMutex.RUnlock()

			for _, peer := range peersCopy {
				// Skip BLE peers - they have different validation
				if peer.PrimaryConnectionType == entity.ConnectionBLE {
					continue
				}
				if peer.AddrIP == "" || peer.Port == "" {
					continue
				}

				// Check if peer has an active connection - if so, skip validation
				// Active connections are a better indicator than periodic pings
				if peer.HasActiveConnection() {
					// Reset failure count if peer has active connection
					p.failureCountsMutex.Lock()
					p.failureCounts[peer.PeerID] = 0
					p.failureCountsMutex.Unlock()
					continue
				}

				// Only validate peers without active connections
				u := url.URL{Scheme: "ws", Host: fmt.Sprintf("%s:%s", peer.AddrIP, peer.Port), Path: "/meow"}

				// Use a short timeout to avoid hanging on network issues
				dialer := &websocket.Dialer{
					HandshakeTimeout: 2 * time.Second,
				}

				c, _, err := dialer.Dial(u.String(), nil)
				if c == nil || err != nil {
					// Increment failure count
					p.failureCountsMutex.Lock()
					failures := p.failureCounts[peer.PeerID] + 1
					p.failureCounts[peer.PeerID] = failures
					p.failureCountsMutex.Unlock()

					// Only delete after multiple consecutive failures
					if failures >= peerValidationRetries {
						// Check if it's a network error that might be temporary
						shouldDelete := true
						if err != nil {
							if netErr, ok := err.(net.Error); ok {
								if netErr.Timeout() || netErr.Temporary() {
									// Temporary network issue, don't delete yet
									shouldDelete = false
								}
							}
						}

						if shouldDelete {
							p.Delete(peer.PeerID)
							p.failureCountsMutex.Lock()
							delete(p.failureCounts, peer.PeerID)
							p.failureCountsMutex.Unlock()
						}
					}
					continue
				}
				c.Close()

				// Reset failure count on successful validation
				p.failureCountsMutex.Lock()
				p.failureCounts[peer.PeerID] = 0
				p.failureCountsMutex.Unlock()
			}
		}
	}()
}
