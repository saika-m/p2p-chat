package bluetooth

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/darwin"

	"p2p-messenger/internal/crypto"
	"p2p-messenger/internal/entity"
)

const (
	bleServiceUUIDStr     = "6e400001-b5a3-f393-e0a9-e50e24dcca9e"
	bleMetaCharacteristic = "6e400002-b5a3-f393-e0a9-e50e24dcca9e"

	connectionTimeout = 5 * time.Second
)

// Manager hosts a BLE GATT service that advertises peer metadata and scans for
// nearby peers to support offline/local discovery.
type Manager struct {
	serviceUUID ble.UUID
	metaUUID    ble.UUID
	proto       *entityProto
	stop        context.CancelFunc
	Available   bool
}

// entityProto is a minimal subset of proto.Proto to avoid import cycles.
type entityProto struct {
	PublicKeyStr string
	Port         string
	Username     string
	Peers        peerRepository
}

// peerRepository matches the public methods we need from repository.PeerRepository.
type peerRepository interface {
	Add(peer *entity.Peer)
	Get(peerID string) (*entity.Peer, bool)
}

// IsAvailable returns true if Bluetooth is enabled and available on the system.
// This check is completely independent of the manager's internal state.
func (m *Manager) IsAvailable() bool {
	// IMPORTANT: This check must be completely independent and not affected
	// by other system operations (like network checks) or the manager's state.
	// Even if advertise/scan fail, Bluetooth hardware might still be available.

	// Use system command to check Bluetooth power state (avoids device conflicts)
	// This is more reliable than creating a device when BLE manager is already running
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "system_profiler", "SPBluetoothDataType")
	output, err := cmd.Output()
	if err == nil && ctx.Err() == nil {
		outputStr := string(output)
		// Check if Bluetooth is powered on
		// Look for indicators that Bluetooth is on
		if strings.Contains(outputStr, "State: On") ||
			strings.Contains(outputStr, "Powered: On") ||
			(len(outputStr) > 300 && !strings.Contains(outputStr, "State: Off") && !strings.Contains(outputStr, "Powered: Off")) {
			// Bluetooth appears to be on according to system
			return true
		}
		// If output explicitly says off or is very short, Bluetooth is off
		if strings.Contains(outputStr, "State: Off") || strings.Contains(outputStr, "Powered: Off") || len(outputStr) < 100 {
			return false
		}
	}

	// Fallback: Try to create a BLE device directly (if system command failed)
	// This will fail if Bluetooth is disabled
	// Use a quick timeout to avoid hanging
	done := make(chan bool, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- false
			}
		}()
		dev, err := darwin.NewDevice()
		if err != nil {
			done <- false
			return
		}
		_ = dev // Just verify we can create it
		done <- true
	}()

	select {
	case result := <-done:
		return result
	case <-time.After(200 * time.Millisecond):
		// Timeout - if system command said it's on, trust that
		// Otherwise assume off
		return false
	}
}

func NewManager(publicKeyStr string, port string, username string, peers peerRepository) *Manager {
	return &Manager{
		serviceUUID: ble.MustParse(bleServiceUUIDStr),
		metaUUID:    ble.MustParse(bleMetaCharacteristic),
		proto: &entityProto{
			PublicKeyStr: publicKeyStr,
			Port:         port,
			Username:     username,
			Peers:        peers,
		},
	}
}

// Start begins advertising and scanning; errors are logged but not fatal.
func (m *Manager) Start() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in bluetooth.Manager.Start: %v", r)
			m.Available = false
		}
	}()

	dev, err := darwin.NewDevice()
	if err != nil {
		log.Printf("bluetooth: skipping BLE, unable to init device: %v", err)
		m.Available = false
		return
	}
	ble.SetDefaultDevice(dev)
	log.Printf("bluetooth: default BLE device set")

	if err := m.addService(); err != nil {
		log.Printf("bluetooth: unable to register service: %v", err)
		m.Available = false
		return
	}

	m.Available = true
	ctx, cancel := context.WithCancel(context.Background())
	m.stop = cancel

	go m.advertise(ctx)
	go m.scan(ctx)
}

func (m *Manager) Stop() {
	if m.stop != nil {
		m.stop()
	}
}

func (m *Manager) advertise(ctx context.Context) {
	// Advertise with service UUID and a short name marker
	// Metadata will be read via GATT characteristic read (requires connection)
	advertiseName := "P2P"

	if err := ble.AdvertiseNameAndServices(ctx, advertiseName, m.serviceUUID); err != nil {
		log.Printf("bluetooth: advertise stopped: %v", err)
		m.Available = false
	}
}

func (m *Manager) scan(ctx context.Context) {
	filter := func(a ble.Advertisement) bool {
		// Accept any advertisement with our service UUID (don't require connectable)
		// We can read metadata without connecting
		return hasService(a, m.serviceUUID)
	}

	for {
		err := ble.Scan(ctx, false, m.handleAdvertisement, filter)
		if err != nil && ctx.Err() == nil {
			log.Printf("bluetooth: scan error: %v", err)
			m.Available = false
			time.Sleep(time.Second)
		}

		if ctx.Err() != nil {
			return
		}
	}
}

func (m *Manager) handleAdvertisement(a ble.Advertisement) {
	defer func() {
		if r := recover(); r != nil {
			// Handle UUID parsing panics gracefully (e.g., "invalid UUID string: DAF55501")
			log.Printf("bluetooth: recovered from panic in handleAdvertisement: %v", r)
		}
	}()

	// Try to read metadata from advertisement data without connecting
	var metaPayload string

	// First, try Service Data (most reliable for our use case)
	if serviceData := a.ServiceData(); len(serviceData) > 0 {
		// Try Service Data
		for _, sd := range serviceData {
			if sd.UUID.Equal(m.serviceUUID) {
				// Service data contains UUID + metadata payload
				// Extract metadata after UUID (16 bytes)
				data := sd.Data
				if len(data) > 16 {
					metaPayload = string(data[16:])
				} else {
					// Fallback: try to decode as base64 or use as-is
					decoded, err := base64.URLEncoding.DecodeString(string(data))
					if err == nil {
						metaPayload = string(decoded)
					} else {
						metaPayload = string(data)
					}
				}
				break
			}
		}
	}

	// Fallback: try Manufacturer Data
	if metaPayload == "" {
		if mData := a.ManufacturerData(); len(mData) > 0 {
			// Try to decode as base64 first, then as plain string
			decoded, err := base64.URLEncoding.DecodeString(string(mData))
			if err == nil {
				metaPayload = string(decoded)
			} else {
				metaPayload = string(mData)
			}
		}
	}

	// If we found metadata in advertisement, parse it directly
	if metaPayload != "" {
		meta, err := parseMetadata(metaPayload)
		if err == nil {
			// Ignore our own advertisements
			if meta.PubKeyStr == m.proto.PublicKeyStr {
				return
			}

			// Decode public key from base64
			pubKeyBytes, err := base64.StdEncoding.DecodeString(meta.PubKeyStr)
			if err != nil || len(pubKeyBytes) != 32 {
				return
			}

			peerID := crypto.PeerID(pubKeyBytes)
			peer := &entity.Peer{
				PeerID:    peerID,
				PublicKey: pubKeyBytes,
				Port:      meta.Port,
				Messages:  make([]*entity.Message, 0),
				BLEAddr:   a.Addr().String(),
				Username:  meta.Username,
			}
			peer.AddConnectionType(entity.ConnectionBLE)

			log.Printf("bluetooth: discovered BLE peer %s at %s", peerID, a.Addr().String())
			m.proto.Peers.Add(peer)
			return
		}
	}

	// If no metadata found in advertisement, try reading from characteristic
	// This requires a connection but is reliable
	if !a.Connectable() {
		// Only log verbose if we haven't seen this peer recently to avoid spam,
		// but for now we want to debug why we aren't connecting
		// log.Printf("bluetooth: device %s not connectable", a.Addr())
		return
	}

	log.Printf("bluetooth: found connectable device %s, attempting connection to read metadata...", a.Addr())

	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	client, err := ble.Dial(ctx, a.Addr())
	if err != nil {
		log.Printf("bluetooth: failed to dial device %s: %v", a.Addr(), err)
		return
	}
	defer client.CancelConnection()

	log.Printf("bluetooth: connected to %s, looking for characteristic...", a.Addr())

	characteristic, err := m.findMetaCharacteristic(ctx, client)
	if err != nil || characteristic == nil {
		log.Printf("bluetooth: failed to find characteristic on %s: %v", a.Addr(), err)
		return
	}

	data, err := client.ReadCharacteristic(characteristic)
	if err != nil || len(data) == 0 {
		log.Printf("bluetooth: failed to read characteristic from %s: %v", a.Addr(), err)
		return
	}

	log.Printf("bluetooth: successfully read metadata from %s (len=%d)", a.Addr(), len(data))

	meta, err := parseMetadata(string(data))
	if err != nil {
		log.Printf("bluetooth: failed to parse metadata from %s: %v", a.Addr(), err)
		return
	}

	// Ignore our own advertisements.
	if meta.PubKeyStr == m.proto.PublicKeyStr {
		return
	}

	// Decode public key from base64
	pubKeyBytes, err := base64.StdEncoding.DecodeString(meta.PubKeyStr)
	if err != nil || len(pubKeyBytes) != 32 {
		return
	}

	peerID := crypto.PeerID(pubKeyBytes)
	peer := &entity.Peer{
		PeerID:    peerID,
		PublicKey: pubKeyBytes,
		Port:      meta.Port,
		Messages:  make([]*entity.Message, 0),
		BLEAddr:   a.Addr().String(),
		Username:  meta.Username,
	}
	peer.AddConnectionType(entity.ConnectionBLE)

	log.Printf("bluetooth: discovered BLE peer %s at %s", peerID, a.Addr().String())
	m.proto.Peers.Add(peer)
}

func (m *Manager) findMetaCharacteristic(ctx context.Context, client ble.Client) (*ble.Characteristic, error) {
	profile, err := client.DiscoverProfile(true)
	if err != nil {
		return nil, err
	}

	for _, s := range profile.Services {
		if !s.UUID.Equal(m.serviceUUID) {
			continue
		}
		for _, c := range s.Characteristics {
			if c.UUID.Equal(m.metaUUID) {
				return c, nil
			}
		}
	}

	return nil, fmt.Errorf("metadata characteristic not found")
}

func (m *Manager) addService() error {
	service := ble.NewService(m.serviceUUID)

	metaChar := ble.NewCharacteristic(m.metaUUID)
	metaChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		_, _ = rsp.Write([]byte(m.metadataPayload()))
	}))

	service.AddCharacteristic(metaChar)
	return ble.AddService(service)
}

func (m *Manager) metadataPayload() string {
	// Include username in metadata
	return strings.Join([]string{
		m.proto.PublicKeyStr,
		m.proto.Port,
		m.proto.Username,
	}, "|")
}

type metadata struct {
	PubKeyStr string
	Port      string
	Username  string
}

func parseMetadata(payload string) (*metadata, error) {
	parts := strings.Split(payload, "|")
	// Support both old format (2 fields) and new format (3 fields with username)
	if len(parts) < 2 || len(parts) > 3 {
		return nil, fmt.Errorf("invalid metadata payload")
	}

	meta := &metadata{
		PubKeyStr: parts[0],
		Port:      parts[1],
	}

	// Username is optional (3rd field)
	if len(parts) == 3 {
		meta.Username = parts[2]
	}

	return meta, nil
}

func hasService(a ble.Advertisement, uuid ble.UUID) bool {
	defer func() {
		if r := recover(); r != nil {
			// Handle UUID parsing panics gracefully
			log.Printf("bluetooth: recovered from UUID panic in hasService: %v", r)
		}
	}()

	for _, srv := range a.Services() {
		if srv.Equal(uuid) {
			return true
		}
	}
	return false
}
