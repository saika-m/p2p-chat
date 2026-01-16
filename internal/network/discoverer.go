package network

import (
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"time"

	"p2p-messenger/internal/crypto"
	"p2p-messenger/internal/entity"
	"p2p-messenger/internal/proto"
	"p2p-messenger/pkg/udp"
)

const (
	udpConnectionBufferSize = 1024
	multicastString         = "me0w"
)

type Discoverer struct {
	Addr               *net.UDPAddr
	MulticastFrequency time.Duration
	Proto              *proto.Proto
}

func NewDiscoverer(addr *net.UDPAddr, multicastFrequency time.Duration, proto *proto.Proto) *Discoverer {
	return &Discoverer{
		Addr:               addr,
		MulticastFrequency: multicastFrequency,
		Proto:              proto,
	}
}

func (d *Discoverer) Start() {
	go d.startMulticasting()
	go d.listenMulticasting()
}

func (d *Discoverer) startMulticasting() {
	var conn *net.UDPConn
	var err error

	// Retry connection setup
	for {
		conn, err = net.DialUDP("udp", nil, d.Addr)
		if err == nil {
			break
		}
		log.Printf("discoverer: failed to create multicast connection: %v, retrying...", err)
		time.Sleep(2 * time.Second)
	}

	ticker := time.NewTicker(d.MulticastFrequency)
	defer ticker.Stop()

	for {
		<-ticker.C
		// Include username in multicast message
		// Format: multicastString:PubKeyStr:Port:Username
		msg := fmt.Sprintf("%s:%s:%s:%s",
			multicastString,
			d.Proto.PublicKeyStr,
			d.Proto.Port,
			d.Proto.Username)

		_, err := conn.Write([]byte(msg))
		if err != nil {
			log.Printf("discoverer: multicast write error: %v, attempting to reconnect...", err)
			conn.Close()
			// Retry connection
			for {
				conn, err = net.DialUDP("udp", nil, d.Addr)
				if err == nil {
					log.Printf("discoverer: reconnected successfully")
					break
				}
				log.Printf("discoverer: reconnection failed: %v, retrying...", err)
				time.Sleep(2 * time.Second)
			}
		}
	}
}

func (d *Discoverer) listenMulticasting() {
	var conn *net.UDPConn
	var err error

	// Retry connection setup
	for {
		conn, err = net.ListenMulticastUDP("udp", nil, d.Addr)
		if err == nil {
			break
		}
		log.Printf("discoverer: failed to listen on multicast: %v, retrying...", err)
		time.Sleep(2 * time.Second)
	}

	err = conn.SetReadBuffer(udpConnectionBufferSize)
	if err != nil {
		log.Printf("discoverer: warning: failed to set read buffer: %v", err)
		// Continue anyway, buffer size is not critical
	}

	for {
		rawBytes, addr, err := udp.ReadFromUDPConnection(conn, udpConnectionBufferSize)
		if err != nil {
			log.Printf("discoverer: read error: %v, attempting to reconnect...", err)
			conn.Close()
			// Retry connection
			for {
				conn, err = net.ListenMulticastUDP("udp", nil, d.Addr)
				if err == nil {
					err = conn.SetReadBuffer(udpConnectionBufferSize)
					if err != nil {
						log.Printf("discoverer: warning: failed to set read buffer after reconnect: %v", err)
					}
					log.Printf("discoverer: reconnected successfully")
					break
				}
				log.Printf("discoverer: reconnection failed: %v, retrying...", err)
				time.Sleep(2 * time.Second)
			}
			continue
		}

		message, err := entity.UDPMulticastMessageToPeer(rawBytes)
		if err != nil {
			// Log parse errors occasionally (not every time to avoid spam)
			log.Printf("discoverer: failed to parse multicast message: %v, from %s", err, addr.IP.String())
			continue // Skip invalid messages, don't crash
		}

		log.Printf("discoverer: discovered peer from %s (Port=%s, Username=%s)", addr.IP.String(), message.Port, message.Username)

		// Decode public key from base64
		pubKeyBytes, err := base64.StdEncoding.DecodeString(message.PubKeyStr)
		if err != nil || len(pubKeyBytes) != 32 {
			log.Printf("discoverer: invalid public key format (not base64 or wrong length): %v", err)
			continue
		}

		peerID := crypto.PeerID(pubKeyBytes)
		peer := &entity.Peer{
			PeerID:    peerID,
			PublicKey: pubKeyBytes,
			Port:      message.Port,
			Messages:  make([]*entity.Message, 0),
			AddrIP:    addr.IP.String(),
			Username:  message.Username,
		}
		peer.AddConnectionType(entity.ConnectionNAT)

		if message.PubKeyStr != d.Proto.PublicKeyStr {
			d.Proto.Peers.Add(peer)
		}
	}
}
