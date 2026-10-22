// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

// Package mtls loads the Lawful Interception PKI credentials and builds the
// mutual-TLS configurations for the X1/X2/X3 interfaces.
//
// LI requires mutual TLS with certificate verification on every interface
// (ETSI TS 103 221-1), using credentials from the operator's LI PKI — kept
// separate from the general SBI certificates for isolation. The credentials
// (this network element's LI certificate + key, and the LI CA trust anchor)
// are pre-provisioned out of band before any X1 tasking, corresponding to the
// ETSI TS 104 000 (X0) step; here that means the deployment places them at
// configured file paths (e.g. a dedicated, access-restricted Kubernetes
// Secret). Unlike SD-Core's SBI TLS, verification is never skipped.
//
// **There is no reload.** Load reads the files once and the configurations it returns
// hold what it read, so rotating the certificate or the trust anchor takes effect only
// when the network function restarts. A certificate that expires in service makes every
// X1, X2 and X3 handshake fail, and the failure is visible only at the peer.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"
)

// Material holds the loaded LI PKI credentials for one network element.
type Material struct {
	cert   tls.Certificate
	caPool *x509.CertPool
}

// Load reads this network element's LI certificate and key, and the LI CA
// trust anchor, from the given file paths (X0-pre-provisioned). All three are
// required — LI has no insecure/verification-skipped mode.
//
// A certificate that has already expired is refused here, where the operator is
// looking, rather than discovered handshake by handshake at every peer.
func Load(certPath, keyPath, caCertPath string) (*Material, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("mtls: load LI certificate/key: %w", err)
	}
	leaf := cert.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return nil, fmt.Errorf("mtls: parse LI certificate: %w", err)
		}
	}
	if now := time.Now(); now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("mtls: LI certificate %q expired at %s",
			leaf.Subject.CommonName, leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("mtls: read LI CA certificate: %w", err)
	}
	pool, err := caPool(caPEM)
	if err != nil {
		return nil, fmt.Errorf("mtls: LI CA file %q: %w", caCertPath, err)
	}
	return &Material{cert: cert, caPool: pool}, nil
}

// caPool builds the trust anchors from a PEM bundle, admitting only CA certificates.
//
// x509.CertPool.AppendCertsFromPEM admits any certificate, and a certificate in the
// pool is trusted as it stands: verification accepts a peer presenting it as a
// complete chain of one. A bundle that carried a leaf — a misassembled ca.crt, say —
// therefore made that leaf's holder trusted on every LI interface without any CA
// having issued it for that. Such entries are skipped, and a bundle with no CA at all
// is refused.
func caPool(bundle []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	anchors, skipped := 0, 0

	for rest := bundle; ; {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if !c.IsCA || !c.BasicConstraintsValid {
			skipped++
			continue
		}
		pool.AddCert(c)
		anchors++
	}

	if anchors == 0 {
		// Say why: a root without the extension (an X.509 v1 certificate, or one issued
		// with no extensions configured) is skipped by the rule above, and "no CA" alone
		// would send an operator looking at the wrong file.
		return nil, fmt.Errorf("contains no CA certificate: %d certificate(s) skipped for "+
			"lacking basicConstraints with CA:TRUE", skipped)
	}
	return pool, nil
}

// ServerTLS returns the TLS config for the X1 listener: it presents this NE's
// LI certificate and requires and verifies a client (ADMF) certificate issued
// by the LI CA. A peer without a valid LI certificate is rejected at handshake.
func (m *Material) ServerTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.cert},
		ClientCAs:    m.caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
}

// ClientTLS returns the TLS config for delivering X2 (xIRI) and X3 (xCC) to an
// MDF: it presents this NE's LI certificate and verifies the MDF's server
// certificate against the LI CA (verification is never skipped).
func (m *Material) ClientTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.cert},
		RootCAs:      m.caPool,
		MinVersion:   tls.VersionTLS12,
	}
}
