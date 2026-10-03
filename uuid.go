package honk

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewIdempotencyKey returns a new UUIDv7 (RFC 9562): 48-bit Unix milliseconds, then random
// bits. Store it with your job if you want to retry the same event across process restarts.
func NewIdempotencyKey() string { return uuidv7(time.Now()) }

func uuidv7(t time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("honk: crypto/rand failed: " + err.Error())
	}
	ms := uint64(t.UnixMilli())
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	b[6] = b[6]&0x0f | 0x70 // version 7
	b[8] = b[8]&0x3f | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
