# Chebur.NET 🧅⚡

**Chebur.NET** — модульный демон прозрачного проксирования и выборочной маршрутизации сетевого трафика для роутеров под управлением OpenWrt.

Основная логика работает внутри компактного Go-демона `cheburnetd`: загрузка и фильтрация подписок, разбор proxy-нод, генерация конфигураций `sing-box` / `Xray`, маршрутизация, Client Policy, Route Policy, контроль состояния ядер, безопасная перезагрузка, автоматическое восстановление, диагностика и обновление компонентов.

Веб-интерфейс LuCI используется как управляющая панель, а взаимодействие с демоном выполняется через REST API и WebSocket.

> **Текущий релиз: `v0.8.12`**

---

## ✨ Основные возможности

### 🔥 Два прокси-ядра

Chebur.NET поддерживает два независимых proxy engine:

- **sing-box**
- **Xray-core**

Оба ядра работают через единый интерфейс `Engine`, поэтому основная логика Chebur.NET не привязана к конкретному движку.

Поддерживаются:

- генерация конфигурации;
- проверка конфигурации непосредственно бинарником ядра;
- запуск и остановка процесса;
- безопасная перезагрузка;
- проверка состояния после запуска;
- автоматическое восстановление при отказе;
- сбор состояния и health-метрик;
- переключение engine через LuCI.

Xray builder постепенно приведён к функциональному паритету с sing-box для основных режимов маршрутизации и TProxy.

---

## 🚀 Поддерживаемые протоколы

Внутренняя модель `GenericNode` позволяет работать с:

- **VLESS**
  - Reality
  - XTLS-Vision
- **Hysteria2**
- **Trojan**
- **Shadowsocks**
- **SOCKS5**

Дополнительные параметры нод сохраняются при обработке:

- UUID;
- password;
- username;
- encryption method;
- flow;
- network;
- TLS security;
- SNI;
- fingerprint;
- Reality public key;
- Reality short ID;
- HTTP Host;
- path;
- HWID;
- obfuscation;
- port range;
- SOCKS version.

---

# 🧩 Источники нод

## Подписки

Поддерживаются современные форматы:

- Base64;
- Clash YAML;
- JSON;
- JSON-массивы профилей.

Совместимы источники, используемые в экосистемах:

- 3X-UI;
- Remnawave;
- Marzban;
- Xray-подобные JSON API;
- Happ;
- Clash / ClashMeta;
- другие совместимые JSON/YAML профили.

Для каждой HTTP-подписки можно задавать собственный `User-Agent`.

---

## Несколько подписок одновременно

Можно использовать несколько независимых секций `subscription`.

Каждая подписка может иметь собственные:

- имя;
- URL;
- User-Agent;
- HWID;
- enabled/disabled;
- regex-фильтры.

Подписки могут использоваться одновременно с ручными нодами.

---

## Ручные ноды

Помимо подписок поддерживается ручное добавление URI:

```text
vless://...
hysteria2://...
trojan://...
ss://...
socks5://...
```

Ручные ноды могут использоваться для:

- собственных серверов;
- резервных подключений;
- тестирования;
- локальных прокси;
- нод, отсутствующих в подписке.

---

# 🧹 Фильтрация подписок

Для каждой подписки можно задавать `exclude_regex`.

Нода исключается, если регулярное выражение совпадает с её:

- tag;
- outbound tag;
- remark/profile name.

Например:

```text
exclude_regex:
  - "RU-.*"
  - "test"
  - "expired"
  - "backup"
```

Фильтрация выполняется до построения конфигурации proxy engine.

---

# 🎯 Client Policy

Chebur.NET позволяет управлять маршрутизацией отдельных устройств локальной сети.

Для клиента можно указать:

- IP;
- MAC;
- имя;
- режим маршрутизации;
- enabled/disabled.

Поддерживаются три режима.

### `rules`

Обычная маршрутизация через ruleset:

```text
Client
   ↓
rules
   ↓
обычная маршрутизация Chebur.NET
```

### `full_proxy`

Весь трафик устройства направляется через proxy:

```text
Device
   ↓
full_proxy
   ↓
Proxy Engine
   ↓
VPN / Proxy
```

### `direct`

Устройство полностью исключается из проксирования:

```text
Device
   ↓
direct
   ↓
Internet
```

Цель политики может задаваться IP или MAC.

---

# 🛣️ Route Policy

`Client Policy` определяет поведение устройства.

`Route Policy` позволяет отдельно управлять маршрутизацией сервисов и типов трафика.

Например:

- AI;
- YouTube;
- Telegram;
- Discord;
- Google;
- рабочие сервисы.

Каждая политика может содержать:

- имя;
- enabled;
- rulesets;
- пользовательские домены;
- пользовательские подсети;
- custom ports;
- конкретный outbound.

Пример:

```text
config route_policy
    option name 'AI'
    option enabled '1'
    option outbound 'AUTO'

    list rulesets 'google_ai'
    list custom_domains 'chatgpt.com'
    list custom_domains 'openai.com'
```

---

# 🌐 Пользовательские домены и подсети

Помимо готовых ruleset можно добавлять собственные правила.

### Домены

```text
example.com
api.example.com
*.example.com
```

### CIDR

```text
1.2.3.0/24
10.20.30.0/24
```

Правила могут использоваться:

- в основной маршрутизации;
- внутри `Route Policy`;
- для отдельных сервисов;
- в клиентских политиках.

---

# 🔌 Пользовательские порты

Поддерживается маршрутизация по destination port.

Можно указывать:

```text
443
8443
50000-50100
50000:50100
```

Поддерживаются отдельные TCP/UDP-порты и диапазоны.

Это особенно полезно для приложений, которые невозможно надёжно определить только по доменам или IP.

---

# 🎮 Discord / WebRTC UDP routing

Для Discord предусмотрена дополнительная обработка UDP-трафика.

При использовании Discord ruleset могут создаваться правила для:

- UDP `443`;
- UDP `50000-65535`.

Это позволяет проксировать:

- Discord Voice;
- WebRTC media;
- UDP handshake traffic.

Работает с обоими engine:

- sing-box;
- Xray-core.

---

# 📚 Ruleset

Chebur.NET поддерживает:

- системные ruleset;
- динамические ruleset;
- локальные списки;
- пользовательские домены;
- пользовательские CIDR;
- пользовательские порты.

Ruleset могут использоваться в:

- основной маршрутизации;
- `Route Policy`;
- клиентских политиках;
- правилах отдельных сервисов;
- Discord routing.

---

# 💾 Локальные списки

Можно использовать собственные `.lst` файлы:

```text
/usr/share/cheburnet/lists/custom.lst
```

Это позволяет добавлять локальные списки без изменения исходного кода.

---

# ⚖️ Балансировка

Chebur.NET поддерживает группы выходных нод.

Группа может содержать:

- список нод;
- стратегию выбора;
- URL проверки;
- интервал проверки;
- tolerance.

Пример:

```text
AUTO
 ├── node-1
 ├── node-2
 ├── node-3
 └── node-4
```

Для автоматического выбора используются проверки доступности и задержки.

---

# 🧠 Immutable snapshots

Конфигурационное состояние демона организовано через безопасные snapshots.

Это позволяет:

- читать состояние без долгих блокировок;
- строить конфигурацию из согласованного snapshot;
- избегать частично изменённого состояния;
- безопасно выполнять параллельные API, telemetry и engine операции.

Изменение состояния отделено от применения конфигурации к proxy engine.

---

# 🔄 Safe Reload и Rollback

Изменение конфигурации не выполняется по принципу:

```text
restart → надеяться, что заработало
```

Используется последовательность:

```text
UCI / API
   ↓
State Snapshot
   ↓
Build Config
   ↓
Validate Config
   ↓
Backup
   ↓
Reload / Restart
   ↓
Health Check
   ↓
      OK ─────→ новый конфиг активен
       │
      FAIL
       ↓
    Rollback
```

Перед запуском конфигурация проверяется непосредственно соответствующим бинарником:

```text
sing-box check
```

или:

```text
xray -test
```

Если конфигурация не проходит проверку, рабочий процесс не затрагивается.

При ошибке запуска или health check выполняется автоматический rollback.

---

# 🛡️ Защита процесса

Lifecycle proxy engine контролируется демоном.

При завершении процесса:

1. отправляется `SIGTERM`;
2. ожидается штатное завершение;
3. после timeout применяется `SIGKILL`;
4. ожидается фактическое завершение процесса.

Это предотвращает:

- zombie processes;
- зависшие процессы;
- занятые TProxy-порты;
- проблемы повторного запуска.

Дочерний процесс получает `Pdeathsig`, поэтому при аварийном завершении родительского процесса engine не должен оставаться запущенным отдельно.

---

# ❤️ Engine Supervisor

Supervisor контролирует состояние proxy engine и автоматически восстанавливает работу при сбоях.

Используются два уровня проверки.

### L1 — локальная проверка

Проверяется:

- наличие процесса;
- доступность proxy-порта;
- DNS через локальный endpoint.

Проверка выполняется периодически.

### L2 — E2E

Проверяется реальный внешний HTTP-трафик через proxy.

Это позволяет отличить:

```text
процесс запущен
```

от:

```text
proxy реально передаёт трафик
```

При повторяющихся ошибках используется restart с backoff:

```text
0s
5s
15s
30s
60s
```

Также применяется cooldown для защиты роутера от бесконечного цикла перезапусков.

---

# 🌐 DNS

DNS вынесен в отдельный inbound, по умолчанию:

```text
1053
```

Это позволяет не конфликтовать с системным `dnsmasq`.

Поддерживаются:

- UDP;
- DoH;
- DoT;
- bootstrap DNS;
- TTL.

DNS diagnostics выполняются непосредственно из Go.

---

# 🔐 HWID

Chebur.NET умеет автоматически генерировать HWID на основе MAC-адреса устройства.

Это используется для конфигураций, где сервер ожидает уникальный идентификатор клиента.

Можно также задать собственный HWID.

---

# 📡 REST API + WebSocket

`cheburnetd` предоставляет API для LuCI и других клиентов.

Основной API:

```text
:8088
```

WebSocket используется для:

- live telemetry;
- статусов;
- latency;
- engine events;
- supervisor events;
- diagnostics;
- updater operations.

WebSocket поддерживает двусторонний обмен сообщениями.

---

# 🩺 Системная диагностика

Chebur.NET имеет отдельный event-driven слой диагностики.

Diagnostics анализирует состояние системы и уже собранные health snapshots.

Supervisor при этом остаётся владельцем операций восстановления engine.

## 16 встроенных checks

### Engine

```text
engine.process_down
engine.process_unstable
engine.port_unavailable
engine.config_invalid
```

### DNS

```text
dns.listener_down
dns.proxy_unavailable
dns.bootstrap_failed
dns.high_latency
```

### Connectivity

```text
connectivity.internet_unreachable
connectivity.proxy_e2e_failed
```

### Nodes

```text
nodes.no_available
nodes.partial_unavailable
nodes.all_failed
```

### Routing / configuration

```text
routing.ip_rule_missing
routing.nftables_invalid
config.drift
```

---

## Hysteresis

Для защиты от кратковременных сбоев применяется hysteresis.

Состояние проблемы не меняется после одного случайного failure.

Используются разные политики:

- `normal`;
- `strict`;
- `soft`.

Это предотвращает постоянное появление и исчезновение ошибок при нестабильной сети.

---

## Root-cause correlation

Если одна неисправность вызывает несколько вторичных симптомов, диагностика старается показать пользователю первопричину.

Например:

```text
Engine process down
        │
        ├── proxy port unavailable
        ├── DNS proxy unavailable
        ├── proxy E2E failed
        └── nodes unavailable
```

В интерфейсе отображается основной incident, а вторичные симптомы используются для технической детализации.

---

## Event-driven WebSocket diagnostics

LuCI не обязан постоянно опрашивать состояние.

Используются события:

```text
diagnostic.snapshot
diagnostic.problem_created
diagnostic.problem_resolved
```

При подключении клиент получает текущий snapshot, после чего получает только изменения.

Это уменьшает лишний WebSocket-трафик и позволяет UI сразу отображать новые проблемы.

---

# 📊 Telemetry

Telemetry Hub позволяет нескольким клиентам одновременно получать состояние Chebur.NET.

Передаются:

- текущий engine;
- состояние процессов;
- ноды;
- latency;
- статусы;
- updater events;
- supervisor events.

Запись в WebSocket соединения выполняется потокобезопасно.

---

# 🔄 Менеджер обновлений

Chebur.NET содержит встроенный updater.

Он работает с:

```text
Chebur.NET
sing-box
xray-core
```

Поддерживаются:

- ручная проверка;
- автоматическая проверка;
- обновление через LuCI;
- CLI;
- native OpenWrt packages;
- raw binary fallback.

CLI:

```bash
cheburnetd check_updates
```

и:

```bash
cheburnetd upgrade [target]
```

---

# 📦 Multi-architecture Xray updater

Начиная с `v0.8.12`, updater автоматически сопоставляет архитектуру устройства с соответствующим официальным архивом Xray-core.

Поддерживаются:

| GOARCH | Xray asset |
|---|---|
| `arm64` | `Xray-linux-arm64-v8a.zip` |
| `amd64` | `Xray-linux-64.zip` |
| `386` | `Xray-linux-32.zip` |
| `arm` | ARMv5 / ARMv7 |
| `mipsle` | `Xray-linux-mips32le-softfloat.zip` |
| `mips` | `Xray-linux-mips32-softfloat.zip` |
| `mips64le` | `Xray-linux-mips64le-softfloat.zip` |
| `mips64` | `Xray-linux-mips64-softfloat.zip` |

Для ARM дополнительно учитывается вариант архитектуры (`v5`, `arm9` и т.д.).

Если архитектура не поддерживается, updater возвращает явную ошибку вместо выбора неправильного бинарника.

Xray устанавливается из **pinned version**, а перед заменой выполняется проверка контрольной суммы.

---

# 🔒 SHA256 verification

Перед установкой загруженного компонента выполняется SHA256-проверка.

CI публикует:

```text
sha256sums.txt
```

Несовпадающий файл не должен устанавливаться как корректное обновление.

---

# 💾 Защита пользовательской конфигурации

Обновление пакета не должно уничтожать:

```text
/etc/config/cheburnet
```

Для этого используются:

### OPKG

`conffiles`

### APK

pre-install / post-install hooks.

### Updater

Дополнительно существует backup/restore fallback.

Таким образом, обновление компонентов отделено от пользовательской конфигурации роутера.

---

# 🖥️ LuCI

LuCI предоставляет web-интерфейс управления Chebur.NET.

Основные возможности:

- выбор engine;
- routing mode;
- подписки;
- ручные ноды;
- Client Policy;
- Route Policy;
- rulesets;
- custom domains;
- custom subnets;
- custom ports;
- HWID;
- DNS;
- updater;
- состояние sing-box;
- состояние Xray;
- telemetry;
- live WebSocket status;
- системная диагностика;
- incident cards.

Интерфейс работает поверх REST API и WebSocket демона.

---

# 🏗️ Архитектура

```text
                         OpenWrt
                            │
             ┌──────────────┴──────────────┐
             │                             │
           LuCI                           UCI
             │                             │
             │ REST / WebSocket             │
             └──────────────┬──────────────┘
                            │
                            ▼
                  ┌───────────────────┐
                  │    cheburnetd     │
                  │      Go daemon    │
                  └─────────┬─────────┘
                            │
       ┌────────────────────┼────────────────────┐
       │                    │                    │
       ▼                    ▼                    ▼
 Subscription             Routing             Telemetry
   Worker                  Engine                 API
       │                    │                    │
       ▼             ┌──────┴──────┐             ▼
   Node parser       │             │         WebSocket
                     ▼             ▼
                 sing-box        Xray
                     │             │
                     └──────┬──────┘
                            ▼
                    Engine Supervisor
                            │
                    ┌───────┴───────┐
                    ▼               ▼
                  L1 health       L2 E2E
                    │               │
                    └───────┬───────┘
                            ▼
                       Linux Network
                            │
                ┌───────────┼───────────┐
                ▼           ▼           ▼
             nftables     ip rule     TProxy
```

Chebur.NET сознательно не реализует собственный proxy data plane.

Go-демон является **control plane**, а непосредственную передачу трафика выполняют:

- sing-box;
- Xray-core;
- Linux kernel;
- nftables;
- TProxy.

Это позволяет не нагружать Go-демон обработкой каждого сетевого пакета.

---

# 📁 Структура репозитория

```text
├── cmd/
│   └── cheburnetd/
│       └── main.go

├── internal/
│   ├── api/
│   │   └── server.go

│   ├── config/
│   │   ├── models.go
│   │   └── uci.go

│   ├── engine/
│   │   ├── engine.go
│   │   ├── process.go
│   │   ├── reload.go
│   │   ├── health.go
│   │   ├── singbox.go
│   │   ├── xray.go
│   │   ├── singbox/
│   │   │   └── builder.go
│   │   └── xray/
│   │       └── builder.go

│   ├── network/
│   │   └── ...

│   ├── rules/
│   │   └── ...

│   ├── subscription/
│   │   └── worker.go

│   ├── telemetry/
│   │   └── hub.go

│   └── updater/
│       └── manager.go

├── pkg/
│   ├── hwid/
│   └── uri/

├── openwrt/
│   └── files/
│       ├── etc/
│       │   ├── config/
│       │   │   └── cheburnet
│       │   └── init.d/
│       │       └── cheburnet
│       ├── usr/share/luci/
│       └── www/luci-static/

├── .github/
│   └── workflows/
│       └── build-packages.yml

├── Makefile
├── go.mod
├── go.sum
├── sha256sums.txt
└── README.md
```

---

# ⚙️ Конфигурация

Основной файл:

```text
/etc/config/cheburnet
```

Основные секции:

```text
config cheburnet 'main'
config subscription
config client_rule
config route_policy
```

Пример:

```text
config cheburnet 'main'
    option enabled '1'
    option engine 'sing-box'
    option routing_mode 'rules'

    option source_mode 'subscription'

    option auto_hwid '1'
    option custom_hwid ''

    option tproxy_port '1602'
    option dns_port '1053'
    option mixed_port '4534'

    option dns_protocol 'udp'
    option dns_server '1.1.1.1'
    option bootstrap_dns '77.88.8.1'

    list source_interfaces 'br-lan'

    list rulesets 'russia_inside'
    list rulesets 'youtube'
    list rulesets 'telegram'

    list custom_domains 'example.com'
    list custom_subnets '91.108.4.0/22'

    list custom_ports '443'
    list custom_ports '50000-65535'

    list local_list_files '/usr/share/cheburnet/lists/custom.lst'


config subscription
    option name 'Provider 1'
    option enabled '1'
    option user_agent 'Happ/4.1.3'
    option hwid ''
    option url 'https://example.com/api/v1/client/subscribe?token=xxx'

    list exclude_regex 'test'
    list exclude_regex 'expired'


config client_rule
    option enabled '1'
    option name 'Smart TV'
    option target '192.168.1.120'
    option mode 'full_proxy'


config client_rule
    option enabled '1'
    option name 'Work PC'
    option target '192.168.1.55'
    option mode 'direct'


config route_policy
    option enabled '1'
    option name 'AI'
    option outbound 'AUTO'

    list rulesets 'google_ai'
    list custom_domains 'chatgpt.com'
    list custom_domains 'openai.com'
```

---

# 🔀 Режимы маршрутизации

## Rules

Избирательное проксирование:

```text
LAN
 │
 ├── matching ruleset ──→ Proxy
 │
 └── everything else ──→ Direct
```

## Global

Весь перехваченный трафик направляется в активный proxy outbound.

## Client Policy

Переопределение маршрута для конкретного устройства:

```text
Client
 │
 ├── direct
 ├── full_proxy
 └── rules
```

## Route Policy

Переопределение маршрута для отдельных сервисов:

```text
Service
 │
 ├── ruleset
 ├── domain
 ├── subnet
 └── custom ports
       │
       ▼
    Outbound
```

---

# 📦 Установка

## Текущий релиз

**Chebur.NET `v0.8.12`**

Готовые пакеты публикуются в GitHub Releases.

Для OpenWrt с `opkg`:

```text
.ipk
```

Для OpenWrt с `apk`:

```text
.apk
```

### OPKG

```bash
opkg update

opkg install ip-full nftables ca-bundle curl

opkg install sing-box
# или:
# opkg install xray-core

opkg install luci-app-cheburnet_<version>_<arch>.ipk
```

### APK

Перед установкой рекомендуется проверить SHA256 по:

```text
sha256sums.txt
```

После проверки:

```bash
apk update

apk add --allow-untrusted \
    luci-app-cheburnet-<version>.<arch>.apk
```

---

# 🛠️ Ручная сборка

## ARM64

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

## MIPS Little Endian

Для MT7621:

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

CI/CD проекта также учитывает различные OpenWrt архитектуры.

---

# 🗜️ UPX

Для устройств с ограниченным flash/storage можно использовать:

```bash
upx --best --lzma bin/cheburnetd
```

---

# 🚀 Ручное развёртывание

```bash
ROUTER="root@192.168.1.1"
```

Демон:

```bash
scp bin/cheburnetd \
    $ROUTER:/usr/bin/cheburnetd

ssh $ROUTER \
    "chmod +x /usr/bin/cheburnetd"
```

Init script:

```bash
scp openwrt/files/etc/init.d/cheburnet \
    $ROUTER:/etc/init.d/cheburnet

ssh $ROUTER \
    "chmod +x /etc/init.d/cheburnet"
```

UCI:

```bash
scp openwrt/files/etc/config/cheburnet \
    $ROUTER:/etc/config/cheburnet
```

LuCI:

```bash
scp openwrt/files/usr/share/luci/menu.d/luci-app-cheburnet.json \
    $ROUTER:/usr/share/luci/menu.d/

scp openwrt/files/www/luci-static/resources/view/cheburnet/dashboard.js \
    $ROUTER:/www/luci-static/resources/view/cheburnet/
```

Включение:

```bash
ssh $ROUTER \
    "/etc/init.d/cheburnet enable"
```

---

# 🎛️ Управление сервисом

```bash
/etc/init.d/cheburnet start
/etc/init.d/cheburnet stop
/etc/init.d/cheburnet restart
```

Мягкая перезагрузка:

```bash
/etc/init.d/cheburnet reload
```

---

# 🔎 Логи

Демон:

```bash
logread -e cheburnetd
```

sing-box:

```bash
logread -e sing-box
```

Xray:

```bash
logread -e xray
```

---

# 🔄 Обновление через CLI

Проверка:

```bash
cheburnetd check_updates
```

Обновление Chebur.NET:

```bash
cheburnetd upgrade cheburnet
```

sing-box:

```bash
cheburnetd upgrade sing-box
```

Xray:

```bash
cheburnetd upgrade xray-core
```

Все компоненты:

```bash
cheburnetd upgrade all
```

---

# 🧪 Диагностика

Diagnostics контролирует:

- engine process;
- proxy port;
- engine configuration;
- локальный DNS;
- bootstrap DNS;
- DNS latency;
- Internet connectivity;
- proxy E2E connectivity;
- доступность proxy nodes;
- `ip rule`;
- nftables/TProxy;
- configuration drift.

Используются:

- hysteresis;
- root-cause correlation;
- event-driven WebSocket events.

Основные события:

```text
diagnostic.snapshot
diagnostic.problem_created
diagnostic.problem_resolved
```

---

# 🏎️ Принцип производительности

Chebur.NET не реализует собственный proxy data plane.

```text
                    Chebur.NET
                     Control Plane
                          │
             ┌────────────┼────────────┐
             ▼            ▼            ▼
            UCI         Routing       Health
             │            │            │
             └────────────┼────────────┘
                          ▼
                   sing-box / Xray
                          │
                          ▼
                     Linux kernel
```

Go отвечает за:

- конфигурацию;
- маршрутизацию;
- управление;
- subscriptions;
- health;
- API;
- orchestration.

Непосредственную передачу трафика выполняют:

- sing-box;
- Xray-core;
- Linux kernel;
- nftables;
- TProxy.

Go-демон не участвует в обработке каждого сетевого пакета.

---

# 🔐 Безопасность и отказоустойчивость

Ключевые механизмы:

- validation конфигурации до reload;
- backup рабочего состояния;
- rollback;
- post-start health check;
- Engine Supervisor;
- restart backoff;
- cooldown;
- защита от zombie processes;
- Pdeathsig;
- SHA256 verification обновлений;
- сохранение `/etc/config/cheburnet`;
- Safety Fallback для подписок;
- разделение control plane / data plane.

---

# 🧱 Основные компоненты

| Компонент | Назначение |
|---|---|
| `cheburnetd` | основной Go-демон |
| `sing-box` | proxy engine |
| `xray-core` | альтернативный proxy engine |
| `nftables` | перехват и маршрутизация |
| `ip rule` | policy routing |
| `dnsmasq` | системный DNS/LAN DHCP |
| UCI | конфигурация OpenWrt |
| LuCI | web-интерфейс |
| WebSocket | telemetry/control/events |
| Supervisor | контроль proxy engine |
| Diagnostics | диагностика проблем |
| Updater | обновление компонентов |
| Ruleset loader | загрузка и кэширование правил |

---

# 📌 Что нового в `v0.8.12`

### Xray updater

Основное изменение релиза:

- добавлен multi-architecture resolver для Xray-core;
- архитектура устройства автоматически сопоставляется с Xray asset;
- поддержаны ARM64;
- AMD64;
- 386;
- ARMv5;
- ARMv7;
- MIPS little-endian;
- MIPS big-endian;
- MIPS64 little-endian;
- MIPS64 big-endian;
- для MIPS учитывается soft-float;
- для ARM учитывается вариант архитектуры;
- при неподдерживаемой архитектуре updater возвращает явную ошибку;
- pinned Xray version сохраняется;
- SHA256 verification сохраняется.

Это делает встроенное обновление Xray существенно более пригодным для разных поколений OpenWrt-роутеров.

---

# 📜 Лицензия

Проект распространяется под лицензией **WTFPL**.

---

## 🔗 Ссылки

**GitHub:**  
https://github.com/ph4n70m1984/cheburnet

**Releases:**  
https://github.com/ph4n70m1984/cheburnet/releases