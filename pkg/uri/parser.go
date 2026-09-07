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

// decodeBase64Safe декодирует строку Base64 со стандартным или URL-safe алфавитом и любым паддингом
func decodeBase64Safe(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	// Дополняем недостающий padding '='
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}

	if res, err := base64.StdEncoding.DecodeString(s); err == nil {
		return res, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// ParseNodeURI парсит vless://, ss://, trojan://, socks4/5://, hy2/hysteria2://
func ParseNodeURI(rawURI string, autoHWID bool, customHWID string) (*config.GenericNode, error) {
	rawURI = strings.TrimSpace(rawURI)
	if rawURI == "" {
		return nil, fmt.Errorf("empty uri")
	}

	// Обработка старого формата Shadowsocks: ss://BASE64#Tag (где base64 содержит method:pass@host:port)
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

	// Логика опционального HWID
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
		node.Security = q.Get("security")
		node.SNI = q.Get("sni")
		if node.SNI == "" {
			node.SNI = node.Address
		}
		node.Fingerprint = q.Get("fp")
		node.PublicKey = q.Get("pbk")
		node.ShortID = q.Get("sid")
		node.Path = q.Get("path")
		node.Host = q.Get("host")

	case "ss":
		node.Protocol = "shadowsocks"
		if u.User != nil {
			userInfo := u.User.Username()
			password, hasPassword := u.User.Password()

			if !hasPassword {
				// SIP002 URL-safe base64: ss://base64(method:password)@host:port
				if decoded, err := decodeBase64Safe(userInfo); err == nil {
					parts := strings.SplitN(string(decoded), ":", 2)
					if len(parts) == 2 {
						node.Method = parts[0]
						node.Password = parts[1]
					}
				}
			} else {
				// ss://method:password@host:port
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
		node.SNI = q.Get("sni")
		if node.SNI == "" {
			node.SNI = node.Address
		}
		node.Insecure = q.Get("allowInsecure") == "1" || q.Get("insecure") == "1"
		node.Path = q.Get("path")
		node.Host = q.Get("host")

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
		node.SNI = q.Get("sni")
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

	// Валидация базовых сетевых параметров
	if node.Address == "" || node.Port == 0 {
		return nil, fmt.Errorf("missing host or port in uri")
	}

	return node, nil
}

// parseLegacyShadowsocksURI преобразует формат ss://BASE64(method:password@host:port)#Tag в валидный SIP002 URL
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

	// Ожидается строка вида: method:password@host:port
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
