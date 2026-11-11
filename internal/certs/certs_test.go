package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateCAHashes(t *testing.T) {
	dir := t.TempDir()
	for _, h := range []string{"MD5", "SHA1", "SHA256"} {
		crt, key := filepath.Join(dir, h+".crt"), filepath.Join(dir, h+".key")
		if err := CreateCA(crt, key, "", h, time.Now().Add(-time.Hour), time.Now().AddDate(10, 0, 0)); err != nil {
			t.Fatalf("%s: %v", h, err)
		}
		ca, err := LoadCA(crt, key)
		if err != nil {
			t.Fatalf("%s: %v", h, err)
		}
		if got := HashOf(ca.Cert); got != h {
			t.Errorf("CA made with %s is signed with %s", h, got)
		}
		if !ca.Cert.IsCA {
			t.Errorf("%s: not a CA", h)
		}
		is := &Issuer{CA: ca, Hash: h, ValidBeforeNow: -7, ValidAfterNow: 7}
		c, err := is.Get("www.example.com")
		if err != nil {
			t.Fatalf("%s leaf: %v", h, err)
		}
		if HashOf(c.Leaf) != h {
			t.Errorf("leaf made with %s is signed with %s", h, HashOf(c.Leaf))
		}
		if len(c.Certificate) != 2 {
			t.Errorf("chain has %d certificates", len(c.Certificate))
		}
		if h == "SHA256" {
			pool := x509.NewCertPool()
			pool.AddCert(ca.Cert)
			if _, err := c.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "www.example.com"}); err != nil {
				t.Errorf("leaf does not verify: %v", err)
			}
		}
		again, _ := is.Get("www.example.com")
		if again != c {
			t.Error("certificate not cached")
		}
	}
}

func writePEM(t *testing.T, path, typ string, der []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), mode); err != nil {
		t.Fatal(err)
	}
}

// bbsCA makes a directory as "bbs ca" and "bbs ca subca proxy" leave it.
func bbsCA(t *testing.T, withSub bool) string {
	dir := t.TempDir()
	rootKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	root := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "D-Modem BBS Root CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(20, 0, 0),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, BasicConstraintsValid: true, IsCA: true, MaxPathLen: 1,
		SignatureAlgorithm: x509.SHA256WithRSA,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, _ = x509.ParseCertificate(rootDER)
	writePEM(t, filepath.Join(dir, "ca.crt"), "CERTIFICATE", rootDER, 0o644)
	writePEM(t, filepath.Join(dir, "ca.key"), "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rootKey), 0o600)
	if !withSub {
		return dir
	}
	subKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	sub := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "D-Modem BBS proxy CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true, MaxPathLen: 0, MaxPathLenZero: true,
		SignatureAlgorithm: x509.SHA256WithRSA,
	}
	subDER, err := x509.CreateCertificate(rand.Reader, sub, root, &subKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, filepath.Join(dir, "proxy-ca.crt"), "CERTIFICATE", subDER, 0o644)
	writePEM(t, filepath.Join(dir, "proxy-ca.key"), "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(subKey), 0o600)
	return dir
}

func TestBBSSubCA(t *testing.T) {
	dir := bbsCA(t, true)
	signer, root, err := BBSCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if signer == root || signer.Cert.Subject.CommonName != "D-Modem BBS proxy CA" || root.Key != nil {
		t.Fatalf("signer %q, root %q", signer.Cert.Subject.CommonName, root.Cert.Subject.CommonName)
	}
	is := &Issuer{CA: signer, Hash: HashOf(signer.Cert), ValidBeforeNow: -7, ValidAfterNow: 3650}
	c, err := is.Get("example.org")
	if err != nil {
		t.Fatal(err)
	}
	if d := c.Leaf.NotAfter.Sub(c.Leaf.NotBefore); d > 397*24*time.Hour {
		t.Errorf("leaf is valid for %v", d)
	}
	// a client that has only the root installed trusts the leaf, given
	// the sub-CA the proxy sends after it
	inter, _ := x509.ParseCertificate(c.Certificate[1])
	roots, inters := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(root.Cert)
	inters.AddCert(inter)
	if _, err := c.Leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inters, DNSName: "example.org"}); err != nil {
		t.Errorf("chain does not verify: %v", err)
	}
}

func TestBBSRootOnly(t *testing.T) {
	signer, root, err := BBSCA(bbsCA(t, false))
	if err != nil {
		t.Fatal(err)
	}
	if signer != root || signer.Key == nil {
		t.Error("a directory without a sub-CA uses the root itself")
	}
	if _, _, err := BBSCA(t.TempDir()); err == nil {
		t.Error("an empty directory gave a CA")
	}
}

func TestSplitArgs(t *testing.T) {
	got := SplitArgs(`-i "a file.png" -resize 50% out.gif`)
	want := []string{"-i", "a file.png", "-resize", "50%", "out.gif"}
	if len(got) != len(want) {
		t.Fatalf("%q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %q", i, got[i])
		}
	}
}
