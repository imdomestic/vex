package config

import "path/filepath"

func (s Server) validateListeners() error {
	if len(s.Listen) != 0 && len(s.Listeners) != 0 {
		return fieldError("server", "listen and listeners cannot be combined")
	}
	endpoints := s.Endpoints()
	if len(endpoints) == 0 {
		return fieldError("server.listeners", "requires at least one address")
	}
	if (s.TLSCertFile == "") != (s.TLSKeyFile == "") {
		return fieldError("server", "tls_cert_file and tls_key_file must be configured together")
	}
	if s.TLSCertFile != "" && (!filepath.IsAbs(s.TLSCertFile) || !filepath.IsAbs(s.TLSKeyFile)) {
		return fieldError("server", "TLS certificate and key require absolute paths")
	}
	addresses := map[string]bool{}
	hasTLS := false
	for _, endpoint := range endpoints {
		if !validAddress(endpoint.Address) || addresses[endpoint.Address] {
			return fieldError("server.listeners", "requires unique host:port addresses")
		}
		addresses[endpoint.Address] = true
		if endpoint.Scope != "public" && endpoint.Scope != "internal" {
			return fieldError("server.listeners.scope", "requires public or internal")
		}
		if endpoint.TLS && s.TLSCertFile == "" {
			return fieldError("server.listeners.tls", "requires tls_cert_file and tls_key_file")
		}
		hasTLS = hasTLS || endpoint.TLS
	}
	if s.TLSCertFile != "" && !hasTLS {
		return fieldError("server.listeners", "TLS files require a TLS listener")
	}
	return nil
}
