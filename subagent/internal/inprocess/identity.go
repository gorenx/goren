package inprocess

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/gorenx/goren/session"
)

func newSessionID() (session.SessionID, error) {
	var randomBytes [16]byte
	if _, readErr := rand.Read(randomBytes[:]); readErr != nil {
		return "", fmt.Errorf("subagent: generate child identity: %w", readErr)
	}
	randomBytes[6] = randomBytes[6]&0x0f | 0x40
	randomBytes[8] = randomBytes[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], randomBytes[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], randomBytes[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], randomBytes[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], randomBytes[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], randomBytes[10:16])
	return session.SessionID(encoded), nil
}
