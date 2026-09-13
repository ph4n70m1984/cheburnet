# Chebur.NET 🧅⚡

**Chebur.NET** — модульный сервис прозрачного проксирования и интеллектуальной маршрутизации трафика для роутеров под управлением **OpenWrt**.

Проект представляет собой лёгкий скомпилированный демон на **Go**, который берёт на себя управление подписками, парсинг прокси-нод, генерацию конфигурации, маршрутизацию, health-checks, диагностику и управление `sing-box`.

Веб-интерфейс LuCI взаимодействует с `cheburnetd` через **REST API и WebSocket**.

Главная идея проекта — не превращать обычный OpenWrt-роутер в сервер с десятками тяжёлых компонентов, а вынести сложную логику в один компактный управляющий демон и использовать **sing-box** как единственный proxy engine.

---

## ✨ Ключевые возможности

### 🚀 sing-box как единственный engine

Chebur.NET использует **sing-box** как основной и единственный proxy-core.

Поддержка Xray-core удалена из основной архитектуры: это уменьшает количество кода, конфигурационных веток и потенциальных расхождений между движками.

Архитектура теперь выглядит проще:

```text
                  Chebur.NET
                      │
                      ▼
                 sing-box
                      │
          ┌───────────┼───────────┐
          │           │           │
       Routing      Proxy       DNS
          │           │           │
       nftables    tunnels     FakeIP
```

Это позволяет сосредоточить развитие проекта на возможностях sing-box и не поддерживать два различных формата конфигурации.

---

## 🔌 Поддерживаемые протоколы

Через sing-box поддерживаются современные proxy-протоколы, используемые в подписках:

- **VLESS**
- **VLESS + Reality**
- **XTLS-Vision**
- **Hysteria2**
- **Trojan**
- **Shadowsocks**
- **SOCKS5**

Конкретный набор возможностей зависит от версии установленного `sing-box`.

---

## 📦 Подписки и автоматический парсинг

Chebur.NET умеет получать и разбирать подписки непосредственно на роутере.

Поддерживаются:

- Base64 subscription;
- Clash YAML;
- JSON;
- URI-форматы;
- VLESS;
- Hysteria2;
- Trojan;
- Shadowsocks;
- SOCKS5;
- подписки современных VPN-панелей.

Поддерживается несколько источников подписок одновременно.

Для каждого источника могут использоваться собственные параметры, включая `User-Agent` и HWID.

### User-Agent

Для совместимости с различными панелями подписок можно использовать разные User-Agent:

```text
Happ
ClashMeta
sing-box
Xray
```

---

## 🛡️ Safety Fallback

Обновление подписки не должно приводить к потере рабочего подключения.

При обновлении:

```text
Subscription
     │
     ▼
   Parser
     │
     ▼
 New nodes
     │
     ▼
Generate config
     │
     ▼
sing-box check
     │
 ┌───┴────┐
 │        │
FAIL      OK
 │        │
 ▼        ▼
Rollback  SafeReload
```

Если загрузка подписки, парсинг или проверка нового конфигурационного файла завершается ошибкой, существующая рабочая конфигурация сохраняется.

---

## 🔍 Проверка sing-box

Перед запуском или применением новой конфигурации Chebur.NET проверяет установленный бинарник `sing-box`.

Проверяется:

- наличие бинарника;
- права на выполнение;
- возможность запуска;
- версия;
- корректность вывода `sing-box version`;
- совместимость версии с текущим генератором конфигурации.

Также определяется, используется ли расширенная сборка sing-box.

Информация о binary engine представляется структурированно:

```text
Path
VersionRaw
Major
Minor
Patch
IsExtended
```

Это позволяет не полагаться на предположение, что пользователь установил правильную версию ядра.

---

## ✅ Проверка конфигурации перед reload

Перед применением нового конфига Chebur.NET использует встроенную проверку:

```bash
sing-box check -c /tmp/run/cheburnet/sing-box.json
```

Только после успешной проверки конфигурация применяется к работающему engine.

Это особенно важно для автоматического обновления подписок:

```text
Новая подписка
      │
      ▼
Генерация config
      │
      ▼
sing-box check
      │
 ┌────┴─────┐
 │          │
 FAIL       OK
 │          │
 ▼          ▼
Rollback   SafeReload
```

---

# 🧠 Маршрутизация

Chebur.NET формирует маршрутизацию на основании:

- domain rules;
- IP/CIDR rules;
- готовых rulesets;
- пользовательских доменов;
- пользовательских подсетей;
- политики клиента;
- портов;
- типа протокола.

Поддерживается выборочная маршрутизация:

```text
LAN
 │
 ├── Direct
 │
 └── Proxy
        │
        ▼
    sing-box
```

---

## 👤 Client Policy

Для отдельных устройств локальной сети можно назначать собственные политики.

Примеры:

### `rules`

Обычная выборочная маршрутизация:

```text
локальные / разрешённые ресурсы → DIRECT
остальные → PROXY
```

### `full_proxy`

Весь трафик устройства отправляется через прокси.

Подходит, например, для:

- Smart TV;
- игровых приставок;
- отдельных телефонов;
- устройств без собственного VPN-клиента.

### `direct`

Устройство работает напрямую и не использует прокси-маршрутизацию.

Например:

- рабочий компьютер;
- отдельный IoT;
- локальный сервер.

Клиенты могут определяться по IP/MAC через DHCP lease database OpenWrt.

---

## 🔢 Port Routing

Chebur.NET позволяет задавать маршрутизацию для отдельных портов и диапазонов.

Например:

```text
UDP 500-1000 → PROXY
UDP 5222     → PROXY
```

или наоборот:

```text
P2P / отдельные сервисы → DIRECT
```

Поддерживается TCP/UDP в зависимости от используемой политики и конфигурации engine.

---

# 🌐 DNS и FakeIP

Chebur.NET может использовать DNS-инфраструктуру sing-box для корректной маршрутизации доменных запросов.

Используется отдельный DNS inbound, например:

```text
127.0.0.42:1053
```

что позволяет избежать конфликта с системным `dnsmasq`.

Для FakeIP используется диапазон:

```text
198.18.0.0/15
```

Это позволяет сохранить доменную информацию для последующей маршрутизации трафика.

---

# ⚖️ Балансировка и Health

Chebur.NET может формировать группы proxy-нод и передавать их sing-box для выбора рабочего outbound.

В конфигурации могут использоваться стратегии выбора ноды на основании задержки и состояния соединения.

Пример:

```text
             Proxy Pool
                 │
       ┌─────────┼─────────┐
       │         │         │
      VLESS     HYS      VLESS
       │         │         │
       └─────────┼─────────┘
                 │
              Balancer
                 │
                 ▼
              Traffic
```

Health-механизм отделён от UI: внутренние проверки могут выполняться постоянно, а пользовательский интерфейс получает только значимые события и проблемы.

---

# 🩺 Diagnostics

Chebur.NET содержит отдельный диагностический слой.

Вместо постоянного отображения десятков технических проверок интерфейс может показывать только реальные проблемы.

Концепция:

```text
Diagnostics
     │
 ┌───┼───────────────┐
 │   │               │
OK  WARNING        ERROR
 │
 └─────── не показывается пользователю
```

Для проблем используются:

- `problem_id`;
- severity;
- состояние;
- hysteresis;
- timestamps;
- восстановление проблемы;
- связь с Health/Supervisor.

WebSocket позволяет передавать события в интерфейс в реальном времени.

Пример события:

```json
{
  "type": "diagnostic",
  "problem_id": "engine.unhealthy",
  "severity": "error",
  "state": "active"
}
```

После восстановления:

```json
{
  "type": "diagnostic",
  "problem_id": "engine.unhealthy",
  "severity": "error",
  "state": "resolved"
}
```

Таким образом, LuCI не превращается в постоянный мониторинг внутренних технических деталей.

---

# 🔄 SafeReload

Обновление конфигурации выполняется через безопасный pipeline.

```text
Config
  │
  ▼
Generate
  │
  ▼
Validate
  │
  ▼
sing-box check
  │
  ▼
Apply
  │
  ▼
Health
  │
  ├── OK
  │
  └── FAIL → rollback
```

Для текущего sing-box используется единый путь конфигурации:

```text
/tmp/run/cheburnet/sing-box.json
```

Отдельные ветки конфигурации для Xray больше не существуют.

---

# ⚡ Быстрое управление engine

Chebur.NET не использует несколько proxy-core одновременно.

`sing-box` является выделенным engine проекта.

Это позволяет:

- убрать runtime-переключение между разными core;
- исключить дублирование конфигураторов;
- уменьшить размер кодовой базы;
- уменьшить количество потенциальных ошибок;
- сосредоточить тестирование на одном engine;
- использовать возможности sing-box непосредственно.

---

# 🖥️ LuCI

Веб-интерфейс работает через:

```text
LuCI
  │
  ├── REST
  │
  └── WebSocket
        │
        ▼
    cheburnetd
```

Интерфейс может отображать:

- состояние engine;
- количество нод;
- активную ноду;
- состояние подписок;
- latency;
- проблемы диагностики;
- события health;
- состояние маршрутизации.

WebSocket используется для реактивного обновления состояния без постоянного polling.

---

# 🆔 HWID

Chebur.NET поддерживает автоматическое формирование HWID.

При включённом:

```text
auto_hwid = 1
```

идентификатор может генерироваться на основе аппаратного идентификатора сетевого устройства.

Это позволяет использовать HWID-зависимые VLESS-конфигурации без ручного ввода идентификатора.

---

# 🏗️ Архитектура

```text
                  ┌───────────────────────────────┐
                  │             LuCI              │
                  │       Dashboard / JS          │
                  └───────────────┬───────────────┘
                                  │
                            REST / WS
                                  │
                                  ▼
┌─────────────────────────────────────────────────────────┐
│                     cheburnetd                          │
│                         Go                              │
│                                                         │
│  ┌──────────────┐    ┌──────────────────────────────┐   │
│  │ UCI / Config │───▶│ Subscription / Node Parser   │   │
│  └──────────────┘    └──────────────┬───────────────┘   │
│                                     │                   │
│                                     ▼                   │
│                           ┌─────────────────┐           │
│                           │ Config Builder  │           │
│                           └────────┬────────┘           │
│                                    │                    │
│                     ┌──────────────┴──────────────┐     │
│                     │                             │     │
│                 Health                     Diagnostics  │
│                     │                             │     │
│                     └──────────────┬──────────────┘     │
│                                    │                    │
│                              Supervisor                │
│                                    │                    │
└────────────────────────────────────┼────────────────────┘
                                     │
                                     ▼
                              ┌─────────────┐
                              │  sing-box   │
                              └──────┬──────┘
                                     │
                              TProxy / Proxy
                                     │
                                     ▼
                              ┌─────────────┐
                              │ Linux/OpenWrt│
                              │ nftables     │
                              │ ip rule      │
                              │ routing      │
                              └─────────────┘
```

---

# 📁 Структура проекта

```text
.
├── cmd/
│   └── cheburnetd/
│       └── main.go
│
├── internal/
│   ├── api/
│   │   ├── handlers.go
│   │   └── server.go
│   │
│   ├── config/
│   │   └── models.go
│   │
│   ├── diagnostics/
│   │   └── ...
│   │
│   ├── engine/
│   │   ├── detector.go
│   │   ├── engine.go
│   │   ├── health.go
│   │   ├── reload.go
│   │   ├── singbox.go
│   │   └── singbox/
│   │       ├── builder_v14.go
│   │       └── version.go
│   │
│   ├── network/
│   │   ├── ruleset_cron.go
│   │   └── ruleset_loader.go
│   │
│   ├── subscription/
│   │   └── worker.go
│   │
│   └── updater/
│       └── manager.go
│
├── pkg/
│   ├── happ/
│   │   └── crypt4.go
│   │
│   ├── hwid/
│   │   └── ...
│   │
│   └── uri/
│       └── parser.go
│
├── openwrt/
│   └── files/
│       ├── etc/
│       │   ├── config/
│       │   │   └── cheburnet
│       │   └── init.d/
│       │       └── cheburnet
│       │
│       ├── usr/share/luci/
│       │   └── menu.d/
│       │
│       └── www/luci-static/
│           └── resources/view/cheburnet/
│               └── dashboard.js
│
├── .github/
│   └── workflows/
│       └── build-packages.yml
│
└── README.md
```

---

# ⚙️ Конфигурация UCI

Пример базовой конфигурации:

```text
config cheburnet 'main'
    option enabled '1'
    option engine 'sing-box'
    option config_type 'urltest'
    option source_interface 'br-lan'

    option tproxy_port '1602'
    option mixed_port '4534'

    option auto_hwid '1'

    option sub_user_agent 'Happ/1.0.0'

    list rulesets 'russia_inside'
    list rulesets 'youtube'

    list subscription 'https://sub.example.com/api/v1/client/subscribe?token=xxx'
```

### Политика клиента

```text
config client_rule
    option enabled '1'
    option name 'Apple TV'
    option target '192.168.1.120'
    option mode 'full_proxy'
```

```text
config client_rule
    option enabled '1'
    option name 'Рабочий ПК'
    option target '192.168.1.55'
    option mode 'direct'
```

### Правила портов

```text
config port_rule
    option enabled '1'
    option name 'Telegram Voice'
    option protocol 'udp'
    option outbound 'proxy'

    list ports '500-1000'
    list ports '5222'
```

---

# 📊 Производительность

Chebur.NET проектируется прежде всего для обычных OpenWrt-роутеров.

Сам демон `cheburnetd` не является proxy-data-plane. Его задача — управление и оркестрация.

В реальном тестировании на роутере:

```text
cheburnetd
RSS: ~16 MB
CPU: ~0%
```

Пример с sing-box:

```text
cheburnetd
RSS: ~16 MB

sing-box
RSS: ~31 MB
```

Таким образом, суммарное потребление двух основных процессов в состоянии простоя составляло около:

```text
~47 MB RSS
```

Фактическое потребление зависит от версии sing-box, количества нод, rulesets, DNS, активных соединений и нагрузки.

---

# 📦 Установка

Для OpenWrt ≤ 23.05 используется `opkg`.

Для OpenWrt 24.x+ используется `apk`.

Пример:

```bash
opkg update

opkg install ip-full nftables ca-bundle curl

opkg install sing-box

opkg install luci-app-cheburnet.ipk
```

Для APK:

```bash
apk update

apk add --allow-untrusted luci-app-cheburnet.apk
```

> Версия и архитектура пакета должны соответствовать конкретному релизу и платформе OpenWrt.

---

# 🔨 Ручная сборка

Go daemon:

```bash
CGO_ENABLED=0 \
GOOS=linux \
GOARCH=arm64 \
go build \
    -ldflags="-s -w" \
    -trimpath \
    -o bin/cheburnetd \
    ./cmd/cheburnetd
```

Для MediaTek MT7621:

```bash
CGO_ENABLED=0 \
GOOS=linux \
GOARCH=mipsle \
GOMIPS=softfloat \
go build \
    -ldflags="-s -w" \
    -trimpath \
    -o bin/cheburnetd \
    ./cmd/cheburnetd
```

Опциональное сжатие:

```bash
upx --best --lzma bin/cheburnetd
```

---

# 🔧 Управление

```bash
/etc/init.d/cheburnet start
/etc/init.d/cheburnet stop
/etc/init.d/cheburnet restart
/etc/init.d/cheburnet reload
```

Логи:

```bash
logread -e cheburnetd
logread -e sing-box
```

Проверка конфигурации sing-box:

```bash
sing-box check -c /tmp/run/cheburnet/sing-box.json
```

---

# 🔐 Проектная философия

Chebur.NET не пытается заменить OpenWrt или sing-box.

Роли разделены:

```text
OpenWrt
   │
   ├── Linux networking
   ├── nftables
   ├── policy routing
   ├── dnsmasq
   └── UCI
          │
          ▼
     Chebur.NET
          │
          ├── subscriptions
          ├── nodes
          ├── policies
          ├── routing
          ├── health
          ├── diagnostics
          └── configuration
                    │
                    ▼
                 sing-box
```

**Chebur.NET управляет системой.  
sing-box передаёт проксируемый трафик.  
OpenWrt предоставляет сетевую инфраструктуру.**

Такое разделение позволяет сохранять сам daemon небольшим и не дублировать функциональность proxy-core.

---

# 🧪 Текущее состояние

Проект активно развивается.

Основные направления:

- стабильная работа sing-box;
- автоматическое управление подписками;
- безопасное обновление конфигурации;
- health monitoring;
- минималистичная диагностика;
- WebSocket events;
- client policies;
- rulesets;
- оптимизация потребления ресурсов;
- улучшение совместимости с различными версиями sing-box.

---

# 📜 Лицензия

Chebur.NET распространяется под лицензией **WTFPL**.

См. файл `LICENSE`.