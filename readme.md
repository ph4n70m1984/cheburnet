# Chebur.NET 🧅⚡

**Chebur.NET** — модульный демон прозрачного проксирования и выборочной маршрутизации сетевого трафика для роутеров под управлением OpenWrt.

Основная логика работает внутри компактного Go-демона `cheburnetd`: загрузка и фильтрация подписок, разбор прокси-нод, генерация конфигураций sing-box/Xray, маршрутизация, Client Policy, Route Policy, контроль состояния ядер, безопасная перезагрузка, автоматическое восстановление и обновление компонентов.

Веб-интерфейс LuCI используется как управляющая панель, а взаимодействие с демоном выполняется через REST API и WebSocket.

---

## ✨ Основные возможности

### 🔥 Два прокси-ядра

Chebur.NET поддерживает два независимых движка:

- **sing-box**
- **Xray-core**

Оба ядра работают через единый интерфейс `Engine`, поэтому основная логика Chebur.NET не привязана к конкретному прокси-движку.

Поддерживаются:

- генерация конфигурации;
- проверка конфигурации непосредственно бинарником ядра;
- запуск и остановка процесса;
- безопасная перезагрузка;
- проверка состояния после запуска;
- автоматическое восстановление при отказе;
- сбор состояния и метрик;
- переключение ядра через LuCI.

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

Также сохраняются дополнительные параметры нод:

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

Chebur.NET поддерживает несколько источников прокси-нод.

## Подписки

Поддерживаются подписки из современных панелей и клиентов в форматах:

- Base64;
- Clash YAML;
- JSON;
- JSON-массивы профилей.

Поддерживаются источники, встречающиеся в экосистемах:

- 3X-UI;
- Remnawave;
- Marzban;
- Xray-подобные JSON API;
- Happ;
- Clash/ClashMeta;
- другие совместимые JSON/YAML профили.

Для HTTP-запросов подписки можно задавать собственный `User-Agent`.

---

## Несколько подписок одновременно

Можно использовать несколько независимых `subscription` секций.

Каждая подписка имеет собственные:

- имя;
- URL;
- User-Agent;
- HWID;
- статус enabled/disabled;
- набор regex-фильтров.

Это позволяет объединять несколько провайдеров и фильтровать их ноды независимо друг от друга.

---

## Ручные ноды

Помимо подписок поддерживается **ручное добавление URI нод**.

Например:

```text
vless://...
hysteria2://...
trojan://...
ss://...
socks5://...
```

Ручные ноды хранятся отдельно от подписок и могут использоваться одновременно с ними.

Это удобно для:

- собственных серверов;
- тестовых нод;
- резервных подключений;
- локальных прокси;
- нод, которые отсутствуют в подписке.

---

# 🧹 Фильтрация подписок через регулярные выражения

Для каждой подписки можно задать `exclude_regex`.

Нода исключается, если регулярное выражение совпадает с её:

- tag;
- outbound tag;
- remark/profile name.

Пример:

```text
exclude_regex:
  - "RU-.*"
  - "test"
  - "expired"
  - "backup"
```

Это позволяет автоматически исключать ненужные ноды непосредственно после загрузки подписки.

Фильтрация выполняется до построения конфигурации прокси-ядра.

---

# 🎯 Client Policy

Chebur.NET позволяет управлять маршрутизацией отдельных устройств локальной сети.

Для каждого клиента можно указать:

- IP;
- MAC;
- имя;
- режим маршрутизации;
- enabled/disabled.

Поддерживаются три режима.

### `rules`

Обычная маршрутизация по правилам и ruleset.

```text
Клиент
   ↓
rules
   ↓
обычная маршрутизация Chebur.NET
```

### `full_proxy`

Весь трафик конкретного клиента направляется через прокси.

```text
Устройство
     ↓
full_proxy
     ↓
Proxy Engine
     ↓
VPN / Proxy
```

### `direct`

Клиент полностью исключается из проксирования.

```text
Устройство
     ↓
direct
     ↓
Internet
```

Цель политики может быть задана IP или MAC.

---

# 🛣️ Route Policy

В отличие от `Client Policy`, которая определяет поведение устройства, **Route Policy позволяет создавать отдельные правила маршрутизации для сервисов**.

Например:

```text
AI
YouTube
Telegram
Discord
Google
Рабочие сервисы
```

Каждая секция может содержать:

- имя;
- enabled;
- rulesets;
- пользовательские домены;
- пользовательские подсети;
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

Таким образом, разные сервисы могут направляться через разные прокси-ноды или балансировщики.

---

# 🌐 Пользовательские домены и подсети

Помимо готовых ruleset можно вручную добавить:

### Домены

```text
example.com
api.example.com
*.example.com
```

### Подсети

```text
1.2.3.0/24
10.20.30.0/24
```

Они могут использоваться как в основной маршрутизации, так и внутри отдельных `Route Policy`.

---

# 🔌 Пользовательские порты и диапазоны

Chebur.NET поддерживает маршрутизацию по destination port.

Можно указать:

```text
443
8443
50000-50100
50000:50100
```

Поддерживаются как отдельные TCP/UDP-порты, так и диапазоны.

Это позволяет создавать правила для приложений, которые невозможно корректно определить только по доменам или IP.

Например:

```text
443
3478
50000-65535
```

---

# 🎮 Discord / WebRTC UDP routing

Для Discord добавлена специальная обработка UDP-трафика.

При включении Discord ruleset Chebur.NET создаёт дополнительные правила для:

- UDP `443`;
- UDP `50000-65535`.

Это позволяет проксировать:

- Discord Voice;
- WebRTC media;
- UDP handshake traffic.

Маршрутизация выполняется через правила движка, без необходимости жёстко прописывать IP-диапазоны Discord.

Работает для **sing-box и Xray**.

---

# 📚 Ruleset

Chebur.NET умеет использовать локальные ruleset и списки подсетей.

Поддерживаются:

- системные ruleset;
- динамические ruleset;
- локальные списки;
- пользовательские домены;
- пользовательские CIDR;
- пользовательские порты.

Ruleset могут использоваться:

- в основной маршрутизации;
- в `Route Policy`;
- для отдельных сервисов;
- для Discord;
- для клиентских политик.

---

# 💾 Локальные списки

Можно указывать пути к локальным `.lst` файлам.

Например:

```text
/usr/share/cheburnet/lists/custom.lst
```

Это позволяет использовать собственные списки адресов без изменения исходного кода.

---

# ⚖️ Балансировка

Chebur.NET поддерживает группы выходных нод.

Группа может содержать:

- список нод;
- стратегию;
- URL проверки;
- интервал проверки;
- tolerance.

Например:

```text
AUTO
 ├── node-1
 ├── node-2
 ├── node-3
 └── node-4
```

Для автоматического выбора используется проверка доступности/задержки.

---

# 🧠 Thread-safe состояние и immutable snapshots

Конфигурационное состояние демона организовано через безопасные снимки состояния.

Это позволяет:

- читать конфигурацию без блокировки долгих операций;
- строить новый конфиг из согласованного snapshot;
- избегать частично изменённого состояния;
- безопасно выполнять параллельные операции API, telemetry и engine lifecycle.

Изменение состояния выполняется отдельно от применения конфигурации к прокси-ядру.

---

# 🔄 Безопасная перезагрузка

Изменение конфигурации не выполняется по принципу:

```text
перезапустить → надеяться, что заработало
```

Используется последовательность:

```text
UCI / API
    ↓
State Snapshot
    ↓
BuildConfig
    ↓
ValidateConfig
    ↓
Backup
    ↓
Reload
    ↓
Start
    ↓
Health Check
    ↓
OK ───────────────→ новый конфиг активен
    │
    └── FAIL ──────→ Rollback
```

Перед запуском новый конфиг проверяется непосредственно соответствующим бинарником:

```text
sing-box check
```

или:

```text
xray -test
```

Если конфигурация не проходит проверку, рабочий процесс не затрагивается.

При ошибке запуска или health check выполняется автоматический rollback к предыдущей конфигурации.

---

# 🛡️ Защита рабочего процесса

Остановка прокси-ядра реализована через контролируемый lifecycle.

`TerminateCmd`:

1. отправляет `SIGTERM`;
2. ожидает штатного завершения;
3. через timeout применяет `SIGKILL`;
4. ожидает фактического завершения процесса.

Это предотвращает:

- zombie processes;
- зависшие процессы;
- занятые TProxy-порты;
- проблемы при повторном запуске.

Дочерний процесс также получает `Pdeathsig`, поэтому при аварийном завершении родительского процесса ядро не должно оставаться запущенным отдельно.

---

# ❤️ Engine Supervisor

В Chebur.NET появился отдельный supervisor для прокси-ядра.

Он контролирует состояние запущенного двигателя и автоматически пытается восстановить работу при сбое.

Используются два уровня проверки.

### L1 — локальная проверка

Проверяется:

- наличие процесса;
- доступность локального proxy-порта;
- реальный DNS-запрос через локальный DNS endpoint.

Проверка выполняется примерно каждые 10 секунд.

### L2 — E2E проверка

Проверяется реальный внешний HTTP-трафик через proxy.

Проверка выполняется примерно каждые 60 секунд.

Это позволяет отличить:

```text
процесс запущен
```

от:

```text
proxy реально передаёт трафик
```

При повторяющихся ошибках supervisor выполняет restart с backoff.

Используется ступенчатая задержка восстановления:

```text
0s
5s
15s
30s
60s
```

Также применяется cooldown для защиты роутера от бесконечного цикла перезапусков.

---

# 🌐 DNS без утечек

DNS вынесен в отдельный inbound.

По умолчанию используется:

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

DNS diagnostics выполняются непосредственно из Go без зависимости от `nslookup`.

---

# 🔐 HWID

Chebur.NET умеет автоматически генерировать HWID на основе MAC-адреса устройства.

Это используется для VLESS Reality и других конфигураций, где сервер ожидает уникальный идентификатор клиента.

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
- задержек;
- обновлений;
- событий supervisor;
- операций updater.

WebSocket поддерживает двусторонний обмен сообщениями.

Это позволяет LuCI не только получать состояние, но и инициировать операции непосредственно через WebSocket.

---

# 📊 Telemetry

Telemetry Hub позволяет нескольким клиентам одновременно получать состояние Chebur.NET.

Передаются данные о:

- текущем engine;
- состоянии процессов;
- нодах;
- latency;
- статусах;
- обновлениях.

Запись в WebSocket соединения выполняется потокобезопасно.

---

# 🔄 Менеджер обновлений

В Chebur.NET появился встроенный updater.

Он может проверять обновления:

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

# 📦 OpenWrt package updater

Updater определяет:

- используемый пакетный менеджер;
- архитектуру устройства;
- доступный формат пакета.

Для OpenWrt используются native packages:

```text
.ipk
.apk
```

Если подходящего пакета нет, используется fallback на бинарное обновление.

Это особенно важно для разных архитектур роутеров.

---

# 🔒 SHA256 проверка обновлений

Перед установкой загруженного пакета или бинарника выполняется SHA256-проверка.

CI публикует единый файл:

```text
sha256sums.txt
```

Таким образом, повреждённый или несовпадающий файл не должен устанавливаться как корректное обновление.

---

# 💾 Защита пользовательской конфигурации

Обновление пакета не должно уничтожать:

```text
/etc/config/cheburnet
```

Для этого реализована защита конфигурации:

### OPKG

Используется `conffiles`.

### APK

Используются pre-install/post-install hooks.

### Updater

Дополнительно существует backup/restore fallback на уровне самого updater.

Таким образом, обновление Chebur.NET отделено от пользовательской конфигурации роутера.

---

# 🖥️ LuCI

LuCI предоставляет web-интерфейс управления Chebur.NET.

Основные возможности:

- выбор engine;
- управление routing mode;
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
- обновления;
- состояние sing-box;
- состояние Xray;
- telemetry;
- live WebSocket status.

Интерфейс обновляется без необходимости перезапускать сам `cheburnetd`.

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
                  ┌─────────────────────┐
                  │     cheburnetd      │
                  │       Go daemon     │
                  └──────────┬──────────┘
                             │
       ┌─────────────────────┼─────────────────────┐
       │                     │                     │
       ▼                     ▼                     ▼
 Subscription             Routing              Telemetry
   Worker                  Engine                 API
       │                     │                     │
       │          ┌──────────┴──────────┐          │
       │          │                     │          │
       ▼          ▼                     ▼          ▼
   Node URI   sing-box               Xray       WebSocket
   parser     builder                builder
       │          │                     │
       └──────────┴──────────┬──────────┘
                             │
                             ▼
                     Engine Supervisor
                             │
                   ┌─────────┴─────────┐
                   │                   │
                   ▼                   ▼
               L1 health             L2 E2E
                   │                   │
                   └─────────┬─────────┘
                             │
                             ▼
                       Linux Network
                             │
               ┌─────────────┼─────────────┐
               │             │             │
             nftables      ip rule       TProxy
```

---

# 📁 Структура репозитория

```text
├── cmd/
│   └── cheburnetd/
│       └── main.go                    # Точка входа демона
│
├── internal/
│   ├── api/
│   │   └── server.go                   # REST + WebSocket API
│   │
│   ├── config/
│   │   ├── models.go                   # Модели конфигурации
│   │   └── uci.go                      # Чтение/запись UCI
│   │
│   ├── engine/
│   │   ├── engine.go                   # Общий Engine interface
│   │   ├── process.go                  # Управление процессами
│   │   ├── reload.go                   # SafeReload + rollback
│   │   ├── health.go                   # Health checks
│   │   ├── singbox.go                  # Lifecycle sing-box
│   │   ├── xray.go                     # Lifecycle Xray
│   │   │
│   │   ├── singbox/
│   │   │   └── builder.go              # Генератор sing-box config
│   │   │
│   │   └── xray/
│   │       └── builder.go               # Генератор Xray config
│   │
│   ├── network/
│   │   └── ...                          # nftables / TProxy routing
│   │
│   ├── rules/
│   │   └── ...                          # Ruleset/cache
│   │
│   ├── subscription/
│   │   └── worker.go                   # Загрузка/парсинг подписок
│   │
│   ├── telemetry/
│   │   └── hub.go                      # WebSocket telemetry
│   │
│   └── updater/
│       └── manager.go                   # Update manager
│
├── pkg/
│   ├── hwid/
│   │   └── ...                          # HWID generator
│   │
│   └── uri/
│       └── ...                          # Proxy URI decoder
│
├── openwrt/
│   └── files/
│       ├── etc/
│       │   ├── config/
│       │   │   └── cheburnet             # UCI configuration
│       │   └── init.d/
│       │       └── cheburnet             # procd service
│       │
│       ├── usr/share/luci/
│       │   └── ...                       # LuCI menu
│       │
│       └── www/luci-static/
│           └── resources/view/cheburnet/
│               └── dashboard.js          # LuCI UI
│
├── .github/
│   └── workflows/
│       └── build-packages.yml             # CI/CD packages
│
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

---

# ⚙️ Конфигурационная модель

Основной конфигурационный файл:

```text
/etc/config/cheburnet
```

Chebur.NET использует UCI как источник конфигурации OpenWrt.

Основные секции:

```text
config cheburnet 'main'
config subscription
config client_rule
config route_policy
```

---

# 📝 Пример конфигурации

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


# Подписка
config subscription
    option name 'Provider 1'
    option enabled '1'

    option user_agent 'Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)'

    option hwid ''

    option url 'https://example.com/api/v1/client/subscribe?token=xxx'

    list exclude_regex 'test'
    list exclude_regex 'expired'
    list exclude_regex 'backup'


# Ручная нода
config cheburnet 'main'
    list manual_nodes 'vless://...'


# Client Policy
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


# Route Policy
config route_policy
    option enabled '1'
    option name 'AI'
    option outbound 'AUTO'

    list rulesets 'google_ai'
    list custom_domains 'chatgpt.com'
    list custom_domains 'openai.com'


config route_policy
    option enabled '1'
    option name 'YouTube'
    option outbound 'AUTO'

    list rulesets 'youtube'
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

Дополнительное переопределение маршрута для конкретных устройств:

```text
Client
 │
 ├── direct
 ├── full_proxy
 └── rules
```

## Route Policy

Дополнительное переопределение для отдельных сервисов:

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

# 🔄 Жизненный цикл изменения конфигурации

При изменении конфигурации Chebur.NET старается не затрагивать работающий engine до тех пор, пока новый конфиг не будет построен и проверен.

```text
UCI / LuCI / API
       │
       ▼
State Update
       │
       ▼
Immutable Snapshot
       │
       ▼
Build Config
       │
       ▼
Binary Validation
       │
       ▼
Backup
       │
       ▼
Engine Restart
       │
       ▼
Local Health Check
       │
       ├────────────── OK
       │               │
       │               ▼
       │           New Config
       │
       └──────────── FAIL
                       │
                       ▼
                    Rollback
```

---

# 📦 Установка

Готовые пакеты публикуются в GitHub Releases:

[GitHub Releases Chebur.NET](https://github.com/ph4n70m1984/cheburnet/releases?utm_source=chatgpt.com)

Для OpenWrt с `opkg` используется:

```text
.ipk
```

Для OpenWrt с `apk`:

```text
.apk
```

---

## OPKG

```bash
opkg update

opkg install ip-full nftables ca-bundle curl

opkg install sing-box
# или:
# opkg install xray-core

opkg install luci-app-cheburnet_<version>_<arch>.ipk
```

---

## APK

Перед установкой рекомендуется проверить SHA256 пакета по опубликованному:

```text
sha256sums.txt
```

После проверки:

```bash
apk update

apk add --allow-untrusted luci-app-cheburnet-<version>.<arch>.apk
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

## MIPS

Поддержка MIPS/MIPSel также включена в CI/CD matrix проекта.

---

# 🗜️ UPX

Для устройств с ограниченным flash/storage можно дополнительно использовать UPX:

```bash
upx --best --lzma bin/cheburnetd
```

---

# 🚀 Развёртывание вручную

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

UCI configuration:

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
```

```bash
/etc/init.d/cheburnet stop
```

```bash
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

# 🔄 Проверка обновлений через CLI

```bash
cheburnetd check_updates
```

Обновление конкретного компонента:

```bash
cheburnetd upgrade cheburnet
```

или:

```bash
cheburnetd upgrade sing-box
```

```bash
cheburnetd upgrade xray-core
```

Для комплексного обновления:

```bash
cheburnetd upgrade all
```

---

# 🧪 Диагностика

Chebur.NET проверяет не только факт запуска процесса, но и работоспособность локальной proxy-инфраструктуры.

Проверяются:

- процесс engine;
- proxy port;
- DNS;
- запуск нового engine;
- конфигурация через native binary validation;
- E2E connectivity;
- состояние после reload;
- восстановление после engine failure.

---

# 🏎️ Принцип производительности

Chebur.NET сознательно не реализует собственный proxy data plane.

```text
                    Chebur.NET
                 Control Plane
                       │
        ┌──────────────┼──────────────┐
        ▼              ▼              ▼
      UCI            Routing        Health
        │              │              │
        └──────────────┼──────────────┘
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

Это позволяет не нагружать Go-демон обработкой каждого сетевого пакета.

---

# 🔐 Безопасность и отказоустойчивость

Ключевые защитные механизмы:

- проверка конфигурации до reload;
- backup рабочего конфига;
- rollback;
- health check после запуска;
- supervisor;
- restart backoff;
- защита от zombie processes;
- Pdeathsig для дочерних процессов;
- SHA256 verification обновлений;
- сохранение `/etc/config/cheburnet` при package upgrade;
- Safety Fallback для подписок;
- разделение control plane и data plane.

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
| WebSocket | live telemetry/control |
| Updater | обновление компонентов |
| Ruleset loader | загрузка/кэширование правил |

---

# 📌 Что нового по сравнению со старой версией README

Текущая кодовая база уже содержит функции, которых не было в старой документации:

- `Route Policy` для маршрутизации отдельных сервисов;
- ручные proxy-ноды;
- regex-фильтрация нод подписки;
- JSON-array profiles;
- пользовательские destination ports;
- диапазоны портов;
- Discord UDP Voice/WebRTC routing;
- расширенная Client Policy;
- полноценный engine supervisor;
- L1/L2 health checks;
- exponential restart backoff;
- transactional SafeReload;
- automatic rollback;
- native binary config validation;
- controlled SIGTERM → SIGKILL process lifecycle;
- WebSocket update operations;
- встроенный Update Manager;
- CLI `check_updates`;
- CLI `upgrade`;
- SHA256 verification;
- сохранение пользовательского UCI-конфига при обновлении;
- различение установленных и отсутствующих proxy cores;
- native `.ipk` / `.apk` package selection;
- MIPS/MIPSel CI builds;
- thread-safe telemetry;
- immutable/thread-safe configuration snapshots;
- compressed/atomic ruleset caching;
- расширенная маршрутизация Xray;
- синхронизация Xray и sing-box по основным routing/TProxy возможностям.

---

# 📜 Лицензия

Проект распространяется под лицензией **WTFPL**.

---

## Ссылки

**GitHub:**

[github.com/ph4n70m1984/cheburnet](https://github.com/ph4n70m1984/cheburnet?utm_source=chatgpt.com)

**Releases:**

[Chebur.NET Releases](https://github.com/ph4n70m1984/cheburnet/releases?utm_source=chatgpt.com)