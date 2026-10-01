package proto

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

// Crockford base32 alphabet, as used by ULID.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NowMS returns the current Unix time in milliseconds, the protocol's only
// timestamp format.
func NowMS() int64 { return time.Now().UnixMilli() }

var (
	idMu   sync.Mutex
	idLast int64
	idSeq  [10]byte
)

// NewID returns a 26-character ULID: 48-bit millisecond timestamp followed by
// 80 random bits, Crockford base32 encoded. It is monotonic within a process,
// so IDs sort by creation order, which makes logs and traces readable.
func NewID() string {
	idMu.Lock()
	defer idMu.Unlock()

	now := time.Now().UnixMilli()
	if now == idLast {
		// Same millisecond: increment the random part to preserve ordering.
		for i := 9; i >= 0; i-- {
			idSeq[i]++
			if idSeq[i] != 0 {
				break
			}
		}
	} else {
		idLast = now
		if _, err := rand.Read(idSeq[:]); err != nil {
			// crypto/rand failure is not recoverable in any useful way here;
			// fall back to a time-derived sequence rather than panicking.
			binary.BigEndian.PutUint64(idSeq[2:], uint64(now))
		}
	}

	var b [16]byte
	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)
	copy(b[6:], idSeq[:])

	return encodeCrockford(b)
}

// encodeCrockford renders 128 bits as 26 base32 characters.
func encodeCrockford(b [16]byte) string {
	var out [26]byte
	// 26 chars * 5 bits = 130 bits; the top char carries 2 bits of the first byte.
	out[0] = crockford[(b[0]&0xE0)>>5]
	out[1] = crockford[b[0]&0x1F]
	out[2] = crockford[(b[1]&0xF8)>>3]
	out[3] = crockford[((b[1]&0x07)<<2)|((b[2]&0xC0)>>6)]
	out[4] = crockford[(b[2]&0x3E)>>1]
	out[5] = crockford[((b[2]&0x01)<<4)|((b[3]&0xF0)>>4)]
	out[6] = crockford[((b[3]&0x0F)<<1)|((b[4]&0x80)>>7)]
	out[7] = crockford[(b[4]&0x7C)>>2]
	out[8] = crockford[((b[4]&0x03)<<3)|((b[5]&0xE0)>>5)]
	out[9] = crockford[b[5]&0x1F]
	out[10] = crockford[(b[6]&0xF8)>>3]
	out[11] = crockford[((b[6]&0x07)<<2)|((b[7]&0xC0)>>6)]
	out[12] = crockford[(b[7]&0x3E)>>1]
	out[13] = crockford[((b[7]&0x01)<<4)|((b[8]&0xF0)>>4)]
	out[14] = crockford[((b[8]&0x0F)<<1)|((b[9]&0x80)>>7)]
	out[15] = crockford[(b[9]&0x7C)>>2]
	out[16] = crockford[((b[9]&0x03)<<3)|((b[10]&0xE0)>>5)]
	out[17] = crockford[b[10]&0x1F]
	out[18] = crockford[(b[11]&0xF8)>>3]
	out[19] = crockford[((b[11]&0x07)<<2)|((b[12]&0xC0)>>6)]
	out[20] = crockford[(b[12]&0x3E)>>1]
	out[21] = crockford[((b[12]&0x01)<<4)|((b[13]&0xF0)>>4)]
	out[22] = crockford[((b[13]&0x0F)<<1)|((b[14]&0x80)>>7)]
	out[23] = crockford[(b[14]&0x7C)>>2]
	out[24] = crockford[((b[14]&0x03)<<3)|((b[15]&0xE0)>>5)]
	out[25] = crockford[b[15]&0x1F]
	return string(out[:])
}
