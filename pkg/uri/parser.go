package uri

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"cheburnet/internal/config"
	"cheburnet/pkg/hwid"
)

func decodeBase64Safe(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}

	if res, err := base64.StdEncoding.DecodeString(s); err == nil {
		return res, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

func extractSNI(q url.Values) string {
	for _, key := range []string{"sni", "peer", "serverName"} {
		if val := strings.TrimSpace(q.Get(key)); val != "" {
			return val
		}
	}
	return ""
}

func ParseNodeURI(rawURI string, autoHWID bool, customHWID string) (*config.GenericNode, error) {
	rawURI = strings.TrimSpace(rawURI)
	if rawURI == "" {
		return nil, fmt.Errorf("empty uri")
	}

	if strings.HasPrefix(strings.ToLower(rawURI), "ss://") && !strings.Contains(rawURI[5:], "@") {
		decodedURI, err := parseLegacyShadowsocksURI(rawURI)
		if err == nil {
			rawURI = decodedURI
		}
	}

	u, err := url.Parse(rawURI)
	if err != nil {
		return nil, fmt.Errorf("invalid uri: %w", err)
	}

	q := u.Query()
	tag := u.Fragment
	if tag != "" {
		if unescaped, err := url.QueryUnescape(tag); err == nil {
			tag = unescaped
		}
	} else {
		tag = fmt.Sprintf("%s:%s", u.Hostname(), u.Port())
	}

	nodeHWID := q.Get("hwid")
	if nodeHWID == "" {
		if customHWID != "" {
			nodeHWID = customHWID
		} else if autoHWID {
			nodeHWID = hwid.GetDeviceHWID("br-lan")
		}
	}

	port, _ := strconv.Atoi(u.Port())
	scheme := strings.ToLower(u.Scheme)

	node := &config.GenericNode{
		Tag:     tag,
		Address: u.Hostname(),
		Port:    port,
		HWID:    nodeHWID,
	}

	switch scheme {
	case "vless":
		node.Protocol = "vless"
		if u.User != nil {
			node.UUID = u.User.Username()
		}
		node.Flow = q.Get("flow")
		node.Network = q.Get("type")
		if node.Network == "" {
			node.Network = "tcp"
		}
		node.Security = strings.ToLower(strings.TrimSpace(q.Get("security")))

		sni := extractSNI(q)
		if sni == "" && node.Security != "reality" {
			sni = node.Address
		}
		node.SNI = sni

		node.Fingerprint = q.Get("fp")
		if node.Fingerprint == "" {
			node.Fingerprint = "chrome"
		}
		node.PublicKey = q.Get("pbk")
		node.ShortID = q.Get("sid")
		node.Path = q.Get("path")
		node.Host = q.Get("host")

		// Парсинг XHTTP транспорта
		netType := strings.ToLower(strings.TrimSpace(node.Network))
		if netType == "xhttp" || netType == "splithttp" {
			node.Network = "xhttp"
			if node.Path == "" {
				node.Path = "/"
			}
			mode := strings.ToLower(strings.TrimSpace(q.Get("mode")))
			if mode == "" {
				mode = "auto"
			}
			node.XHTTPMode = mode

			padding := q.Get("x_padding_bytes")
			if padding == "" {
				padding = q.Get("xPaddingBytes")
			}
			if padding == "" {
				padding = "100-1000"
			}
			node.XHTTPPadding = padding

			noGRPC := q.Get("no_grpc_header")
			node.XHTTPNoGRPC = (noGRPC == "1" || strings.EqualFold(noGRPC, "true"))

			// Несовместимость протокола: XTLS-Vision запрещен с XHTTP
			node.Flow = ""
		}

	case "ss":
		node.Protocol = "shadowsocks"
		if u.User != nil {
			userInfo := u.User.Username()
			password, hasPassword := u.User.Password()

			if !hasPassword {
				if decoded, err := decodeBase64Safe(userInfo); err == nil {
					parts := strings.SplitN(string(decoded), ":", 2)
					if len(parts) == 2 {
						node.Method = parts[0]
						node.Password = parts[1]
					}
				}
			} else {
				node.Method = userInfo
				node.Password = password
			}
		}

	case "trojan":
		node.Protocol = "trojan"
		if u.User != nil {
			node.Password = u.User.Username()
		}
		node.Network = q.Get("type")
		if node.Network == "" {
			node.Network = "tcp"
		}
		node.Security = q.Get("security")
		if node.Security == "" {
			node.Security = "tls"
		}
		node.SNI = extractSNI(q)
		if node.SNI == "" {
			node.SNI = node.Address
		}
		node.Insecure = q.Get("allowInsecure") == "1" || q.Get("insecure") == "1"
		node.Path = q.Get("path")
		node.Host = q.Get("host")

		netType := strings.ToLower(strings.TrimSpace(node.Network))
		if netType == "xhttp" || netType == "splithttp" {
			node.Network = "xhttp"
			if node.Path == "" {
				node.Path = "/"
			}
			mode := strings.ToLower(strings.TrimSpace(q.Get("mode")))
			if mode == "" {
				mode = "auto"
			}
			node.XHTTPMode = mode

			padding := q.Get("x_padding_bytes")
			if padding == "" {
				padding = q.Get("xPaddingBytes")
			}
			if padding == "" {
				padding = "100-1000"
			}
			node.XHTTPPadding = padding

			noGRPC := q.Get("no_grpc_header")
			node.XHTTPNoGRPC = (noGRPC == "1" || strings.EqualFold(noGRPC, "true"))
		}

	case "socks", "socks5", "socks5h", "socks4", "socks4a":
		node.Protocol = "socks"
		if scheme == "socks4" || scheme == "socks4a" {
			node.SocksVersion = "4"
		} else {
			node.SocksVersion = "5"
		}

		if u.User != nil {
			node.Username = u.User.Username()
			node.Password, _ = u.User.Password()
		}

	case "hysteria2", "hy2":
		node.Protocol = "hysteria2"
		if u.User != nil {
			node.Password = u.User.Username()
		}
		node.SNI = extractSNI(q)
		if node.SNI == "" {
			node.SNI = node.Address
		}
		node.Insecure = q.Get("insecure") == "1" || q.Get("allowInsecure") == "1"
		node.ObfsType = q.Get("obfs")
		node.ObfsPassword = q.Get("obfs-password")
		node.PortRange = q.Get("mport")

	default:
		return nil, fmt.Errorf("unsupported protocol: %s", scheme)
	}

	if node.Address == "" || node.Port == 0 {
		return nil, fmt.Errorf("missing host or port in uri")
	}

	return node, nil
}

func parseLegacyShadowsocksURI(rawURI string) (string, error) {
	tag := ""
	payload := rawURI[5:]

	if idx := strings.Index(payload, "#"); idx != -1 {
		tag = payload[idx:]
		payload = payload[:idx]
	}

	decoded, err := decodeBase64Safe(payload)
	if err != nil {
		return "", err
	}

	decodedStr := string(decoded)
	atIdx := strings.LastIndex(decodedStr, "@")
	if atIdx == -1 {
		return "", fmt.Errorf("invalid legacy ss uri")
	}

	userPart := decodedStr[:atIdx]
	hostPart := decodedStr[atIdx+1:]

	encodedUser := base64.RawURLEncoding.EncodeToString([]byte(userPart))
	return fmt.Sprintf("ss://%s@%s%s", encodedUser, hostPart, tag), nil
}
