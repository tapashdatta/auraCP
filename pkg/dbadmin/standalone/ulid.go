package standalone

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"
)

// ULID — Crockford-base32 encoding of a 48-bit millisecond timestamp +
// 80 bits of crypto/rand entropy. Lexically sortable; collision-resistant
// at realistic write rates.
//
// Format (26 chars): TTTTTTTTTT RRRRRRRRRRRRRRRR  (timestamp + randomness)
//
// This implementation is intentionally tiny — we don't depend on
// github.com/oklog/ulid because adding a dependency for ~80 lines isn't
// worth it given our usage pattern (one ULID per audit event, one per
// user/session/connection).

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	// ulidMu guards entropyState during a monotonic burst within the
	// same millisecond.
	ulidMu        sync.Mutex
	ulidLastMS    uint64
	ulidLastEntr  [10]byte
)

// NewULID returns a monotonic ULID derived from the current time.
func NewULID() string {
	return NewULIDAt(time.Now())
}

// NewULIDAt returns a ULID with the given timestamp. Two calls within
// the same millisecond produce monotonically increasing identifiers by
// incrementing the random tail.
//
// SEC-13 hardening:
//   - Clock-step-backward: if the caller's t resolves to a millisecond
//     earlier than the previously-minted ms, we clamp ms forward to
//     ulidLastMS so monotonicity is preserved across NTP step-back /
//     suspend-resume.
//   - Same-ms rollover: incrementing the 10-byte entropy as a
//     big-endian integer used to silently cascade past 0xFF without
//     freshening — losing entropy and producing predictable tails.
//     Now, when the increment overflows (every byte was 0xFF before
//     the bump), we draw a fresh entropy block AND bump ms by 1 so
//     two ULIDs in the same overflowing burst remain ordered.
func NewULIDAt(t time.Time) string {
	ms := uint64(t.UnixMilli())
	var entropy [10]byte

	ulidMu.Lock()
	// Clamp forward against clock step-back.
	if ms < ulidLastMS {
		ms = ulidLastMS
	}
	if ms == ulidLastMS && (ulidLastMS != 0 || ulidLastEntr != [10]byte{}) {
		// Same-ms call: increment last entropy as a big-endian integer.
		entropy = ulidLastEntr
		overflow := true
		for i := 9; i >= 0; i-- {
			entropy[i]++
			if entropy[i] != 0 {
				overflow = false
				break
			}
		}
		if overflow {
			// Cascaded past 0xFF...FF — bump ms and re-seed.
			ms++
			if _, err := io.ReadFull(rand.Reader, entropy[:]); err != nil {
				ulidMu.Unlock()
				panic("standalone: crypto/rand failed during ULID mint: " + err.Error())
			}
		}
	} else {
		if _, err := io.ReadFull(rand.Reader, entropy[:]); err != nil {
			ulidMu.Unlock()
			panic("standalone: crypto/rand failed during ULID mint: " + err.Error())
		}
	}
	ulidLastMS = ms
	ulidLastEntr = entropy
	ulidMu.Unlock()

	// Pack 6 bytes of timestamp + 10 bytes of entropy.
	var raw [16]byte
	binary.BigEndian.PutUint64(raw[:8], ms<<16)
	// Bytes 6..15 hold entropy. We overwrote raw[6:8] above with the
	// low bytes of ms<<16 (which are zero), so just copy entropy into
	// raw[6:16].
	copy(raw[6:], entropy[:])

	return encodeULID(raw)
}

// encodeULID converts 16 bytes into 26 Crockford base32 characters.
func encodeULID(raw [16]byte) string {
	// 16 bytes = 128 bits; encoded as 26 base32 chars (130 bits, top 2
	// bits always zero). We compute each character by shifting a sliding
	// window across the bit stream.
	out := make([]byte, 26)
	// Helper: get bit i of raw (0 = MSB of raw[0]).
	bit := func(i int) byte {
		if i < 0 {
			return 0
		}
		return (raw[i/8] >> (7 - i%8)) & 1
	}
	// Pad to 130 bits by treating bits -2 and -1 as zero.
	for c := 0; c < 26; c++ {
		var v byte
		base := c*5 - 2 // first bit position of this character
		for k := 0; k < 5; k++ {
			v = (v << 1) | bit(base+k)
		}
		out[c] = crockford[v]
	}
	return string(out)
}

// ParseULID returns the millisecond timestamp embedded in s. Returns an
// error if s is not a valid 26-char Crockford base32 string.
func ParseULID(s string) (time.Time, error) {
	if len(s) != 26 {
		return time.Time{}, errors.New("ulid: invalid length")
	}
	var raw [16]byte
	for c, ch := range []byte(s) {
		v := crockfordDecode(ch)
		if v == 0xff {
			return time.Time{}, errors.New("ulid: invalid character")
		}
		// Write 5 bits at position c*5 - 2 (mirror of encodeULID).
		base := c*5 - 2
		for k := 0; k < 5; k++ {
			pos := base + k
			if pos < 0 {
				continue
			}
			bit := (v >> (4 - k)) & 1
			raw[pos/8] |= bit << (7 - pos%8)
		}
	}
	ms := binary.BigEndian.Uint64(raw[:8]) >> 16
	return time.UnixMilli(int64(ms)).UTC(), nil
}

func crockfordDecode(b byte) byte {
	switch {
	case b >= '0' && b <= '9':
		return b - '0'
	case b >= 'A' && b <= 'H':
		return b - 'A' + 10
	case b == 'J':
		return 18
	case b == 'K':
		return 19
	case b == 'M':
		return 20
	case b == 'N':
		return 21
	case b == 'P':
		return 22
	case b == 'Q':
		return 23
	case b == 'R':
		return 24
	case b == 'S':
		return 25
	case b == 'T':
		return 26
	case b == 'V':
		return 27
	case b == 'W':
		return 28
	case b == 'X':
		return 29
	case b == 'Y':
		return 30
	case b == 'Z':
		return 31
	// Lowercase tolerated.
	case b >= 'a' && b <= 'h':
		return b - 'a' + 10
	case b == 'j':
		return 18
	case b == 'k':
		return 19
	case b == 'm':
		return 20
	case b == 'n':
		return 21
	case b == 'p':
		return 22
	case b == 'q':
		return 23
	case b == 'r':
		return 24
	case b == 's':
		return 25
	case b == 't':
		return 26
	case b == 'v':
		return 27
	case b == 'w':
		return 28
	case b == 'x':
		return 29
	case b == 'y':
		return 30
	case b == 'z':
		return 31
	}
	return 0xff
}
