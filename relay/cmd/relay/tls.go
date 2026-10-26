package main

import (
	"crypto/tls"
	"fmt"
	"os"
	"sync"
	"time"
)

// certReloader serves the certificate from certFile/keyFile and reloads it when the files
// change, so a renewed Secret (cert-manager, or a re-run of helm upgrade) applies without a restart.
type certReloader struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
	checked time.Time
}

func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if _, err := r.get(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) get() (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cert != nil && time.Since(r.checked) < 30*time.Second {
		return r.cert, nil
	}
	r.checked = time.Now()
	info, err := os.Stat(r.certFile)
	if err != nil {
		if r.cert != nil {
			return r.cert, nil // keep serving the last good certificate
		}
		return nil, fmt.Errorf("read TLS certificate: %w", err)
	}
	if r.cert != nil && !info.ModTime().After(r.modTime) {
		return r.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		if r.cert != nil {
			return r.cert, nil
		}
		return nil, fmt.Errorf("load TLS key pair: %w", err)
	}
	r.cert, r.modTime = &cert, info.ModTime()
	return r.cert, nil
}

func (r *certReloader) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return r.get()
		},
	}
}
