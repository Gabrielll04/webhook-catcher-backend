package access

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

type Validator struct {
	requireAccess bool
	expectedAud   string
}

func NewValidator(requireAccess bool, expectedAud string) Validator {
	return Validator{requireAccess: requireAccess, expectedAud: expectedAud}
}

func (v Validator) Validate(headers map[string]string) bool {
	if !v.requireAccess {
		return true
	}
	jwt := headers["cf-access-jwt-assertion"]
	if jwt == "" {
		if headers["cf-access-authenticated-user-email"] != "" {
			return true
		}
		if headers["cf-access-client-id"] != "" && headers["cf-access-client-secret"] != "" {
			return true
		}
		return false
	}
	return validateJWTClaims(jwt, v.expectedAud)
}

func validateJWTClaims(raw, expectedAud string) bool {
	parts := strings.Split(raw, ".")
	if len(parts) < 2 {
		return false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return false
	}

	if exp, ok := claims["exp"].(float64); ok {
		if int64(exp) < time.Now().Unix() {
			return false
		}
	}

	if expectedAud == "" {
		return true
	}
	aud, ok := claims["aud"]
	if !ok {
		return false
	}
	switch a := aud.(type) {
	case string:
		return a == expectedAud
	case []any:
		for _, item := range a {
			if s, ok := item.(string); ok && s == expectedAud {
				return true
			}
		}
	}
	return false
}
