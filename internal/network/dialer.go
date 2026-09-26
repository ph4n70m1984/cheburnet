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

type proxyDecisionKey struct{}

// NewBypassDialer создает сетевой dialer с принудительной системной меткой SO_MARK.
// Пакеты сокета помечаются меткой (по умолчанию EmergencyDirectMarkInt = 0x00300000),
// что позволяет им миновать перехват TProxy в цепочке mangle_output nftables.
func NewBypassDialer(timeout time.Duration, mark int) *net.Dialer {
	if mark <= 0 {
		mark = EmergencyDirectMarkInt
	}
	return &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Control: func(networkProto, address string, c syscall.RawConn) error {
			var operr error
			err := c.Control(func(fd uintptr) {
				operr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
			})
			if err != nil {
				return err
			}
			return operr
		},
	}
}

// NewBypassTransport создает http.Transport со встроенным BypassDialer
func NewBypassTransport(timeout time.Duration, mark int) *http.Transport {
	return &http.Transport{
		DialContext:           NewBypassDialer(timeout, mark).DialContext,
		ResponseHeaderTimeout: timeout,
		TLSHandshakeTimeout:   10 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		DisableKeepAlives:     true,
	}
}

// NewBypassClient создает изолированный http.Client с обходом TProxy-перехвата
func NewBypassClient(timeout time.Duration, mark int) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: NewBypassTransport(timeout, mark),
	}
}

func NewSmartTransport(timeout time.Duration, mixedPort int, isMixedProxyAlive func() bool) *http.Transport {
	if mixedPort <= 0 {
		mixedPort = 4534
	}

	proxyAddr := fmt.Sprintf("http://127.0.0.1:%d", mixedPort)
	proxyURL, _ := url.Parse(proxyAddr)

	cleanLocalDialer := &net.Dialer{
		Timeout: timeout,
	}

	directBypassDialer := NewBypassDialer(timeout, EmergencyDirectMarkInt)

	return &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			alive := isMixedProxyAlive != nil && isMixedProxyAlive()

			// Сохраняем атомарное решение в контексте запроса для устранения гонки с DialContext
			*req = *req.WithContext(context.WithValue(req.Context(), proxyDecisionKey{}, alive))

			targetHost := req.URL.Host
			if targetHost == "" {
				targetHost = req.Host
			}

			if alive {
				return proxyURL, nil
			}

			log.Printf("[network/transport] Target: %s%s -> ROUTE: Emergency Direct (proxy down, bypass mark 0x%08x)",
				targetHost, req.URL.Path, EmergencyDirectMarkInt)
			return nil, nil
		},
		DialContext: func(ctx context.Context, networkProto, addr string) (net.Conn, error) {
			start := time.Now()

			useProxy, hasDecision := ctx.Value(proxyDecisionKey{}).(bool)
			if !hasDecision {
				useProxy = isMixedProxyAlive != nil && isMixedProxyAlive()
			}

			// 1. Маршрут через локальный mixedPort sing-box
			if useProxy {
				conn, err := cleanLocalDialer.DialContext(ctx, networkProto, addr)
				rtt := time.Since(start).Milliseconds()
				if err != nil {
					log.Printf("[network/dialer] Proxy Dial ERROR to %s (via %s) after %dms: %v",
						addr, proxyAddr, rtt, err)
					return nil, err
				}
				return conn, nil
			}

			// 2. Аварийный прямой маршрут в обход TProxy с меткой EmergencyDirectMarkInt
			conn, err := directBypassDialer.DialContext(ctx, networkProto, addr)
			rtt := time.Since(start).Milliseconds()
			if err != nil {
				log.Printf("[network/dialer] Direct Dial ERROR to %s (mark: 0x%08x) after %dms: %v",
					addr, EmergencyDirectMarkInt, rtt, err)
				return nil, err
			}
			return conn, nil
		},
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
	}
}
