package determinism

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	ebuserrors "github.com/Project-Helianthus/helianthus-ebusgo/errors"
)

// CanonicalJSON returns a deterministic JSON encoding suitable for stable hashing.
// It normalizes values through a JSON round-trip so map ordering and numeric
// representation become stable for equivalent payloads.
func CanonicalJSON(value any) ([]byte, error) {
	normalized, err := CanonicalClone(value)
	if err != nil {
		return nil, err
	}

	canonical, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("canonical marshal failed: %w: %v", ebuserrors.ErrInvalidPayload, err)
	}
	return canonical, nil
}

// CanonicalClone deep-clones a JSON-compatible value into a deterministic
// representation (maps with sorted keys on marshal and json.Number numerics).
func CanonicalClone(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonical marshal source failed: %w: %v", ebuserrors.ErrInvalidPayload, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var out any
	if err := decoder.Decode(&out); err != nil {
		return nil, fmt.Errorf("canonical decode failed: %w: %v", ebuserrors.ErrInvalidPayload, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("canonical decode found trailing data: %w", ebuserrors.ErrInvalidPayload)
	}
	return out, nil
}

func CanonicalHash(value any) (string, error) {
	canonical, err := CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
