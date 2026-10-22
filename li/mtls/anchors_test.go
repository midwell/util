// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// selfSignedLeaf returns a self-signed certificate that is not a CA, as cert and key PEM, valid
// from notBefore to notAfter.
func selfSignedLeaf(t *testing.T, cn string, notBefore, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// TestALeafInTheCABundleDoesNotAuthenticateItsHolder: a certificate in the trust pool is
// trusted as it stands, and Go accepts a peer presenting it as a chain of one. A ca.crt that
// also carried a leaf therefore let that leaf's holder through mutual TLS on every LI
// interface, with no CA having issued it for that. Only CA certificates may be anchors.
func TestALeafInTheCABundleDoesNotAuthenticateItsHolder(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, caPEM := newCA(t, "LI Test CA")
	neCert, neKey := newLeaf(t, "ne", ca, caKey)
	strayCert, strayKey := selfSignedLeaf(t, "stray", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	bundle := append(append([]byte{}, caPEM...), strayCert...)
	m := writeMaterial(t, dir, neCert, neKey, bundle)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", m.ServerTLS())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	// The server's own verdict, not the client's: under TLS 1.3 a client learns of the
	// refusal only on a later read, and a closed connection reads EOF either way.
	verdict := make(chan error, 1)
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr != nil {
			verdict <- acceptErr

			return
		}
		verdict <- c.(*tls.Conn).HandshakeContext(t.Context())
		_ = c.Close()
	}()

	stray, err := tls.X509KeyPair(strayCert, strayKey)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &tls.Dialer{Config: &tls.Config{
		Certificates: []tls.Certificate{stray},
		RootCAs:      m.caPool,
		MinVersion:   tls.VersionTLS12,
	}}
	if conn, dialErr := dialer.DialContext(t.Context(), "tcp", ln.Addr().String()); dialErr == nil {
		_ = conn.Close()
	}

	if err := <-verdict; err == nil {
		t.Error("the server completed mutual TLS with a peer presenting a leaf that sat in the " +
			"CA bundle: the leaf was trusted as an anchor")
	}
}

// TestABundleWithNoCAIsRefused: with every non-CA entry skipped, a bundle holding only leaves
// has no anchor at all, and Load says so rather than building a pool that trusts nothing.
func TestABundleWithNoCAIsRefused(t *testing.T) {
	dir := t.TempDir()
	ca, caKey, _ := newCA(t, "LI Test CA")
	neCert, neKey := newLeaf(t, "ne", ca, caKey)
	onlyLeaf, _ := selfSignedLeaf(t, "stray", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	cp, kp, ap := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), filepath.Join(dir, "ca.crt")
	for p, b := range map[string][]byte{cp: neCert, kp: neKey, ap: onlyLeaf} {
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Load(cp, kp, ap); err == nil || !strings.Contains(err.Error(), "no CA") {
		t.Errorf("Load of a bundle holding only a leaf = %v, want a refusal naming the missing CA", err)
	}
}

// TestAnExpiredCertificateIsRefusedAtLoad: an expired NE certificate fails every handshake, and
// the failure surfaces only at the peers. Load refuses it where the operator is looking.
func TestAnExpiredCertificateIsRefusedAtLoad(t *testing.T) {
	dir := t.TempDir()
	_, _, caPEM := newCA(t, "LI Test CA")
	expired, key := selfSignedLeaf(t, "ne", time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))

	cp, kp, ap := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), filepath.Join(dir, "ca.crt")
	for p, b := range map[string][]byte{cp: expired, kp: key, ap: caPEM} {
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Load(cp, kp, ap); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("Load of an expired certificate = %v, want a refusal saying it expired", err)
	}
}
