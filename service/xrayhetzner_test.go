package service

import "testing"

// TestParseVlessencPair фиксирует парсинг реального вывода `xray vlessenc`:
// команда печатает несколько секций (X25519 и Post-Quantum ML-KEM среди
// них), EnsureXDNSKeys должен брать именно первую пару из секции X25519.
func TestParseVlessencPair(t *testing.T) {
	out := `Choose one Authentication to use, do not mix them. Ephemeral key exchange is Post-Quantum safe anyway.

Authentication: X25519, not Post-Quantum
"decryption": "mlkem768x25519plus.native.600s.AAAADEC"
"encryption": "mlkem768x25519plus.native.0rtt.AAAAENC"

Authentication: ML-KEM-768, Post-Quantum
"decryption": "mlkem768x25519plus.native.600s.MLKEMDEC"
"encryption": "mlkem768x25519plus.native.0rtt.MLKEMENC"
`
	dec, enc, err := parseVlessencPair(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec != "mlkem768x25519plus.native.600s.AAAADEC" {
		t.Errorf("expected X25519 decryption, got %q", dec)
	}
	if enc != "mlkem768x25519plus.native.0rtt.AAAAENC" {
		t.Errorf("expected X25519 encryption, got %q", enc)
	}
}

func TestParseVlessencPair_NoX25519Section(t *testing.T) {
	if _, _, err := parseVlessencPair("garbage output without the expected section"); err == nil {
		t.Fatal("expected error when X25519 section is absent")
	}
}
