package adapterproto_test

import (
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestIdentifierRules(t *testing.T) {
	type v interface{ Validate() error }
	tests := []struct {
		name string
		val  v
		ok   bool
	}{
		{"endpoint ref of one character", adapterproto.EndpointRef("e"), true},
		{"endpoint ref of 128 characters", adapterproto.EndpointRef(repeat("e", 128)), true},
		{"endpoint ref of 129 characters", adapterproto.EndpointRef(repeat("e", 129)), false},
		{"endpoint ref empty", adapterproto.EndpointRef(""), false},
		{"endpoint ref counts characters, not bytes", adapterproto.EndpointRef(repeat("é", 128)), true},
		{"address of 128 characters", adapterproto.Address(repeat("a", 128)), true},
		{"address of 129 characters", adapterproto.Address(repeat("a", 129)), false},
		{"message id empty", adapterproto.MessageID(""), false},
		{"message id of 128", adapterproto.MessageID(repeat("m", 128)), true},
		{"native ref of 256", adapterproto.NativeRef(repeat("n", 256)), true},
		{"native ref of 257", adapterproto.NativeRef(repeat("n", 257)), false},
		{"turn ref of 256", adapterproto.TurnRef(repeat("t", 256)), true},
		{"turn ref empty", adapterproto.TurnRef(""), false},
		{"attempt id", adapterproto.AttemptID(attempt1), true},
		{"attempt id with an upper-case digit", adapterproto.AttemptID("6f1c2a4e-8b3d-4c5a-9e7f-0a1b2c3d4e5F"), false},
		{"attempt id of the nil UUID", adapterproto.AttemptID("00000000-0000-0000-0000-000000000000"), false},
		{"attempt id with braces", adapterproto.AttemptID("{" + attempt1 + "}"), false},
		{"attempt id with a urn prefix", adapterproto.AttemptID("urn:uuid:" + attempt1), false},
		{"lease", adapterproto.LeaseRef(lease1), true},
		{"lease of the highest value", adapterproto.LeaseRef(repeat("f", 32)), true},
		{"lease all zero", adapterproto.LeaseRef(repeat("0", 32)), false},
		{"lease with one non-zero digit", adapterproto.LeaseRef(repeat("0", 31) + "1"), true},
		{"token", adapterproto.Token(token1), true},
		{"token ending in A", adapterproto.Token(token1[:42] + "A"), true},
		{"token with the 16 canonical last characters", adapterproto.Token(token1[:42] + "8"), true},
		{"token with a non-canonical last character", adapterproto.Token(token1[:42] + "B"), false},
		{"token of 42 characters", adapterproto.Token(token1[:42]), false},
		{"token with a plus sign", adapterproto.Token(strings.Replace(token1, "k", "+", 1)), false},
		{"token with a slash", adapterproto.Token(strings.Replace(token1, "k", "/", 1)), false},
		{"token with an underscore and a dash", adapterproto.Token("-_" + token1[2:]), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.val.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// TestTokenCanonicalLastCharacters: of the 64 characters that could end a
// 43-character token, exactly the 16 whose low two bits are zero encode 32 bytes.
func TestTokenCanonicalLastCharacters(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	canonical := ""
	for i, c := range alphabet {
		if i%4 == 0 {
			canonical += string(c)
		}
	}
	for _, c := range alphabet {
		err := adapterproto.Token(token1[:42] + string(c)).Validate()
		if want := strings.ContainsRune(canonical, c); (err == nil) != want {
			t.Errorf("last character %q: valid=%v, want %v", c, err == nil, want)
		}
	}
}

func TestPresetNameAndKeyRules(t *testing.T) {
	for s, ok := range map[string]bool{
		"default": true, "a": true, "0": true, "a.b": true, "a-b": true, "a_b": true, "review.readonly": true, repeat("a", 64): true,
		"": false, "A": false, ".a": false, "-a": false, "_a": false, "a b": false, "a/b": false, repeat("a", 65): false, "é": false, "a\n": false,
	} {
		if err := adapterproto.ValidatePresetName(s); (err == nil) != ok {
			t.Errorf("ValidatePresetName(%q) = %v, want ok=%v", s, err, ok)
		}
	}
	for s, ok := range map[string]bool{
		"home": true, "a": true, "a1_b": true, "a" + repeat("b", 63): true,
		"": false, "A": false, "1a": false, "_a": false, "a-b": false, "a.b": false, "a" + repeat("b", 64): false,
	} {
		if err := adapterproto.ValidateConfigKey(s); (err == nil) != ok {
			t.Errorf("ValidateConfigKey(%q) = %v, want ok=%v", s, err, ok)
		}
	}
	for s, ok := range map[string]bool{
		"example": true, "a": true, "0": true, "chat-bridge": true, "a" + repeat("b", 31): true,
		"": false, "-a": false, "A": false, "a_b": false, "a.b": false, "a" + repeat("b", 32): false,
	} {
		if err := adapterproto.ValidateAdapterID(s); (err == nil) != ok {
			t.Errorf("ValidateAdapterID(%q) = %v, want ok=%v", s, err, ok)
		}
	}
}
