package network

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// NewBypassDialer создает сетевой dialer с принудительной системной меткой SO_MARK.
// Пакеты сокета помечаются меткой EmergencyDirectMarkInt (0x00300000),
// что позволяет им миновать перехват TProxy в цепочках nftables.
func NewBypassDialer(timeout time.Duration, mark int) *net.Dialer {
	if mark <= 0 {
		mark = EmergencyDirectMarkInt
	}

	dialerControl := func(networkProto, address string, c syscall.RawConn) error {
		var operr error
		err := c.Control(func(fd uintptr) {
			operr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
		})
		if err != nil {
			return err
		}
		return operr
	}

	// Изолированный DNS-резолвер с SO_MARK для прямых запросов.
	// Исключает зависание на локальном dnsmasq/sing-box при падении прокси.
	directResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, networkProto, address string) (net.Conn, error) {
			d := &net.Dialer{
				Timeout: timeout,
				Control: dialerControl,
			}
			// Резолвим через публичный DNS Cloudflare напрямую в обход туннеля
			return d.DialContext(ctx, "udp", "1.1.1.1:53")
		},
	}

	return &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Resolver:  directResolver,
		Control:   dialerControl,
	}
}

// NewBypassTransport создает http.Transport со встроенным BypassDialer
func NewBypassTransport(timeout time.Duration, mark int) *http.Transport {
	return &http.Transport{
		DialContext:           NewBypassDialer(timeout, mark).DialContext,
		ResponseHeaderTimeout: timeout,
		TLSHandshakeTimeout:   5 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		DisableKeepAlives:     true,
	}
}

// NewBypassClient создает изолированный http.Client с гарантированным обходом TProxy
func NewBypassClient(timeout time.Duration, mark int) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: NewBypassTransport(timeout, mark),
	}
}

// NewSmartTransport создает гибридный транспорт:
// При живом прокси запросы идут в локальный mixedPort sing-box.
// При сбое прокси трафик мгновенно направляется напрямую с меткой обхода TProxy.
func NewSmartTransport(timeout time.Duration, mixedPort int, isMixedProxyAlive func() bool) *http.Transport {
	if mixedPort <= 0 {
		mixedPort = 2080
	}

	proxyAddr := fmt.Sprintf("http://127.0.0.1:%d", mixedPort)
	proxyURL, _ := url.Parse(proxyAddr)

	cleanLocalDialer := &net.Dialer{
		Timeout: timeout,
	}

	directBypassDialer := NewBypassDialer(timeout, EmergencyDirectMarkInt)

	return &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			if isMixedProxyAlive != nil && isMixedProxyAlive() {
				return proxyURL, nil
			}

			targetHost := req.URL.Host
			if targetHost == "" {
				targetHost = req.Host
			}

			log.Printf("[network/transport] Target: %s%s -> ROUTE: Emergency Direct (proxy down, mark 0x%08x)",
				targetHost, req.URL.Path, EmergencyDirectMarkInt)
			return nil, nil
		},
		DialContext: func(ctx context.Context, networkProto, addr string) (net.Conn, error) {
			start := time.Now()

			// Если запрос идет к локальному mixed-порту sing-box
			if addr == fmt.Sprintf("127.0.0.1:%d", mixedPort) {
				conn, err := cleanLocalDialer.DialContext(ctx, networkProto, addr)
				if err != nil {
					log.Printf("[network/dialer] Proxy Dial ERROR to %s after %dms: %v",
						addr, time.Since(start).Milliseconds(), err)
					return nil, err
				}
				return conn, nil
			}

			// Прямой аварийный маршрут в обход TProxy с меткой EmergencyDirectMarkInt
			conn, err := directBypassDialer.DialContext(ctx, networkProto, addr)
			if err != nil {
				log.Printf("[network/dialer] Direct Dial ERROR to %s (mark: 0x%08x) after %dms: %v",
					addr, EmergencyDirectMarkInt, time.Since(start).Milliseconds(), err)
				return nil, err
			}
			return conn, nil
		},
		ResponseHeaderTimeout: timeout,
		TLSHandshakeTimeout:   5 * time.Second,
		IdleConnTimeout:       15 * time.Second,
		DisableKeepAlives:     true,
	}
}
