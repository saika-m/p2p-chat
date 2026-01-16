package network

import (
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/multiformats/go-multiaddr"

	"p2p-messenger/internal/bluetooth"
	"p2p-messenger/internal/dht"
	"p2p-messenger/internal/proto"
)

const (
	MulticastIP        = "224.0.0.1"
	ListenerIP         = "0.0.0.0"
	MulticastFrequency = 1 * time.Second
)

type Manager struct {
	Proto      *proto.Proto
	Listener   *Listener
	Discoverer *Discoverer
	BLE        *bluetooth.Manager
	DHT        *dht.Manager

	// Cached availability status (updated periodically)
	bleAvailable      bool
	natAvailable      bool
	internetAvailable bool
	lastCheck         time.Time
	checkMutex        sync.Mutex
}

func NewManager(proto *proto.Proto) *Manager {
	multicastAddr, err := net.ResolveUDPAddr(
		"udp",
		fmt.Sprintf("%s:%s", MulticastIP, proto.Port))
	if err != nil {
		log.Fatal(err)
	}

	listenerAddr := fmt.Sprintf("%s:%s", ListenerIP, proto.Port)

	portInt, err := strconv.Atoi(proto.Port)
	if err != nil {
		log.Fatalf("Invalid port: %v", err)
	}

	// DHT callback to add discovered peers
	dhtPeerFoundCb := func(peerID string, addrs []multiaddr.Multiaddr) {
		// Extract IP and port from multiaddr
		for _, addr := range addrs {
			var ip string
			var port string
			
			// Parse multiaddr to extract IP and port
			multiaddr.ForEach(addr, func(c multiaddr.Component) bool {
				switch c.Protocol().Code {
				case multiaddr.P_IP4, multiaddr.P_IP6:
					ip = c.Value()
				case multiaddr.P_TCP, multiaddr.P_UDP:
					port = c.Value()
				}
				return true
			})
			
			// Only process if we have both IP and port, and it's not a local/private IP
			if ip != "" && port != "" {
				// Check if it's a public IP (not localhost or private)
				ipAddr := net.ParseIP(ip)
				if ipAddr != nil && !ipAddr.IsLoopback() && !ipAddr.IsPrivate() {
					// Try to find existing peer by public key hash (peerID from DHT is libp2p peer ID, not our peer ID)
					// For now, we'll create a peer entry but we need the public key from the handshake
					// Since DHT uses libp2p peer IDs which are different from our Noise Protocol peer IDs,
					// we can't directly match. We'll need to match during handshake.
					// For now, skip DHT peers without public key - they'll be matched during handshake
				}
			}
		}
	}

	dhtManager, err := dht.NewManager(portInt+1, dhtPeerFoundCb) // Use different port for DHT
	if err != nil {
		log.Printf("Warning: DHT initialization failed: %v", err)
	}

	return &Manager{
		Proto:      proto,
		Listener:   NewListener(listenerAddr, proto),
		Discoverer: NewDiscoverer(multicastAddr, MulticastFrequency, proto),
		BLE:        bluetooth.NewManager(proto.PublicKeyStr, proto.Port, proto.Username, proto.Peers),
		DHT:        dhtManager,
	}
}

func (m *Manager) Start() {
	go m.Listener.Start()
	go m.Discoverer.Start()
	if m.BLE != nil {
		go m.BLE.Start()
		// Give BLE manager a moment to initialize before checking
		// This prevents race condition when Bluetooth is already on at startup
		time.Sleep(100 * time.Millisecond)
	}
	if m.DHT != nil {
		m.DHT.Start()
	}

	// Do initial availability check after BLE has had time to initialize
	m.updateAvailability()

	// Start periodic availability checking
	go m.checkAvailabilityPeriodically()
}

// checkAvailabilityPeriodically checks availability of each mode every second
func (m *Manager) checkAvailabilityPeriodically() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		<-ticker.C
		m.updateAvailability()
	}
}

// updateAvailability checks and updates the availability status of all modes
func (m *Manager) updateAvailability() {
	// Get current values first (to preserve on error for other checks)
	m.checkMutex.Lock()
	currentNat := m.natAvailable
	currentInternet := m.internetAvailable
	m.checkMutex.Unlock()

	// Check BLE availability FIRST and update immediately
	// This is completely independent and must always work, even when WiFi is off
	var bleAvail bool
	if m.BLE != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("network: BLE availability check panic: %v", r)
					// On panic, read current value
					m.checkMutex.Lock()
					bleAvail = m.bleAvailable
					m.checkMutex.Unlock()
				}
			}()
			// Do BLE check in complete isolation - this MUST always work
			bleAvail = m.BLE.IsAvailable()
		}()
	} else {
		bleAvail = false
	}

	// Update BLE immediately (don't wait for other checks)
	m.checkMutex.Lock()
	m.bleAvailable = bleAvail
	m.checkMutex.Unlock()

	// Now check other modes (these can fail without affecting BLE)
	var natAvail, internetAvail bool

	// Check NAT/multicast availability (completely independent)
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("network: NAT availability check panic: %v", r)
				natAvail = currentNat
			}
		}()
		natAvail = m.checkNATAvailable()
	}()

	// Check Internet availability (completely independent, runs last)
	// This is the slowest check and should not affect others
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("network: Internet availability check panic: %v", r)
				internetAvail = currentInternet
			}
		}()
		internetAvail = m.checkInternetAvailable()
	}()

	// Update NAT and Internet atomically
	m.checkMutex.Lock()
	m.natAvailable = natAvail
	m.internetAvailable = internetAvail
	m.lastCheck = time.Now()
	m.checkMutex.Unlock()
}

// checkNATAvailable checks if NAT/multicast is possible on current network
// Requires: active network connection AND multicast support
func (m *Manager) checkNATAvailable() bool {
	// First, check if we have an active network interface with an IP address
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}

	hasActiveInterface := false
	for _, iface := range interfaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		// Check if interface has a non-loopback IP address
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip != nil && !ip.IsLoopback() && ip.To4() != nil {
				hasActiveInterface = true
				break
			}
		}

		if hasActiveInterface {
			break
		}
	}

	if !hasActiveInterface {
		return false
	}

	// Now check if multicast actually works
	// Try to create a test multicast connection
	// If multicast is blocked (like on school WiFi), this will fail
	testAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%s", MulticastIP, m.Proto.Port))
	if err != nil {
		return false
	}

	// Try to listen on multicast address
	conn, err := net.ListenMulticastUDP("udp", nil, testAddr)
	if err != nil {
		return false
	}
	conn.Close()

	// Try to send to multicast address
	sendConn, err := net.DialUDP("udp", nil, testAddr)
	if err != nil {
		return false
	}
	sendConn.Close()

	return true
}

// checkInternetAvailable pings 8.8.8.8 to check internet connectivity
func (m *Manager) checkInternetAvailable() bool {
	// Use ping with timeout (1 second)
	// macOS: -c 1 (count), -W 1000 (wait timeout in milliseconds)
	// Linux: -c 1 (count), -W 1 (wait timeout in seconds)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1000", "8.8.8.8")
	err := cmd.Run()
	if err == nil {
		return true
	}

	// If context timeout, definitely not available
	if ctx.Err() == context.DeadlineExceeded {
		return false
	}

	// Try Linux format as fallback
	ctx2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel2()
	cmd = exec.CommandContext(ctx2, "ping", "-c", "1", "-W", "1", "8.8.8.8")
	err = cmd.Run()
	return err == nil && ctx2.Err() == nil
}

// GetAvailableModes returns which connection modes are currently available
func (m *Manager) GetAvailableModes() (bleAvailable, natAvailable, internetAvailable bool) {
	// Check if we need to update (without holding lock during check)
	needsUpdate := false
	m.checkMutex.Lock()
	if time.Since(m.lastCheck) > 500*time.Millisecond {
		needsUpdate = true
	}
	m.checkMutex.Unlock()

	// Do update outside of lock to avoid blocking
	if needsUpdate {
		m.updateAvailability()
	}

	// Return current values
	m.checkMutex.Lock()
	defer m.checkMutex.Unlock()
	return m.bleAvailable, m.natAvailable, m.internetAvailable
}
