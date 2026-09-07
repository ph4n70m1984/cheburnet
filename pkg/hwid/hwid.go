package hwid

import (
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"strings"
)

// GetDeviceHWID генерирует стабильный UUID на базе MAC интерфейса br-lan или /etc/machine-id
func GetDeviceHWID(ifaceName string) string {
	if iface, err := net.InterfaceByName(ifaceName); err == nil && len(iface.HardwareAddr) > 0 {
		h := sha256.Sum256([]byte(iface.HardwareAddr.String()))
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
	}

	if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		raw := strings.TrimSpace(string(data))
		if len(raw) >= 32 {
			return fmt.Sprintf("%s-%s-%s-%s-%s",
				raw[0:8], raw[8:12], raw[12:16], raw[16:20], raw[20:32])
		}
	}

	return ""
}
