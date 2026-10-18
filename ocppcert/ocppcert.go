// SPDX-License-Identifier: MIT

// Package ocppcert computes the certificate identifiers OCPP messages carry:
// CertificateHashData, which GetInstalledCertificateIds, DeleteCertificate,
// GetCertificateStatus and the 1.6 Security Whitepaper messages use to name
// a certificate without sending it. It keeps no certificates and needs
// nothing but the standard library; storing certificates is the
// application's.
package ocppcert

import (
	"crypto"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"fmt"

	// The hash algorithms OCPP names.
	_ "crypto/sha256"
	_ "crypto/sha512"
)

// HashData is CertificateHashDataType (OCPP 2.0.1 and 2.1 Part 2 §2.6;
// 1.6 Security Whitepaper): copy its fields into the version package's
// type, e.g. ocpp201.CertificateHashDataType{HashAlgorithm:
// ocpp201.HashAlgorithmEnumType(h.HashAlgorithm), IssuerNameHash:
// h.IssuerNameHash, ...}.
type HashData struct {
	// HashAlgorithm is "SHA256", "SHA384" or "SHA512".
	HashAlgorithm string `json:"hashAlgorithm"`
	// IssuerNameHash is the hash of the DER encoding of the issuer's name
	// in the certificate, in hexadecimal.
	IssuerNameHash string `json:"issuerNameHash"`
	// IssuerKeyHash is the hash of the issuer's public key: the value of
	// the subject public key field of the issuer's certificate, without
	// tag and length, in hexadecimal. As in OCSP (RFC 6960 §4.1.1), the
	// bit string's bytes are hashed without its unused-bits octet.
	IssuerKeyHash string `json:"issuerKeyHash"`
	// SerialNumber is the certificate's serial number in hexadecimal,
	// without "0x" and without leading zeroes.
	SerialNumber string `json:"serialNumber"`
}

// ErrUnsupportedHash is returned for a hash other than SHA-256, SHA-384
// and SHA-512, the ones HashAlgorithmEnumType defines.
var ErrUnsupportedHash = errors.New("ocppcert: hash algorithm not defined by OCPP")

var hashNames = map[crypto.Hash]string{crypto.SHA256: "SHA256", crypto.SHA384: "SHA384", crypto.SHA512: "SHA512"}

// Hash returns the HashData of cert, issued by issuer, with the hash alg.
// For a self-signed (root) certificate, issuer is cert.
func Hash(cert, issuer *x509.Certificate, alg crypto.Hash) (HashData, error) {
	name, ok := hashNames[alg]
	if !ok {
		return HashData{}, fmt.Errorf("%w: %v", ErrUnsupportedHash, alg)
	}
	if cert == nil || issuer == nil {
		return HashData{}, errors.New("ocppcert: nil certificate")
	}
	var spki struct {
		Algorithm pkix.AlgorithmIdentifier
		PublicKey asn1.BitString
	}
	if rest, err := asn1.Unmarshal(issuer.RawSubjectPublicKeyInfo, &spki); err != nil || len(rest) > 0 {
		return HashData{}, fmt.Errorf("ocppcert: issuer public key: %v", err)
	}
	sum := func(b []byte) string {
		h := alg.New()
		h.Write(b)
		return hex.EncodeToString(h.Sum(nil))
	}
	return HashData{
		HashAlgorithm:  name,
		IssuerNameHash: sum(cert.RawIssuer),
		IssuerKeyHash:  sum(spki.PublicKey.RightAlign()),
		SerialNumber:   cert.SerialNumber.Text(16),
	}, nil
}
