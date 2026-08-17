package agent

import (
	"crypto/rand"
	"fmt"
)

// NewSessionID generates an RFC 4122 v4 UUID for --session-id. Claude
// Code needs the ID before launch so tend can store it up front rather
// than discovering it after the fact; the stdlib has no UUID type, and
// this is the entire surface tend needs, so it's hand-rolled rather than
// pulling in a dependency for one function.
func NewSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating session id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
