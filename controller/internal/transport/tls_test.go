package transport_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/imdomestic/vex/controller/internal/api"
	"github.com/imdomestic/vex/controller/internal/transport"
)

func certificate(t *testing.T, directory string, serial int64) (transport.CertificateFiles, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:        true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	files := transport.CertificateFiles{Certificate: filepath.Join(directory, "cert.pem"), Key: filepath.Join(directory, "key.pem")}
	if err = os.WriteFile(files.Certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(files.Key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}), 0600); err != nil {
		t.Fatal(err)
	}
	return files, parsed
}

func TestActualAPIUsesRotatedCertificateWithoutRestart(t *testing.T) {
	files, first := certificate(t, t.TempDir(), 1)
	secondFiles, second := certificate(t, t.TempDir(), 2)
	configuration, err := files.Config()
	if err != nil {
		t.Fatal(err)
	}
	service := &api.Server{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: service.Handler(), TLSConfig: configuration, ReadHeaderTimeout: time.Second}
	served := make(chan error, 1)
	go func() { served <- server.ServeTLS(listener, "", "") }()
	t.Cleanup(func() {
		server.Close()
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})
	serverURL := "https://" + listener.Addr().String()
	roots := x509.NewCertPool()
	roots.AddCert(first)
	roots.AddCert(second)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}
	defer client.CloseIdleConnections()
	check := func(serial int64) {
		t.Helper()
		response, err := client.Get(serverURL + "/v2/health")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body struct {
			Status string `json:"status"`
		}
		if err = json.NewDecoder(response.Body).Decode(&body); err != nil || body.Status != "ok" || response.StatusCode != http.StatusOK {
			t.Fatal("actual API health response:", err)
		}
		if response.TLS.PeerCertificates[0].SerialNumber.Int64() != serial {
			t.Fatal("TLS handshake used an unexpected certificate")
		}
	}
	check(1)
	if err = os.Rename(secondFiles.Certificate, files.Certificate); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(secondFiles.Key, files.Key); err != nil {
		t.Fatal(err)
	}
	check(2)
	if err = os.WriteFile(files.Key, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if response, err := client.Get(serverURL + "/v2/health"); err == nil {
		response.Body.Close()
		t.Fatal("invalid replacement key did not reject the new handshake")
	}
	plain := httptest.NewServer(service.InternalHandler(nil))
	defer plain.Close()
	response, err := plain.Client().Get(plain.URL + "/v2/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("internal loopback HTTP health failed")
	}
}
