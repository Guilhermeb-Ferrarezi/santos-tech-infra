package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Cliente HTTP para buscar imagens de URLs fornecidas pelo usuário. Este
// servidor roda DENTRO da rede da infra, então buscar URL arbitrária é vetor
// de SSRF — o DialContext só conecta em IPs públicos (cobre redirects também,
// e disca o IP já validado para evitar rebinding DNS).
func newFetchClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			ip = ip.Unmap()
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
				ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		return nil, fmt.Errorf("host %q não resolve para um IP público", host)
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}
}
