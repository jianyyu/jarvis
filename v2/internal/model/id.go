package model

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID generates an 8-character hex token.
func NewID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic("failed to generate random ID: " + err.Error())
	}
	return hex.EncodeToString(b)
}
