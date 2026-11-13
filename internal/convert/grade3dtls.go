package convert

// A live grading's HTTPS side: a headset's browser allows WebXR on a
// secure page only, and a page on a LAN address is not one over plain
// HTTP. So the grading also serves over TLS, on the next port, with a
// certificate it makes for itself (this machine's addresses), kept in
// the user's cache so a browser's acceptance of it holds between runs.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// gradeTLS is a TLS configuration with a self-signed certificate for
// this machine's addresses: the one in the cache when it covers them,
// else a new one, kept there.
func gradeTLS() (*tls.Config, error) {
	ips := localIPs()
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "bdtools")
	crt, key := filepath.Join(dir, "grade-tls.crt"), filepath.Join(dir, "grade-tls.key")
	if c, err := tls.LoadX509KeyPair(crt, key); err == nil && certCovers(c, ips) {
		return &tls.Config{Certificates: []tls.Certificate{c}, MinVersion: tls.VersionTLS12}, nil
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "bdtools 3D grade", Organization: []string{"bdtools"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	kb, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	if err := os.MkdirAll(dir, 0o750); err == nil {
		_ = os.WriteFile(crt, certPEM, 0o600)
		_ = os.WriteFile(key, keyPEM, 0o600)
	}
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{c}, MinVersion: tls.VersionTLS12}, nil
}

// certCovers says whether a certificate names every one of the addresses.
func certCovers(c tls.Certificate, ips []net.IP) bool {
	if len(c.Certificate) == 0 {
		return false
	}
	x, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil || time.Now().After(x.NotAfter) {
		return false
	}
	for _, ip := range ips {
		if !slices.ContainsFunc(x.IPAddresses, func(have net.IP) bool { return have.Equal(ip) }) {
			return false
		}
	}
	return true
}

// localIPs is this machine's addresses: the loopback and every
// interface's IPv4.
func localIPs() []net.IP {
	ips := []net.IP{net.IPv4(127, 0, 0, 1)}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			ips = append(ips, n.IP.To4())
		}
	}
	return ips
}

// nextPort is an address with its port one up: the TLS side's.
func nextPort(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	var p int
	if _, err := fmt.Sscanf(port, "%d", &p); err != nil || p <= 0 || p >= 65535 {
		return "", errors.New("no port after " + addr)
	}
	return net.JoinHostPort(host, fmt.Sprint(p+1)), nil
}
