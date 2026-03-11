package id

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"math/big"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func NewULID(t time.Time) (string, error) {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := rand.Read(b[6:]); err != nil {
		return "", err
	}

	n := new(big.Int).SetBytes(b[:])
	mod := new(big.Int)
	divisor := big.NewInt(32)
	chars := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		n.DivMod(n, divisor, mod)
		chars[i] = crockford[mod.Int64()]
	}
	if chars[0] > '7' {
		return "", errors.New("ulid overflow")
	}
	return string(chars), nil
}

func NewToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
