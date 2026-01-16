package ui

import (
	"fmt"
	"strings"

	"github.com/rivo/tview"

	"p2p-messenger/internal/entity"
	"p2p-messenger/internal/repository"
)

type Sidebar struct {
	View             *tview.List
	peerRepo         *repository.PeerRepository
	currentPeerCount int
}

func NewSidebar(peerRepo *repository.PeerRepository) *Sidebar {
	view := tview.NewList()
	view.SetTitle("peers").SetBorder(true)

	return &Sidebar{
		View:             view,
		peerRepo:         peerRepo,
		currentPeerCount: -1,
	}
}

func (s *Sidebar) Reprint() {
	peers := s.peerRepo.GetPeers()
	peersCount := len(peers)
	
	// Always reprint to update connection types
	s.currentPeerCount = peersCount

	s.View.Clear()

	for _, peer := range peers {
		// Display username only (no ID)
		displayName := peer.Username
		if displayName == "" {
			displayName = peer.PeerID
		}
		
		// Add connection type indicators on the leftmost
		connTypes := s.formatConnectionTypes(peer)
		displayText := fmt.Sprintf("%s %s", connTypes, displayName)
		
		// Store peerID in secondary text (not displayed) for retrieval
		s.View.
			AddItem(displayText, peer.PeerID, 0, nil)
	}
}

func (s *Sidebar) formatConnectionTypes(peer *entity.Peer) string {
	if len(peer.ConnectionTypes) == 0 {
		return ""
	}
	
	var indicators []string
	for _, ct := range peer.ConnectionTypes {
		switch ct {
		case entity.ConnectionBLE:
			indicators = append(indicators, "[green]●[white]BLE")
		case entity.ConnectionNAT:
			indicators = append(indicators, "[yellow]●[white]NAT")
		case entity.ConnectionInternet:
			indicators = append(indicators, "[blue]●[white]Internet")
		}
	}
	
	if len(indicators) > 0 {
		return fmt.Sprintf("(%s) ", strings.Join(indicators, ","))
	}
	return ""
}
