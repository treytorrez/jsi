package token

import (
	"errors"
	"strings"
	"testing"
)

func TestAlphabet(t *testing.T) {
	if len(Alphabet) != 32 {
		t.Fatalf("Alphabet has %d chars, want 32", len(Alphabet))
	}
	seen := make(map[rune]bool, len(Alphabet))
	for _, c := range Alphabet {
		if seen[c] {
			t.Errorf("Alphabet contains duplicate %q", c)
		}
		seen[c] = true
	}
	for _, c := range "ILOU" {
		if strings.ContainsRune(Alphabet, c) {
			t.Errorf("Alphabet must not contain confusable/forbidden %q", c)
		}
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error // sentinel matched with errors.Is; nil = success
	}{
		// Valid canonical input passes through unchanged.
		{"canonical", "7KQX2A", "7KQX2A", nil},
		{"all digits", "012345", "012345", nil},
		{"all letters", "HJKMNP", "HJKMNP", nil},
		{"high letters", "VWXYZ0", "VWXYZ0", nil},
		// Case-insensitive input (D4): lowercase and mixed case uppercase.
		{"lowercase", "7kqx2a", "7KQX2A", nil},
		{"mixed case", "7kQx2A", "7KQX2A", nil},
		// Crockford confusables map after case folding.
		{"confusables upper", "ILOILO", "110110", nil},
		{"confusables lower", "iloilo", "110110", nil},
		{"confusables mixed with digits", "OiL01l", "011011", nil},
		// Surrounding whitespace is trimmed.
		{"space padded", "  7KQX2A ", "7KQX2A", nil},
		{"tab newline padded", "\t7KQX2A\n", "7KQX2A", nil},
		// Wrong lengths.
		{"too short 5", "7KQX2", "", ErrLength},
		{"too long 7", "7KQX2AB", "", ErrLength},
		{"empty", "", "", ErrLength},
		{"only spaces", "   ", "", ErrLength},
		// Invalid characters (post-mapping).
		{"letter U forbidden", "7KQX2U", "", ErrAlphabet},
		{"letter u forbidden", "7kqx2u", "", ErrAlphabet},
		{"punctuation", "7KQX2!", "", ErrAlphabet},
		{"hyphen", "7KQ-2A", "", ErrAlphabet},
		{"internal space", "7KQ XA", "", ErrAlphabet},
		{"non-ascii", "7KQX2é", "", ErrAlphabet},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Normalize(tt.in)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Normalize(%q) error: %v", tt.in, err)
				}
				if got != tt.want {
					t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
				}
				if !Validate(got) {
					t.Errorf("Normalize(%q) = %q is not canonical per Validate", tt.in, got)
				}
				return
			}
			if err == nil {
				t.Fatalf("Normalize(%q) = %q, want error %v", tt.in, got, tt.wantErr)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Normalize(%q) error = %v, want errors.Is %v", tt.in, err, tt.wantErr)
			}
			if got != "" {
				t.Errorf("Normalize(%q) returned %q alongside error, want \"\"", tt.in, got)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"7KQX2A", true},
		{"000000", true},
		{"ZZZZZZ", true},
		{"110110", true},
		{"7kqx2a", false},  // not uppercase
		{"7KQX2O", false},  // O not in alphabet
		{"7KQX2I", false},  // I not in alphabet
		{"7KQX2L", false},  // L not in alphabet
		{"7KQX2U", false},  // U not in alphabet
		{"", false},        // empty
		{"7KQX2", false},   // too short
		{"7KQX2AB", false}, // too long
		{" 7KQX2A", false}, // padded is not canonical
		{"7KQX2A ", false}, // padded is not canonical
		{"7KQ-2A", false},  // punctuation
		{"7KQX2🚀", false},  // non-ascii
	}
	for _, tt := range tests {
		if got := Validate(tt.in); got != tt.want {
			t.Errorf("Validate(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestGenerate(t *testing.T) {
	const samples = 1000
	seen := make(map[string]struct{}, samples)
	for i := 0; i < samples; i++ {
		tok, err := Generate()
		if err != nil {
			t.Fatalf("Generate() error: %v", err)
		}
		if !Validate(tok) {
			t.Fatalf("Generate() = %q is not canonical", tok)
		}
		if norm, err := Normalize(tok); err != nil || norm != tok {
			t.Fatalf("Normalize(Generate()) = %q, %v; want %q, nil", norm, err, tok)
		}
		seen[tok] = struct{}{}
	}
	if len(seen) != samples {
		t.Errorf("Generate() produced %d duplicates in %d samples", samples-len(seen), samples)
	}
}
