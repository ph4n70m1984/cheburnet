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

// SingBoxSelfMark — системная метка обхода TProxy (0x00200000 = 2097152)
const SingBoxSelfMark = 0x00200000

func NewSmartTransport(timeout time.Duration, mixedPort int, isEngineAlive func() bool) *http.Transport {
	if mixedPort <= 0 {
		mixedPort = 4534
	}

	proxyAddr := fmt.Sprintf("http://127.0.0.1:%d", mixedPort)
	proxyURL, _ := url.Parse(proxyAddr)

	// Чистый диалер без системных меток для подключения к локальному сокету 127.0.0.1:mixedPort
	cleanLocalDialer := &net.Dialer{
		Timeout: timeout,
	}

	// Аварийный прямой диалер с установкой SO_MARK = 0x00200000 для обхода правил TProxy nftables[cite: 6]
	directBypassDialer := &net.Dialer{
		Timeout: timeout,
		Control: func(network, address string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, SingBoxSelfMark)
			})
		},
	}

	return &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			alive := isEngineAlive != nil && isEngineAlive()
			targetHost := req.URL.Host
			if targetHost == "" {
				targetHost = req.Host
			}

			if alive {
				log.Printf("[network/transport] Target: %s%s -> ROUTE: VPN Tunnel (via sing-box %s)",
					targetHost, req.URL.Path, proxyAddr)
				return proxyURL, nil
			}

			log.Printf("[network/transport] Target: %s%s -> ROUTE: Emergency Direct (core inactive, fail-open bypass)",
				targetHost, req.URL.Path)
			return nil, nil
		},
		DialContext: func(ctx context.Context, networkProto, addr string) (net.Conn, error) {
			start := time.Now()
			alive := isEngineAlive != nil && isEngineAlive()

			if alive {
				conn, err := cleanLocalDialer.DialContext(ctx, networkProto, addr)
				rtt := time.Since(start).Milliseconds()
				if err != nil {
					log.Printf("[network/dialer] Proxy Dial ERROR to %s (via %s) after %dms: %v",
						addr, proxyAddr, rtt, err)
					return nil, err
				}
				log.Printf("[network/dialer] Proxy Dial OK to %s (rtt: %dms, socket: clean)", addr, rtt)
				return conn, nil
			}

			conn, err := directBypassDialer.DialContext(ctx, networkProto, addr)
			rtt := time.Since(start).Milliseconds()
			if err != nil {
				log.Printf("[network/dialer] Direct Dial ERROR to %s (mark: 0x%08x) after %dms: %v",
					addr, SingBoxSelfMark, rtt, err)
				return nil, err
			}
			log.Printf("[network/dialer] Direct Dial OK to %s (rtt: %dms, bypass mark: 0x%08x)",
				addr, rtt, SingBoxSelfMark)
			return conn, nil
		},
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
	}
}
