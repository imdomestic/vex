package transport

import (
	"crypto/tls"
	"fmt"
	"time"
)

type CertificateFiles struct {
	Certificate string
	Key         string
}

func (f CertificateFiles) Load() (*tls.Certificate, error) {
	certificate, err := tls.LoadX509KeyPair(f.Certificate, f.Key)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	if certificate.Leaf == nil {
		return nil, fmt.Errorf("TLS certificate has no parsed leaf")
	}
	now := time.Now()
	if now.Before(certificate.Leaf.NotBefore) || !now.Before(certificate.Leaf.NotAfter) {
		return nil, fmt.Errorf("TLS certificate is outside its validity period")
	}
	return &certificate, nil
}

func (f CertificateFiles) Config() (*tls.Config, error) {
	if _, err := f.Load(); err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return f.Load()
		},
	}, nil
}
