

# Chebur.NET 🧅⚡  [![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)  [![License](https://img.shields.io/badge/License-WTFPL-brightgreen.svg)](LICENSE)

**Chebur.NET** — модульный сервис прозрачного проксирования и выборочной маршрутизации сетевого трафика для роутеров под управлением OpenWrt.

Вся логика парсинга подписок, оркестрации ядер, генерации конфигураций, динамической балансировки и мониторинга вынесена в автономный скомпилированный демон на Go (`cheburnetd`), взаимодействующий с веб-интерфейсом LuCI через REST и WebSocket.

---

## Ключевые особенности

* **Два сменных ядра**:
  * **sing-box**: высокая производительность, низкие требования к памяти, поддержка современных протоколов и FakeIP (`198.18.0.0/15`).
  * **Xray-core**: эталонная совместимость с экосистемой V2Ray/Xray.
  * Горячее переключение ядер напрямую из LuCI без остановки службы.
* **Поддерживаемые протоколы**: VLESS (Reality / XTLS-Vision), Hysteria2, Trojan, Shadowsocks, SOCKS5.
* **Клиентские политики (Client Policy)**:
  * Разделение маршрутизации устройств локальной сети по трем режимам:
    * `rules` — по спискам (избирательный обход).
    * `full_proxy` — весь трафик девайса принудительно через VPN.
    * `direct` — работа строго напрямую без проксирования.
  * Автоматический резолв клиентов по связке IP/MAC через базу аренды DHCP (`/tmp/dhcp.leases`).
* **Универсальная работа с подписками**:
  * Поддержка форматов Base64, YAML (Clash) и JSON современных панелей (3X-UI, Remnawave, Marzban).
  * Ротация User-Agent (`Happ`, `ClashMeta`, `sing-box`, `Xray-core`).
  * **Safety Fallback**: защита от затирания рабочей конфигурации ядра при сбоях загрузки подписки.
* **Аппаратный HWID**:
  * Автоматическая генерация уникального идентификатора на базе MAC-адреса хоста для VLESS Reality.
* **Разделение DNS без утечек**:
  * DNS-inbound вынесен на порт `1053` во избежание конфликта с системным `dnsmasq`.
  * Поддержка защищённых протоколов DoH (DNS-over-HTTPS) и DoT (DNS-over-TLS) для предотвращения перехвата запросов.
* **Реактивный интерфейс LuCI**:
  * Потоковый вывод задержек и статусов узлов по WebSocket (`:8088`) и Clash REST API (`:9090`).
  * Встроенная интеграция с веб-панелью YACD.

---

## Архитектура

```text
               +----------------------------------------------------+
               |                LuCI Web Interface                  |
               |         (JavaScript / CSR dashboard.js)            |
               +-------------------------+--------------------------+
                                         | REST / WebSocket (:8088)
                                         v
+-------------------------------------------------------------------+
|                        cheburnetd (Go Daemon)                     |
|                                                                   |
|  [ UCI Reader ]  <--->  [ Sub Parser ]  <--->  [ Health Checker ] |
|         |                                              |          |
|         v                                              v          |
|  [ Config Builders ]                         [ Process Manager ]  |
|    - sing-box builder                              - sing-box     |
|    - xray builder                                  - xray-core    |
+--------------------+-----------------------------------+----------+
                     | (configs)                         |
                     v                                   v
            +-----------------+                 +------------------+
            |  Proxy Engines  | <=============> |  Linux Network   |
            | (sing-box/xray) |                 |  - nftables      |
            +-----------------+                 |  - ip rule       |
                                                |  - tproxy (1602) |
                                                +------------------+

```

## Структура репозитория


```

├── cmd/
│   └── cheburnetd/                     # Точка входа сервиса
├── internal/
│   ├── api/                            # REST и WebSocket сервер (:8088)
│   ├── config/                         # Модели данных (models.go), чтение UCI, парсинг
│   ├── engine/                         # Супервизор процессов sing-box / xray
│   │   ├── singbox/                    # Генератор sing-box config.json (builder.go)
│   │   └── xray/                       # Генератор xray config.json
│   └── network/                        # Управление правилами nftables и tproxy
├── pkg/
│   ├── hwid/                           # Генератор MAC-bound HWID
│   └── uri/                            # Декодер ссылок прокси-нод
├── openwrt/
│   └── files/
│       ├── etc/
│       │   ├── config/cheburnet        # Конфигурация UCI по умолчанию
│       │   └── init.d/cheburnet        # procd init-скрипт управления сервисом
│       ├── usr/share/luci/menu.d/
│       │   └── luci-app-cheburnet.json # Регистрация меню LuCI
│       └── www/luci-static/resources/view/cheburnet/
│           └── dashboard.js            # Фронтенд-панель управления LuCI
├── .github/workflows/
│   └── build-packages.yml              # CI/CD матрица сборки (.ipk и .apk с UPX)
└── README.md

```

## Установка

Готовые бинарные пакеты доступны во вкладке Releases:

Для OpenWrt ≤ 23.05 (opkg): формат .ipk.

Для OpenWrt 24.x+ (apk): формат .apk.

Установка через OPKG (OpenWrt ≤ 23.05):

``` bash
opkg update
opkg install ip-full nftables ca-bundle curl
opkg install sing-box   # или opkg install xray-core

opkg install luci-app-cheburnet_0.0.1-1_<arch>.ipk
```

Установка через APK (OpenWrt 24.x+):

``` bash
apk update
apk add --allow-untrusted luci-app-cheburnet-0.0.1-r1.<arch>.apk
```

## Ручная сборка в WSL / Linux

Компиляция Go-демона

``` bash
# Для ARM64 (Cortex-A53):
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
go build -ldflags="-s -w" -trimpath -o bin/cheburnetd ./cmd/cheburnetd

# Для MediaTek MT7621 (MIPS Little Endian, soft-float):
# CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat \
# go build -ldflags="-s -w" -trimpath -o bin/cheburnetd ./cmd/cheburnetd

# Сжатие UPX (опционально)
upx --best --lzma bin/cheburnetd
2. Развёртывание на устройство по SSH
Bash
ROUTER="root@192.168.1.1"

# Исполняемый файл
scp bin/cheburnetd $ROUTER:/usr/bin/cheburnetd
ssh $ROUTER "chmod +x /usr/bin/cheburnetd"

# Системная служба и конфигурация
scp openwrt/files/etc/init.d/cheburnet $ROUTER:/etc/init.d/cheburnet
scp openwrt/files/etc/config/cheburnet $ROUTER:/etc/config/cheburnet
ssh $ROUTER "chmod +x /etc/init.d/cheburnet"

# Веб-интерфейс LuCI
scp openwrt/files/usr/share/luci/menu.d/luci-app-cheburnet.json $ROUTER:/usr/share/luci/menu.d/
scp openwrt/files/www/luci-static/resources/view/cheburnet/dashboard.js $ROUTER:/www/luci-static/resources/view/cheburnet/

# Активация и сброс кэша
ssh $ROUTER "/etc/init.d/cheburnet enable && rm -rf /tmp/luci-* && /etc/init.d/uhttpd restart"
```

## Конфигурация UCI (```/etc/config/cheburnet```)

``` text
config cheburnet 'main'
    option enabled '1'
    option engine 'sing-box'
    option routing_mode 'rules'
    option auto_update '0'
    option config_type 'urltest'
    option source_mode 'subscription'
    option urltest_interval '3m'
    option urltest_tolerance '50'
    option urltest_url '[https://www.gstatic.com/generate_204](https://www.gstatic.com/generate_204)'
    option auto_hwid '1'
    option custom_hwid ''
    option tproxy_port '1602'
    option dns_port '1053'
    option mixed_port '4534'
    option dns_protocol 'doh'
    option dns_server '1.1.1.1'
    option bootstrap_dns '77.88.8.1'
    option dns_ttl '60'
    option enable_yacd '1'
    list source_interfaces 'br-lan'
    list rulesets 'russia_inside'
    list rulesets 'youtube'
    list rulesets 'telegram'
    option custom_domains 'ipify.org'
    option custom_subnets '91.108.4.0/22'

# Подписки
config subscription
    option enabled '1'
    option user_agent 'Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)'
    option hwid ''
    option url 'https://example.com/api/v1/client/subscribe?token=xxx'

# Политики клиентов
config client_rule
    option enabled '1'
    option name 'Smart TV'
    option target '192.168.1.120'
    option mode 'full_proxy'

config client_rule
    option enabled '1'
    option name 'Рабочий ПК'
    option target '192.168.1.55'
    option mode 'direct'
```

## Управление через консоль

``` bash
# Управление демоном и дочерними процессами
/etc/init.d/cheburnet start
/etc/init.d/cheburnet stop
/etc/init.d/cheburnet restart

# Мягкая перезагрузка правил через REST API (без остановки демона)
/etc/init.d/cheburnet reload

# Просмотр логов сервиса и ядер
logread -e cheburnetd
logread -e sing-box
```
📄 Лицензия
-----------

Проект распространяется под лицензией [WTFPL](https://www.wtfpl.net/).