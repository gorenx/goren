package apiproxy

import (
	"crypto/rand"
	"fmt"

	"github.com/gorenx/goren/connection"
)

func mintFrameRPCID() (connection.RPCID, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	randomBytes[6] = randomBytes[6]&0x0f | 0x40
	randomBytes[8] = randomBytes[8]&0x3f | 0x80
	return connection.RPCID(fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		randomBytes[0:4], randomBytes[4:6], randomBytes[6:8], randomBytes[8:10], randomBytes[10:16],
	)), nil
}
