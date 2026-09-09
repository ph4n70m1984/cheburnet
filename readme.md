# Chebur.NET 🧅⚡

**Chebur.NET** — модульный демон прозрачного проксирования и выборочной маршрутизации сетевого трафика для роутеров под управлением OpenWrt.

Основная логика работает внутри компактного Go-демона `cheburnetd`: загрузка и фильтрация подписок, разбор прокси-нод, генерация конфигураций sing-box/Xray, маршрутизация, Client Policy, Route Policy, контроль состояния ядер, безопасная перезагрузка, автоматическое восстановление и обновление компонентов.

Веб-интерфейс LuCI используется как управляющая панель, а взаимодействие с демоном выполняется через REST API и WebSocket.


> **Текущий релиз: `v0.0.8.4`**  
> Диагностика системы, event-driven WebSocket-события, hysteresis, root-cause correlation, безопасная работа подписчиков WebSocket, 16 реальных health-checks, улучшения UI и оптимизация Go runtime для OpenWrt.

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

# 🩺 Системная диагностика

Начиная с `v0.0.8.1`, а в `v0.0.8.4` — с исправлениями конкурентного доступа и состояния,
Chebur.NET имеет отдельный event-driven слой диагностики.

Diagnostics не дублирует Supervisor: он анализирует уже собранные `HealthSnapshot`,
определяет активные проблемы и передаёт события в LuCI. Supervisor остаётся владельцем
операций восстановления engine.

### 16 встроенных проверок

Диагностика включает:

**Engine**
- `engine.process_down`
- `engine.process_unstable`
- `engine.port_unavailable`
- `engine.config_invalid`

**DNS**
- `dns.listener_down`
- `dns.proxy_unavailable`
- `dns.bootstrap_failed`
- `dns.high_latency`

**Connectivity**
- `connectivity.internet_unreachable`
- `connectivity.proxy_e2e_failed`

**Nodes**
- `nodes.no_available`
- `nodes.partial_unavailable`
- `nodes.all_failed`

**Routing / configuration**
- `routing.ip_rule_missing`
- `routing.nftables_invalid`
- `config.drift`

### Hysteresis

Для предотвращения ложных срабатываний состояние проблемы не меняется после одного
случайного сбоя.

Используются политики с разным порогом:

- normal — несколько последовательных failures/successes;
- strict — для критических состояний;
- soft — для нестабильных/latency-проверок.

Это позволяет избежать мигания ошибок при кратковременных сетевых сбоях.

### Root-cause correlation

Если одна неисправность вызывает несколько вторичных симптомов, диагностика старается
показать пользователю первопричину вместо списка одинаковых ошибок.

Например:

```text
Engine process down
        │
        ├── proxy port unavailable
        ├── DNS proxy unavailable
        ├── proxy E2E failed
        └── nodes unavailable
```

В UI при этом отображается основной incident, а зависимые симптомы используются
для технической детализации.

### WebSocket events

LuCI получает диагностические события без постоянного polling:

```text
diagnostic.snapshot
diagnostic.problem_created
diagnostic.problem_resolved
```

При подключении клиент получает текущий snapshot, после чего получает только изменения
состояния.

Это уменьшает лишний WebSocket-трафик и позволяет интерфейсу обновлять incident cards
сразу после изменения состояния.

### Безопасность конкурентного доступа

В `v0.0.8.4` исправлены важные race conditions:

- snapshot проблем копируется глубоко;
- карты `Details` не разделяются между внутренним состоянием и клиентом;
- subscriber channel больше не закрывается конкурентно с broadcast;
- неинициализированные health snapshots не создают ложный статус healthy;
- число проверок в API/UI соответствует реальным 16 checks.

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
 - системная диагностика и incident cards;

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

### Текущий релиз

urlChebur.NET v0.0.8.4https://github.com/ph4n70m1984/cheburnet/releases/tag/v0.0.8.4


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

В `v0.0.8.4` диагностика использует 16 встроенных проверок состояния engine,
DNS, connectivity, nodes, routing и configuration drift.

Проверяется:

- процесс engine;
- proxy port;
- конфигурация engine;
- локальный DNS;
- bootstrap DNS;
- DNS latency;
- доступность внешнего Internet;
- proxy E2E connectivity;
- доступность proxy nodes;
- `ip rule`;
- nftables/TProxy;
- drift сетевой конфигурации.

Состояния проблем проходят через hysteresis, а связанные симптомы могут быть
сгруппированы вокруг первопричины.

Полный текущий snapshot доступен через REST API, а изменения состояния передаются
через WebSocket events.

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

# 📌 Что нового в `v0.0.8.4`

Текущий README соответствует релизной кодовой базе `v0.0.8.4`.

### Диагностика и отказоустойчивость

- отдельный `DiagnosticsEngine`;
- **16 реальных системных health-checks**;
- hysteresis для защиты от flapping;
- root-cause correlation;
- event-driven diagnostic WebSocket;
- `diagnostic.snapshot`;
- `diagnostic.problem_created`;
- `diagnostic.problem_resolved`;
- безопасная работа с WebSocket subscribers;
- deep-copy диагностических snapshots;
- корректная обработка неинициализированного health state;
- реальные действия восстановления:
  - `restart_engine`;
  - `fix_routing`;
  - `reload_firewall`;
- неподдерживаемые diagnostic actions теперь возвращают ошибку вместо silent no-op.

### Engine Supervisor

- L1/L2 health checks;
- restart backoff;
- cooldown;
- контроль proxy-порта;
- локальный DNS health check;
- внешний E2E health check.

### SafeReload

- native binary validation;
- backup рабочего состояния;
- reload/restart;
- post-start health check;
- автоматический rollback при неуспешном применении.

### Updater

- native `.ipk` / `.apk` package selection;
- raw binary fallback;
- SHA256 verification;
- сохранение пользовательского `/etc/config/cheburnet`;
- CLI `check_updates`;
- CLI `upgrade`;
- обновление Chebur.NET, sing-box и Xray-core.

### Routing

- Client Policy;
- Route Policy;
- custom domains;
- custom subnets;
- custom destination ports и диапазоны;
- Discord UDP routing;
- sing-box и Xray builders;
- MIPS/MIPSel CI.

### Runtime для OpenWrt

В `v0.0.8.4` runtime-настройки Go были скорректированы для роутеров с ограниченными
ресурсами:

```text
GOMEMLIMIT=48MiB
GOGC=25
GODEBUG=madvdontneed=1
```

При этом искусственное ограничение виртуального адресного пространства процесса было
убрано, чтобы не создавать лишнее ограничение для Go runtime.

# 📜 Лицензия

Проект распространяется под лицензией **WTFPL**.

---

## Ссылки

**GitHub:**

[github.com/ph4n70m1984/cheburnet](https://github.com/ph4n70m1984/cheburnet?utm_source=chatgpt.com)

**Releases:**

[Chebur.NET Releases](https://github.com/ph4n70m1984/cheburnet/releases?utm_source=chatgpt.com)