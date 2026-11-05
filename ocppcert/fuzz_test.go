// SPDX-License-Identifier: MIT

package ocppcert

import (
	"crypto"
	"crypto/x509"
	"encoding/hex"
	"math/big"
	"testing"
)

// FuzzHash feeds Hash certificates whose issuer public key, issuer name and
// serial number are arbitrary bytes, as a hostile peer's or a corrupt
// store's certificate could carry. Hash must not panic, and what it
// returns must be hexadecimal of the hash's size, with the serial number
// in canonical form.
func FuzzHash(f *testing.F) {
	leaf, ca := certs(f)
	f.Add(ca.RawSubjectPublicKeyInfo, leaf.RawIssuer, leaf.SerialNumber.Bytes(), false, uint8(0))
	f.Add([]byte{0x30, 0x03, 0x01}, []byte{}, []byte{}, true, uint8(1))
	f.Add([]byte{0x30, 0x80}, []byte{0x30}, []byte{0, 0, 1}, false, uint8(2))
	f.Fuzz(func(t *testing.T, spki, rawIssuer, serial []byte, negative bool, algb uint8) {
		alg := []crypto.Hash{crypto.SHA256, crypto.SHA384, crypto.SHA512, crypto.SHA1}[algb%4]
		n := new(big.Int).SetBytes(serial)
		if negative {
			n.Neg(n)
		}
		c := &x509.Certificate{RawIssuer: rawIssuer, SerialNumber: n}
		issuer := &x509.Certificate{RawSubjectPublicKeyInfo: spki}
		h, err := Hash(c, issuer, alg)
		if err != nil {
			return
		}
		for _, s := range []string{h.IssuerNameHash, h.IssuerKeyHash} {
			if b, err := hex.DecodeString(s); err != nil || len(b) != alg.Size() {
				t.Fatalf("hash %q for %v", s, alg)
			}
		}
		got, ok := new(big.Int).SetString(h.SerialNumber, 16)
		if !ok || got.Cmp(n) != 0 || (len(h.SerialNumber) > 1 && h.SerialNumber[0] == '0') {
			t.Fatalf("serial %q for %v", h.SerialNumber, n)
		}
	})
}
