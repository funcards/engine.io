package eio

import (
	"crypto/rand"
	"encoding/base32"
	"io"
	"time"
)

var b32enc = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

func generateSidBytes(length int) []byte { // length > 7
	now := uint64(time.Now().UnixNano() / int64(time.Millisecond))
	k := make([]byte, length)
	k[0] = byte(now >> 40)
	k[1] = byte(now >> 32)
	k[2] = byte(now >> 24)
	k[3] = byte(now >> 16)
	k[4] = byte(now >> 8)
	k[5] = byte(now)
	_, _ = io.ReadFull(rand.Reader, k[6:])
	return k
}

func NewSID() string {
	sid := generateSidBytes(16)
	return b32enc.EncodeToString(sid)
}
