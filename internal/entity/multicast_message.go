package entity

import (
	b "bytes"
	"errors"
	"strings"
)

const (
	nullByte = "\x00"
)

var (
	ErrBadMulticastMessage = errors.New("ErrorBadMulticastMessage")
)

type MulticastMessage struct {
	MulticastString string
	PubKeyStr       string
	Port            string
	Username        string
}

func UDPMulticastMessageToPeer(bytes []byte) (*MulticastMessage, error) {
	bytes = b.Trim(bytes, nullByte)
	array := strings.Split(string(bytes), ":")

	// Support both old format (3 fields) and new format (4 fields with username)
	if len(array) < 3 || len(array) > 4 {
		return nil, ErrBadMulticastMessage
	}

	msg := &MulticastMessage{
		MulticastString: array[0],
		PubKeyStr:       array[1],
		Port:            array[2],
	}
	
	// Username is optional (4th field)
	if len(array) == 4 {
		msg.Username = array[3]
	}

	return msg, nil
}
