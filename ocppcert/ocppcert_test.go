// SPDX-License-Identifier: MIT

package ocppcert

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"
	"time"
)

func certs(t *testing.T) (leaf, ca *x509.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CA"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ = x509.ParseCertificate(der)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(0x0a0b0c), Subject: pkix.Name{CommonName: "CS1"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ = x509.ParseCertificate(der)
	return leaf, ca
}

func TestHash(t *testing.T) {
	leaf, ca := certs(t)
	h, err := Hash(leaf, ca, crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	nameHash := sha256.Sum256(ca.RawSubject) // the leaf's issuer is the CA's subject
	// For P-256 the subject public key is the uncompressed point, 65 bytes.
	pub := ca.PublicKey.(*ecdsa.PublicKey)
	point, _ := pub.Bytes()
	keyHash := sha256.Sum256(point)
	want := HashData{HashAlgorithm: "SHA256", IssuerNameHash: hex.EncodeToString(nameHash[:]),
		IssuerKeyHash: hex.EncodeToString(keyHash[:]), SerialNumber: "a0b0c"}
	if h != want {
		t.Fatalf("got  %+v\nwant %+v", h, want)
	}
	for _, alg := range []crypto.Hash{crypto.SHA384, crypto.SHA512} {
		h, err := Hash(leaf, ca, alg)
		if err != nil || len(h.IssuerNameHash) != alg.Size()*2 || len(h.IssuerNameHash) > 128 {
			t.Fatalf("%v: %+v %v", alg, h, err)
		}
	}
	if _, err := Hash(leaf, ca, crypto.SHA1); !errors.Is(err, ErrUnsupportedHash) {
		t.Fatalf("SHA-1: %v", err)
	}
	if _, err := Hash(nil, ca, crypto.SHA256); err == nil {
		t.Fatal("nil certificate accepted")
	}
	// A root: its own issuer.
	if _, err := Hash(ca, ca, crypto.SHA256); err != nil {
		t.Fatal(err)
	}
}
