// Package certs is WebOne's certificate authority: the CA certificate the
// proxy makes (or is given), and the per-site certificates it signs with it
// on the fly for HTTPS clients, in the old hash algorithms (MD5, SHA-1)
// that old browsers need.
//
// Ported from WebOne's CertificateUtil.cs. Go's crypto/x509 will not sign
// with MD5, so MD5 signatures are made here by hand.
package certs

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	mrand "math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CA is a certificate authority: its certificate and key.
type CA struct {
	Cert *x509.Certificate
	Key  crypto.Signer
	PEM  []byte // the certificate, PEM
}

// DER is the CA certificate as DER.
func (ca *CA) DER() []byte { return ca.Cert.Raw }

// ParseSubject reads a distinguished name as "CN=x,OU=y,O=z,C=w".
func ParseSubject(dn string) pkix.Name {
	var n pkix.Name
	for _, part := range splitDN(dn) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k, v = strings.ToUpper(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "CN":
			n.CommonName = v
		case "OU":
			n.OrganizationalUnit = append(n.OrganizationalUnit, v)
		case "O":
			n.Organization = append(n.Organization, v)
		case "C":
			n.Country = append(n.Country, v)
		case "L":
			n.Locality = append(n.Locality, v)
		case "S", "ST":
			n.Province = append(n.Province, v)
		case "STREET":
			n.StreetAddress = append(n.StreetAddress, v)
		}
	}
	return n
}

func splitDN(dn string) []string {
	var parts []string
	var b strings.Builder
	quoted, esc := false, false
	for _, r := range dn {
		switch {
		case esc:
			b.WriteRune(r)
			esc = false
		case r == '\\':
			esc = true
		case r == '"':
			quoted = !quoted
		case (r == ',' || r == ';') && !quoted:
			parts = append(parts, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	return append(parts, b.String())
}

// sigAlg is the x509 algorithm for a hash name and key.
func sigAlg(hash string, key crypto.Signer) x509.SignatureAlgorithm {
	_, isEC := key.Public().(*ecdsa.PublicKey)
	switch strings.ToUpper(hash) {
	case "MD5":
		if isEC {
			return x509.ECDSAWithSHA1
		}
		return x509.MD5WithRSA
	case "SHA1", "SHA":
		if isEC {
			return x509.ECDSAWithSHA1
		}
		return x509.SHA1WithRSA
	case "SHA384":
		if isEC {
			return x509.ECDSAWithSHA384
		}
		return x509.SHA384WithRSA
	case "SHA512":
		if isEC {
			return x509.ECDSAWithSHA512
		}
		return x509.SHA512WithRSA
	}
	if isEC {
		return x509.ECDSAWithSHA256
	}
	return x509.SHA256WithRSA
}

var (
	oidSHA1WithRSA = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 5}
	oidMD5WithRSA  = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 4}
)

// createCertificate is x509.CreateCertificate, but also signs with MD5:
// it makes a SHA-1 certificate, then puts MD5's OID in its place (the
// same length) and signs the result again.
func createCertificate(tmpl, parent *x509.Certificate, pub any, priv crypto.Signer) ([]byte, error) {
	if tmpl.SignatureAlgorithm != x509.MD5WithRSA {
		return x509.CreateCertificate(rand.Reader, tmpl, parent, pub, priv)
	}
	rsaKey, ok := priv.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("MD5 signatures need an RSA key")
	}
	t := *tmpl
	t.SignatureAlgorithm = x509.SHA1WithRSA
	der, err := x509.CreateCertificate(rand.Reader, &t, parent, pub, priv)
	if err != nil {
		return nil, err
	}
	var cert struct {
		TBS asn1.RawValue
		Alg pkix.AlgorithmIdentifier
		Sig asn1.BitString
	}
	if _, err := asn1.Unmarshal(der, &cert); err != nil {
		return nil, err
	}
	sha1OID, _ := asn1.Marshal(oidSHA1WithRSA)
	md5OID, _ := asn1.Marshal(oidMD5WithRSA)
	tbs := bytes.Replace(cert.TBS.FullBytes, sha1OID, md5OID, 1)
	if bytes.Equal(tbs, cert.TBS.FullBytes) {
		return nil, errors.New("cannot find the signature algorithm in the certificate")
	}
	sum := md5.Sum(tbs)
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.MD5, sum[:])
	if err != nil {
		return nil, err
	}
	cert.TBS = asn1.RawValue{FullBytes: tbs}
	cert.Alg = pkix.AlgorithmIdentifier{Algorithm: oidMD5WithRSA, Parameters: asn1.NullRawValue}
	cert.Sig = asn1.BitString{Bytes: sig, BitLength: len(sig) * 8}
	return asn1.Marshal(cert)
}

// CreateCA makes a self-signed CA certificate and its RSA 2048 key, and
// writes them as PEM to certFile and keyFile.
func CreateCA(certFile, keyFile, subject, hash string, notBefore, notAfter time.Time) error {
	n := mrand.Int64N(900) + 100
	if subject == "" || strings.Contains(subject, "%number%") {
		if subject == "" {
			subject = "CN=WebOne Certificate Authority %number%,OU=This is not really secure connection,O=MITM Proxy,C=SU"
		}
		subject = strings.ReplaceAll(subject, "%number%", strconv.FormatInt(n, 10))
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	name := ParseSubject(subject)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(n),
		Subject:               name,
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            12,
		SignatureAlgorithm:    sigAlg(hash, key),
	}
	der, err := createCertificate(tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(keyFile); dir != "" {
		os.MkdirAll(dir, 0o755)
	}
	kp := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyFile, kp, 0o600); err != nil {
		return err
	}
	cp := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return os.WriteFile(certFile, cp, 0o644)
}

// LoadCert reads a certificate (PEM or DER) without its key: a root that
// only has to be shown to clients.
func LoadCert(certFile string) (*CA, error) {
	cp, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	der := cp
	for rest := cp; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type == "CERTIFICATE" {
			der = b.Bytes
			break
		}
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", certFile, err)
	}
	return &CA{Cert: cert, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})}, nil
}

// HashOf is the hash name (as SslHashAlgorithm takes it) of a
// certificate's signature.
func HashOf(c *x509.Certificate) string {
	switch c.SignatureAlgorithm {
	case x509.MD5WithRSA:
		return "MD5"
	case x509.SHA1WithRSA, x509.ECDSAWithSHA1, x509.DSAWithSHA1:
		return "SHA1"
	case x509.SHA384WithRSA, x509.ECDSAWithSHA384, x509.SHA384WithRSAPSS:
		return "SHA384"
	case x509.SHA512WithRSA, x509.ECDSAWithSHA512, x509.SHA512WithRSAPSS:
		return "SHA512"
	}
	return "SHA256"
}

// BBSCA is the CA of a D-Modem BBS directory (bbs ca): the proxy signs
// with the sub-CA proxy-ca.crt/.key (made by "bbs ca subca proxy"), and
// clients install the root ca.crt. A directory with no sub-CA but a
// readable ca.key is used directly.
func BBSCA(dir string) (signer, root *CA, err error) {
	rootCert := filepath.Join(dir, "ca.crt")
	sub, subErr := LoadCA(filepath.Join(dir, "proxy-ca.crt"), filepath.Join(dir, "proxy-ca.key"))
	if subErr == nil {
		r, err := LoadCert(rootCert)
		if err != nil {
			return nil, nil, err
		}
		if err := sub.Cert.CheckSignatureFrom(r.Cert); err != nil {
			return nil, nil, fmt.Errorf("proxy-ca.crt is not signed by ca.crt in %s: %v", dir, err)
		}
		return sub, r, nil
	}
	if !os.IsNotExist(subErr) {
		return nil, nil, fmt.Errorf("cannot read the BBS proxy sub-CA in %s: %v (run the proxy as the BBS user, or give it read access)", dir, subErr)
	}
	ca, err := LoadCA(rootCert, filepath.Join(dir, "ca.key"))
	if err != nil {
		return nil, nil, fmt.Errorf("no BBS proxy sub-CA in %s (make it with \"bbs ca subca proxy\"): %v", dir, err)
	}
	return ca, ca, nil
}

// LoadCA reads a CA certificate and key (PEM; the key PKCS#1, PKCS#8 or EC).
func LoadCA(certFile, keyFile string) (*CA, error) {
	cp, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	kp, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	var cb *pem.Block
	for rest := cp; ; {
		cb, rest = pem.Decode(rest)
		if cb == nil || cb.Type == "CERTIFICATE" {
			break
		}
	}
	if cb == nil {
		return nil, fmt.Errorf("%s: not a PEM certificate", certFile)
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := parseKey(kp)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", keyFile, err)
	}
	return &CA{Cert: cert, Key: key, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})}, nil
}

func parseKey(kp []byte) (crypto.Signer, error) {
	for {
		b, rest := pem.Decode(kp)
		if b == nil {
			return nil, errors.New("no PEM private key")
		}
		kp = rest
		switch b.Type {
		case "RSA PRIVATE KEY":
			return x509.ParsePKCS1PrivateKey(b.Bytes)
		case "EC PRIVATE KEY":
			return x509.ParseECPrivateKey(b.Bytes)
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
			if err != nil {
				return nil, err
			}
			s, ok := k.(crypto.Signer)
			if !ok {
				return nil, errors.New("unsupported private key type")
			}
			return s, nil
		}
	}
}

// Issuer makes and caches the certificates of the sites clients connect to.
type Issuer struct {
	CA *CA
	// Hash: MD5, SHA1, SHA256, SHA384 or SHA512
	Hash string
	// days around now a site certificate is good for
	ValidBeforeNow, ValidAfterNow int
	// SiteCerts is a path (with %Subject%) where site certificates may be
	// found as .crt and .key files; Generator is a command that makes them.
	SiteCerts, Generator string
	// Expand expands variables (and %Subject%) in SiteCerts and Generator.
	Expand func(s, subject string) string
	// Log reports external generator problems.
	Log func(format string, args ...any)

	mu    sync.Mutex
	cache map[string]*tls.Certificate
	key   *rsa.PrivateKey
}

// Get is the certificate for host (cached until it runs out).
func (is *Issuer) Get(host string) (*tls.Certificate, error) {
	subject := "CN=" + host
	is.mu.Lock()
	if is.cache == nil {
		is.cache = map[string]*tls.Certificate{}
	}
	if c, ok := is.cache[subject]; ok {
		if now := time.Now(); now.After(c.Leaf.NotBefore) && now.Before(c.Leaf.NotAfter) {
			is.mu.Unlock()
			return c, nil
		}
		delete(is.cache, subject)
	}
	is.mu.Unlock()

	if c, err := is.external(subject); err != nil {
		return nil, err
	} else if c != nil {
		return c, nil
	}

	c, err := is.issue(host)
	if err != nil {
		return nil, err
	}
	is.mu.Lock()
	if old, ok := is.cache[subject]; ok {
		c = old // keep one serial per subject (Firefox objects to two)
	} else {
		is.cache[subject] = c
	}
	is.mu.Unlock()
	return c, nil
}

// external looks for the site's certificate on disk, or has the external
// generator make it.
func (is *Issuer) external(subject string) (*tls.Certificate, error) {
	if strings.TrimSpace(is.SiteCerts) == "" && strings.TrimSpace(is.Generator) == "" {
		return nil, nil
	}
	expand := is.Expand
	if expand == nil {
		expand = func(s, sub string) string { return strings.ReplaceAll(s, "%Subject%", sub) }
	}
	base := expand(is.SiteCerts, subject)
	crt, key := base+".crt", base+".key"
	load := func() *tls.Certificate {
		if fileExists(crt) && fileExists(key) {
			if c, err := tls.LoadX509KeyPair(crt, key); err == nil {
				if c.Leaf == nil {
					c.Leaf, _ = x509.ParseCertificate(c.Certificate[0])
				}
				return &c
			}
		}
		return nil
	}
	if strings.TrimSpace(is.SiteCerts) != "" {
		if c := load(); c != nil {
			return c, nil
		}
	}
	if g := strings.TrimSpace(is.Generator); g != "" {
		prog, args, _ := strings.Cut(g, " ")
		args = expand(args, subject)
		if prog != "" && strings.TrimSpace(args) != "" {
			cmd := exec.Command(prog, SplitArgs(args)...)
			if err := cmd.Run(); err != nil && is.Log != nil {
				is.Log(" Certificate generator failed: %v", err)
			}
			if c := load(); c != nil {
				return c, nil
			}
			if is.Log != nil {
				is.Log(" No certificate and/or private key file were produced: %s (.key)!", crt)
			}
		}
	}
	return nil, nil
}

func (is *Issuer) issue(host string) (*tls.Certificate, error) {
	is.mu.Lock()
	if is.key == nil {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			is.mu.Unlock()
			return nil, err
		}
		is.key = k
	}
	key := is.key
	is.mu.Unlock()

	serial := make([]byte, 16)
	rand.Read(serial)
	serial[0] &= 0x7f
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:       new(big.Int).SetBytes(serial),
		Subject:            pkix.Name{CommonName: host},
		NotBefore:          now.AddDate(0, 0, is.ValidBeforeNow),
		NotAfter:           now.AddDate(0, 0, is.ValidAfterNow),
		SignatureAlgorithm: sigAlg(is.Hash, is.CA.Key),
	}
	if is.ValidAfterNow == 0 {
		tmpl.NotAfter = now.AddDate(0, 0, 7)
	}
	// today's browsers take no site certificate longer than 397 days
	if limit := tmpl.NotBefore.AddDate(0, 0, 397); tmpl.NotAfter.After(limit) {
		tmpl.NotAfter = limit
	}
	if tmpl.NotAfter.After(is.CA.Cert.NotAfter) {
		tmpl.NotAfter = is.CA.Cert.NotAfter
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := createCertificate(tmpl, is.CA.Cert, &key.PublicKey, is.CA.Key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, is.CA.Cert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// SplitArgs splits a command line on spaces, keeping "quoted parts" whole.
func SplitArgs(s string) []string {
	var out []string
	var b strings.Builder
	quoted, any := false, false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			any = true
		case (r == ' ' || r == '\t') && !quoted:
			if any {
				out = append(out, b.String())
				b.Reset()
				any = false
			}
		default:
			b.WriteRune(r)
			any = true
		}
	}
	if any {
		out = append(out, b.String())
	}
	return out
}
