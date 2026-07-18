package token

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

// Alphabet is the Crockford Base32 alphabet (no I, L, O, U) — proto/SIGNALING.md
// §Token, PLAN.md decision D4.
const Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Length is the exact number of characters in a session token.
const Length = 6

var (
	// ErrLength is returned by Normalize when the trimmed input does not
	// contain exactly Length characters.
	ErrLength = errors.New("token: wrong length")
	// ErrAlphabet is returned by Normalize when a character is not in
	// Alphabet after case folding and confusable mapping.
	ErrAlphabet = errors.New("token: character not in Crockford Base32 alphabet")
)

// Generate returns a new random token, uniform over Alphabet^Length, using
// crypto/rand. (SP/1 sessions are minted by the worker per D3; Generate exists
// for local uses such as tests and offline channels.)
func Generate() (string, error) {
	var b [Length]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("token: generate: %w", err)
	}
	for i, v := range b {
		// len(Alphabet) == 32 divides 256, so masking 5 bits is uniform.
		b[i] = Alphabet[v&31]
	}
	return string(b[:]), nil
}

// Normalize converts user input to canonical token form: it trims surrounding
// whitespace, uppercases, and maps Crockford confusables (i/I/l/L → '1',
// o/O → '0'). The result must be exactly Length characters, all in Alphabet.
// On success it returns the canonical uppercase form; on failure a descriptive
// error wrapping ErrLength or ErrAlphabet (match with errors.Is).
func Normalize(s string) (string, error) {
	r := []rune(strings.ToUpper(strings.TrimSpace(s)))
	if len(r) != Length {
		return "", fmt.Errorf("%w: have %d characters, want %d", ErrLength, len(r), Length)
	}
	for i, c := range r {
		switch c {
		case 'I', 'L':
			c = '1'
		case 'O':
			c = '0'
		}
		if !strings.ContainsRune(Alphabet, c) {
			return "", fmt.Errorf("%w: %q at position %d", ErrAlphabet, c, i)
		}
		r[i] = c
	}
	return string(r), nil
}

// Validate reports whether s is already in canonical form: exactly Length
// characters, all uppercase members of Alphabet.
func Validate(s string) bool {
	if len(s) != Length {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(Alphabet, rune(s[i])) {
			return false
		}
	}
	return true
}
