package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"unicode/utf16"
)

// ReadTokenFile reads a token from path. The file is read as bytes: a UTF-8
// BOM is stripped, a UTF-16 LE/BE BOM is detected and decoded, then
// whitespace, CR and LF are trimmed at both ends. A token that still contains
// anything outside printable ASCII (0x21-0x7E) is an error that names the file
// but never its content.
func ReadTokenFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		// os errors carry the path and the OS reason, never file content.
		return "", fmt.Errorf("FORGEJO_TOKEN_FILE: %w", err)
	}
	tok, err := DecodeToken(raw)
	if err != nil {
		return "", fmt.Errorf("FORGEJO_TOKEN_FILE %s: %w", path, err)
	}
	return tok, nil
}

// DecodeToken applies the token-file rules of ReadTokenFile to raw bytes.
func DecodeToken(raw []byte) (string, error) {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		raw = raw[3:]
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		s, err := decodeUTF16(raw[2:], false)
		if err != nil {
			return "", err
		}
		raw = []byte(s)
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		s, err := decodeUTF16(raw[2:], true)
		if err != nil {
			return "", err
		}
		raw = []byte(s)
	}
	raw = bytes.TrimFunc(raw, func(r rune) bool {
		switch r {
		case ' ', '\t', '\r', '\n', '\v', '\f':
			return true
		}
		return false
	})
	if len(raw) == 0 {
		return "", errors.New("file is empty")
	}
	for _, b := range raw {
		if b < 0x21 || b > 0x7E {
			return "", errors.New("file contains non-printable or non-ASCII bytes after decoding (is it a plain-text token?)")
		}
	}
	return string(raw), nil
}

func decodeUTF16(b []byte, bigEndian bool) (string, error) {
	if len(b)%2 != 0 {
		return "", errors.New("invalid UTF-16 content (odd byte count)")
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		if bigEndian {
			u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			u[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	return string(utf16.Decode(u)), nil
}
