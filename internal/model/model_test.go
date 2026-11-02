package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretsNeverMarshal(t *testing.T) {
	for _, v := range []any{User{PasswordHash: "secret-marker"}, Session{Hash: []byte("secret-marker"), ExpectedPasswordHash: "secret-marker"}, DWSConnection{WorkspaceCiphertext: []byte("secret-marker")}, Checkpoint{CursorCiphertext: []byte("secret-marker"), CLITokenCiphertext: []byte("secret-marker")}, Attachment{Data: []byte("secret-marker")}} {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		s := string(b)
		if strings.Contains(s, "secret-marker") || strings.Contains(s, "c2VjcmV0LW1hcmtlcg==") {
			t.Fatalf("secret leaked from %T", v)
		}
	}
}
