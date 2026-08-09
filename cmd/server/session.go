package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func temporarySessionID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err == nil {
		return prefix + "-" + hex.EncodeToString(b)
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
