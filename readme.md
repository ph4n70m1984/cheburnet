# Chebur.NET 🧅⚡

**Chebur.NET** — модульный сервис прозрачного проксирования и интеллектуальной маршрутизации трафика для роутеров под управлением **OpenWrt**.

Проект представляет собой лёгкий скомпилированный демон на **Go**, который берёт на себя управление конфигурацией, подписками, парсинг прокси-нод, маршрутизацию, health-checks, диагностику, телеметрию и управление **sing-box**.

Веб-интерфейс LuCI взаимодействует с `cheburnetd` через **REST API и WebSocket**.

Главная идея проекта — не превращать обычный OpenWrt-роутер в сервер с десятками тяжёлых компонентов, а вынести сложную логику в один компактный управляющий демон и использовать **sing-box как единственный proxy engine**.

> **Текущая версия: v0.0.8.47-beta.5**

---

## ✨ Ключевые возможности

### 🚀 sing-box как единственный engine

Chebur.NET использует **sing-box** как основной и единственный proxy-core.

Поддержка Xray-core удалена из основной архитектуры: это уменьшает количество кода, конфигурационных веток и потенциальных расхождений между движками.

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

Через sing-box поддерживаются:

- **VLESS**
- **VLESS + Reality**
- **XTLS-Vision**
- **Hysteria2**
- **Trojan**
- **Shadowsocks**
- **SOCKS5**

Конкретный набор возможностей зависит от версии установленного `sing-box`.

---

# 📦 Подписки и автоматический парсинг

Chebur.NET умеет получать и разбирать подписки непосредственно на роутере.

Поддерживаются:

- Base64 subscription;
- Clash YAML;
- JSON;
- URI-профили;
- VLESS;
- Hysteria2;
- Trojan;
- Shadowsocks;
- SOCKS5;
- несколько источников подписок одновременно.

Для каждого источника могут использоваться собственные параметры:

- URL;
- User-Agent;
- HWID;
- фильтрация нод;
- регулярные выражения исключения.

### Фильтрация

Поддерживается `exclude_regex`, позволяющий исключать ноды по регулярным выражениям.

---

## 🆔 HWID

Chebur.NET поддерживает автоматический HWID.

При включённом:

```text
auto_hwid = 1
```

идентификатор может использоваться при работе с HWID-зависимыми подписками.

Для отдельных subscription sources также поддерживается собственный HWID.

---

## 🏷️ Уникальные теги нод

Ноды, полученные из разных подписок, получают префикс источника:

```text
[Subscription-A] Node-01
[Subscription-B] Node-01
```

Это предотвращает коллизии тегов при объединении нескольких подписок.

При необходимости одинаковые имена внутри одного источника дополнительно получают суффиксы:

```text
[Subscription-A] Node
[Subscription-A] Node (2)
[Subscription-A] Node (3)
```

---

# 🛡️ Safe Fallback / SafeReload

Обновление подписки или конфигурации не должно приводить к потере рабочего подключения.

Основной pipeline:

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
Keep     SafeReload
old      │
config   ▼
       Health
          │
       ┌──┴──┐
       │     │
      OK    FAIL
       │     │
       ▼     ▼
    active rollback
```

Перед применением нового конфига выполняется проверка `sing-box`.

Если генерация, загрузка, валидация или применение нового состояния завершаются ошибкой, существующая рабочая конфигурация сохраняется либо выполняется rollback.

---

# 🧠 Автономная работа подписок и Persistent Node Cache

Chebur.NET не рассматривает subscription URL как единственный источник работоспособности proxy-системы. Последний успешно применённый пул нод сохраняется на диске: 

```text
/etc/cheburnet/nodes_cache.json
```

При старте daemon: 

```text
Subscription
    │
    ├── SUCCESS ──────► fresh nodes
    │                       │
    │                       ▼
    │                  save cache
    │
    └── FAILURE ──────► persistent cache
                            │
                            ▼
                       restore nodes
```

Если отдельная подписка недоступна, Chebur.NET пытается восстановить именно её ранее сохранённые ноды. Если не удалось получить ни одной подписки, но полный persistent cache существует, daemon может восстановить весь последний сохранённый пул.

После успешного обновления новые ноды проходят обычный pipeline генерации и `SafeReload`; только после успешного применения состояния cache обновляется. При неудачном обновлении существующий рабочий pool сохраняется.

### Преимущества

- GitHub/subscription endpoint не является единственной точкой отказа для уже загруженных нод;
- перезапуск роутера не требует обязательного успешного скачивания всех подписок;
- временная недоступность источника не уничтожает последний рабочий пул;
- обновление выполняется атомарно относительно рабочего engine;
- неудачный subscription update не заменяет рабочее состояние пустым или повреждённым набором.

> Persistent cache — это cache последнего успешно принятого состояния нод, а не гарантия работоспособности самих серверов. Актуальность и доступность нод дополнительно контролируются Health/Adaptive.

---

# 🚨 Emergency Direct Subscription Bypass

Для загрузки подписок реализован двухступенчатый транспорт. Сначала запрос выполняется через `SmartTransport`, который учитывает состояние локального proxy engine. Если основной путь не работает, используется отдельный **аварийный прямой маршрут** с `SO_MARK`/policy-routing меткой, обходящей TProxy для служебного трафика daemon.

```text
Subscription request
        │
        ▼
   SmartTransport
        │
    proxy alive?
      /      \
    yes       no/fail
     │          │
     ▼          ▼
 mixed       Emergency
 proxy        Direct
     │          │
     └────┬─────┘
          ▼
       parser
```

Это особенно важно при восстановлении: сам `sing-box` может быть недоступен, но daemon получает возможность напрямую обновить subscription, если WAN при этом доступен.

Дополнительно аварийный маршрут используется с отдельной сетевой меткой, чтобы служебный трафик не зацикливался обратно через TProxy.

---

# 🛡️ CensorshipSentinel — обнаружение сетевой фильтрации

Chebur.NET содержит отдельный механизм определения сценария, когда зарубежный прямой доступ нарушен, но сеть в целом продолжает работать. `CensorshipSentinel` работает совместно с `StateController` и `AdaptiveWorker` и управляет именно **группой нод**, а не конкретным сервером.

Sentinel выполняет параллельное зондирование трёх независимых каналов:

1. **Direct Foreign** — прямой доступ к внешним контрольным endpoint;
2. **Direct Domestic** — доступ к отечественным контрольным ресурсам;
3. **Tunnel Foreign** — внешний запрос через локальный `mixed` inbound sing-box.

```text
                 CensorshipSentinel
                         │
          ┌──────────────┼──────────────┐
          ▼              ▼              ▼
   Direct Foreign  Direct Domestic  Tunnel Foreign
          │              │              │
          └──────────────┼──────────────┘
                         ▼
                 network diagnosis
                         │
                 ┌───────┴───────┐
                 │               │
              general           lte
```

Если зарубежный прямой канал недоступен, а domestic-контрольные точки доступны, система отличает этот сценарий от полной потери WAN. Отдельно контролируется работоспособность текущего туннеля.

### Гистерезис и защита от flap

Механизм использует асимметричное восстановление:

- **3 последовательных отрицательных цикла** для автоматического перехода в `lte`;
- **минимальный dwell time 2 минуты** после переключения;
- **8 последовательных успешных циклов** для возврата в `general`;
- после более чем 3 переключений за 10 минут добавляется **backoff penalty +5 проверок** к порогу восстановления.

Это уменьшает вероятность постоянного переключения между группами при нестабильном канале.

### Schedule Window

Можно задать временное окно `LTE` с учётом timezone роутера (`/etc/TZ`):

```text
21:00 ─────────────── 07:00
       LTE window
```

При активном окне переход в `lte` выполняется без ожидания обычного hysteresis-порога. После выхода из окна возврат в `general` снова проходит через проверку состояния.

### Преимущества

- отличает потерю WAN от сценария, когда domestic-сегмент доступен, а внешний прямой доступ нарушен;
- отделяет выбор группы от выбора конкретной ноды;
- уменьшает ложные переключения благодаря hysteresis и dwell time;
- позволяет заранее включать специальный пул по расписанию;
- не заставляет AdaptiveWorker искать узел вне разрешённой Sentinel-группы.

---

# 🧩 PoolClassifier — изоляция пулов нод

Все ноды могут классифицироваться в отдельные логические группы через regex + priority:

```text
                    Node Pool
                       │
             ┌─────────┼─────────┐
             ▼         ▼         ▼
          general     lte      custom
                       │
                  Sentinel
                       │
                  active group
```

Группа имеет: имя, приоритет, набор регулярных выражений и признак enabled. Более высокий priority обрабатывается раньше. Ноды, которые не совпали ни с одной пользовательской группой, попадают в `general`.

Это позволяет отделить, например:

- обычные зарубежные VPS;
- специальные узлы для обхода ограничений;
- игровые/UDP-пулы;
- message-пулы;
- любые пользовательские категории.

**Ключевое преимущество:** Sentinel может ограничить пространство поиска AdaptiveWorker только текущей разрешённой группой. Детектор сетевого режима и алгоритм выбора конкретного сервера не конфликтуют друг с другом.

---

# 🧠 AdaptiveWorker / многоуровневый выбор ноды

AdaptiveWorker не ограничивается одним ping. Выбор узла выполняется каскадно:

```text
Candidate Pool
      │
      ▼
 L1 — L4 / RTT
      │
      ▼
 L2 — HTTP latency
      │
      ▼
 top candidates
      │
      ▼
 L3 — throughput benchmark
      │
      ▼
 selected node
```

Для выбранных кандидатов учитываются: 

- L4 RTT;
- HTTP RTT;
- jitter;
- packet loss;
- throughput;
- итоговые scores.

В логах выбор отображается, например:

```text
Selected 'node' via L3-winner:
L4=62ms, HTTP=290ms, Jitter=2ms,
Loss=0%, Speed=444.8 KB/s,
L1Score=66.0, L3Score=116801.4
```

Перед полным probe используется короткоживущий cache результата, а при отсутствии пригодного результата предусмотрен fallback-путь выбора ноды. Одновременные одинаковые probe-запросы дополнительно дедуплицируются через singleflight.

### Преимущества

- выбор основывается не только на ICMP/TCP latency;
- плохой по throughput сервер не обязательно выигрывает только из-за низкого RTT;
- количество дорогих speed-test операций ограничивается предварительными этапами;
- AdaptiveWorker работает только внутри разрешённого текущего пула;
- кратковременные повторные запросы могут использовать cached результат.

---

# 🔁 Reassert — сохранение выбранной ноды

После reload/restart engine или синхронизации pool `StateController` выполняет `Reassert`: пытается восстановить ранее выбранную ноду в selector sing-box.

```text
engine restart / reload
          │
          ▼
     Reassert node
          │
     ┌────┴────┐
     │         │
   success   fallback
     │         │
     ▼         ▼
 selected   alternate
 selector   selector
```

Это уменьшает вероятность того, что после технического reload selector окажется на первой ноде конфигурации независимо от её реального качества.

---

# ♻️ Transactional SafeReload + Rollback

Применение новой конфигурации является транзакционным по смыслу: перед заменой активного состояния создаётся backup, новый конфиг проверяется, engine перезапускается/перезагружается и проходит health-check. При неуспехе выполняется rollback предыдущей конфигурации.

Дополнительно при проблеме применения nftables выполняется восстановление предыдущего engine-конфига и предыдущего firewall state.

```text
old state
   │
   ├── backup
   │
   ▼
new config
   │
   ├── validate
   ├── reload
   ├── health
   └── nftables
          │
       ┌──┴──┐
      OK    FAIL
       │      │
       ▼      ▼
    commit  rollback
```

**Преимущество:** ошибка нового конфига не должна автоматически превращаться в потерю предыдущего рабочего состояния.

---

# 🩺 Diagnostics 2.0 — проблемы вместо шума

Диагностический слой содержит **15 реальных проверок** состояния engine, DNS, connectivity, nodes, routing и config drift. Проверки проходят через отдельные hysteresis trackers.

Категории включают:

- `engine.process_down`;
- `engine.process_unstable`;
- `engine.port_unavailable`;
- `engine.config_invalid`;
- `dns.listener_down`;
- `dns.proxy_unavailable`;
- `dns.bootstrap_failed`;
- `dns.high_latency`;
- `connectivity.internet_unreachable`;
- `connectivity.proxy_e2e_failed`;
- `nodes.no_available`;
- `nodes.partial_unavailable`;
- `routing.ip_rule_missing`;
- `routing.nftables_invalid`;
- `config.drift`.

Каждая проблема имеет структурированный `problem_id`, component, severity, timestamps, occurrences, details, symptoms, recoverability и action.

Состояния tracker:

```text
CLEAR → PENDING → ACTIVE → RECOVERING → CLEAR
```

WebSocket передаёт только изменения состояния проблемы:

```text
diagnostic.problem_created
diagnostic.problem_updated
diagnostic.problem_resolved
```

Таким образом UI может оставаться минималистичным: здоровые проверки не превращаются в длинный список зелёных пунктов.

---

# 📊 Prometheus / Runtime Observability

Опциональная сборка с `-tags metrics` позволяет наблюдать не только сам daemon, но и состояние OpenWrt. Помимо стандартных Go/process metrics доступны CPU, память и network counters.

Это удобно для стресс-тестов и длительной эксплуатации: можно сопоставлять количество нод, частоту adaptive probes и recovery events с фактическим RSS/CPU/FD footprint.

---

---

# 🔍 Проверка sing-box

Перед использованием engine Chebur.NET проверяет установленный бинарник `sing-box`.

Проверяется:

- наличие бинарника;
- возможность запуска;
- версия;
- корректность вывода `sing-box version`;
- совместимость версии с текущим генератором конфигурации;
- наличие расширенной сборки sing-box.

Информация о binary engine представляется структурированно:

```text
Path
VersionRaw
Major
Minor
Patch
IsExtended
```

---

# 🧠 Интеллектуальная маршрутизация

Chebur.NET формирует маршрутизацию на основании:

- domain rules;
- IP/CIDR;
- готовых rulesets;
- пользовательских доменов;
- пользовательских подсетей;
- клиентских политик;
- портов и диапазонов портов;
- protocol-specific правил;
- route policies.

Поддерживаются режимы:

```text
LAN
 │
 ├── DIRECT
 │
 └── PROXY
        │
        ▼
     sing-box
```

---

# 👤 Client Policy

Для отдельных устройств локальной сети можно назначать собственные политики.

### `rules`

Выборочная маршрутизация согласно правилам:

```text
локальные / разрешённые ресурсы → DIRECT
остальные → PROXY
```

### `full_proxy`

Весь трафик устройства отправляется через proxy.

Подходит для:

- Smart TV;
- игровых приставок;
- телефонов;
- устройств без собственного VPN-клиента.

### `direct`

Устройство работает напрямую, минуя proxy routing.

Клиенты могут определяться по IP или MAC.

---

# 🛣️ Route Policy

Route Policy позволяет создавать отдельные логические политики маршрутизации для сервисов.

Политика может объединять:

- rulesets;
- домены;
- подсети;
- outbound.

Пример:

```text
Route Policy: AI
 ├── google_ai.srs
 ├── custom domains
 └── PROXY
```

или:

```text
Route Policy: YouTube
 ├── domains
 ├── subnets
 └── AUTO
```

---

# 🔢 Port Routing

Можно задавать маршрутизацию для отдельных портов и диапазонов:

```text
UDP 500-1000 → PROXY
UDP 5222     → PROXY
```

Поддерживается TCP/UDP в зависимости от правила и конфигурации engine.

---

# 🌐 DNS и FakeIP

Chebur.NET использует DNS-инфраструктуру sing-box для маршрутизации доменного трафика.

DNS работает через отдельный inbound, например:

```text
127.0.0.42:1053
```

что позволяет избежать конфликта с системным `dnsmasq`.

## FakeIP

Для FakeIP используется:

```text
198.18.0.0/15
```

В конфигурации создаётся отдельный DNS server:

```text
fakeip-dns
```

с типом:

```text
fakeip
```

FakeIP применяется к выбранным доменам/rulesets и интегрирован непосредственно в routing pipeline.

```text
                 DNS request
                      │
              ┌───────┴───────┐
              │               │
        proxy domains     direct domains
              │               │
              ▼               ▼
           FakeIP          normal DNS
        198.18.0.0/15
              │
              ▼
            TProxy
              │
              ▼
          sing-box
              │
              ▼
            Route
```

Также используется сохранение FakeIP-состояния в cache:

```text
experimental.cache_file
    store_fakeip = true
```

---

# ⚖️ Балансировка и URLTest

Chebur.NET может формировать группы proxy-нод и передавать их sing-box для автоматического выбора outbound.

Группа может использовать:

- список нод;
- URL для проверки;
- интервал проверки;
- tolerance;
- стратегию выбора.

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

---

# ❤️ Health Monitoring

Состояние engine и proxy-ноды отслеживается отдельным health-слоем.

Health используется для:

- определения состояния `sing-box`;
- проверки доступности порта;
- получения latency нод;
- обнаружения сбоев;
- передачи состояния Supervisor;
- принятия решений о восстановлении.

Это позволяет отделить **контроль состояния** от пользовательского интерфейса.

---

# 🩺 Diagnostics

Chebur.NET содержит отдельный диагностический слой.

Внутренние проверки выполняются постоянно, но UI не обязан отображать все технические проверки.

Пользователю передаются только актуальные проблемы.

```text
Diagnostics
     │
 ┌───┼───────────────┐
 │   │               │
 OK WARNING         ERROR
 │
 └─────── не отображается как проблема
```

Используются:

- `problem_id`;
- severity;
- состояние;
- hysteresis;
- occurrences;
- timestamps;
- symptoms;
- recoverability;
- action;
- WebSocket events.

Пример:

```json
{
  "type": "diagnostic",
  "problem_id": "engine.process_down",
  "severity": "critical",
  "state": "active"
}
```

После восстановления:

```json
{
  "type": "diagnostic",
  "problem_id": "engine.process_down",
  "severity": "critical",
  "state": "resolved"
}
```

Diagnostics работает совместно с Health/Supervisor и не превращает LuCI в постоянно открытый список внутренних проверок.

---

# 🔄 Supervisor и восстановление

Supervisor отвечает за lifecycle engine и автоматическое восстановление.

В случае проблем возможны действия:

- restart engine;
- восстановление routing;
- reload firewall;
- rollback конфигурации.

Диагностический слой определяет проблему, а механизм управления engine выполняет соответствующее действие.

---

# ⚡ Управление engine

В текущей архитектуре используется только `sing-box`.

Это позволяет:

- не поддерживать два разных proxy-core;
- уменьшить размер кодовой базы;
- исключить дублирование конфигураторов;
- сосредоточить тестирование на одном engine;
- использовать актуальные возможности sing-box;
- упростить диагностику и lifecycle management.

Перезапуск самого proxy-core выполняется через системный lifecycle управления процессом.

---

# 📊 Метрики и телеметрия

Chebur.NET имеет отдельную телеметрию для LuCI и опциональный Prometheus endpoint.

### Обычная сборка

Без build tag endpoint `/metrics` сообщает, что Prometheus metrics отключены.

### Сборка с метриками

Для включения Prometheus metrics:

```bash
CGO_ENABLED=0 go build -tags metrics -ldflags="-s -w" -o cheburnetd ./cmd/cheburnetd
```

После такой сборки доступен:

```text
/metrics
```

Сборщик предоставляет, в частности:

- Go runtime / GC metrics;
- goroutines;
- heap и allocation metrics;
- process CPU time;
- process RSS;
- process network RX/TX;
- open file descriptors;
- системную загрузку CPU;
- системную память;
- network RX/TX bytes;
- network RX/TX packets.

Пример основных системных метрик:

```text
node_cpu_utilization_ratio

node_memory_total_bytes
node_memory_available_bytes
node_memory_free_bytes
node_memory_used_bytes

node_network_receive_bytes_total
node_network_transmit_bytes_total
node_network_receive_packets_total
node_network_transmit_packets_total
```

Это позволяет наблюдать реальное потребление ресурсов OpenWrt без изменения основной логики daemon.

---

# 📈 Runtime footprint

Chebur.NET проектируется для обычных OpenWrt-роутеров и не требует тяжёлого пользовательского пространства.

### Наблюдение при пуле 100 нод

В одном из последних runtime/stress-тестов с большим пулом нод Prometheus зафиксировал:

```text
cheburnetd RSS             ≈ 24.5 MB
Go heap in use             ≈ 2.5 MB
Go runtime system memory  ≈ 14.3 MB
Goroutines                 = 23
Open FDs                  = 14
CPU utilization           ≈ 2.8%
```

За время работы процесс накопил около 4.45 GB total heap allocations, при этом live heap оставался около 2.5 MB. Медианное время GC pause составляло примерно 0.18 ms, а максимальное наблюдаемое значение — около 6.1 ms.

Это **результат конкретного стресс-теста, а не гарантированная верхняя граница**. Фактическое потребление зависит от нагрузки, количества активных соединений, частоты probes, правил маршрутизации, версии sing-box и размера rulesets.

Для сравнения с более ранними измерениями в проекте также встречается ориентир `cheburnetd ≈ 16 MB RSS`; это показывает, что footprint зависит от сценария и состава активных компонентов.

В одном из измерений совместный footprint двух основных процессов составлял около:

```text
cheburnetd ≈ 16 MB RSS
sing-box   ≈ 31 MB RSS
-----------------------
~47 MB RSS
```

Фактическое потребление зависит от:

- архитектуры CPU;
- версии sing-box;
- количества нод;
- размера rulesets;
- количества активных соединений;
- DNS/FakeIP;
- текущего сетевого трафика.

---

# 🖥️ LuCI

Веб-интерфейс:

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

Может отображать:

- состояние engine;
- ноды;
- latency;
- подписки;
- активные проблемы;
- health state;
- события;
- состояние маршрутизации;
- телеметрию.

WebSocket используется для реактивного обновления состояния без постоянного polling.

---

# 📦 Установка

## Быстрая установка / обновление

На OpenWrt с установленным `curl`:

```bash
sh -c "$(curl -fsSL https://raw.githubusercontent.com/ph4n70m1984/cheburnet/main/install.sh)"
```

Скрипт автоматически:

- определяет архитектуру устройства;
- определяет пакетный менеджер `apk` или `opkg`;
- показывает установленную версию Chebur.NET;
- показывает установленную версию sing-box;
- предлагает установить/обновить sing-box;
- проверяет необходимые зависимости;
- получает актуальный релиз Chebur.NET;
- выбирает пакет под архитектуру устройства;
- устанавливает/обновляет Chebur.NET;
- сохраняет существующую конфигурацию;
- управляет остановкой и запуском службы во время обновления.

Скрипт поддерживает как **OpenWrt 24.x+ / apk**, так и системы с **opkg**.

> Перед использованием установочного скрипта убедитесь, что устройство имеет доступ к GitHub и достаточно свободного места для пакетов.

---

# 📦 Ручная установка пакета

Для OpenWrt с `opkg`:

```bash
opkg update
opkg install ip-full nftables ca-bundle curl
opkg install sing-box
opkg install luci-app-cheburnet.ipk
```

Для OpenWrt с `apk`:

```bash
apk update
apk add --upgrade sing-box
apk add --allow-untrusted luci-app-cheburnet.apk
```

Версия и архитектура пакета должны соответствовать конкретному релизу и платформе OpenWrt.

---

# 🔨 Сборка

## Обычная сборка

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

Для MIPS:

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

## Сборка с Prometheus metrics

Для получения endpoint `/metrics`:

```bash
CGO_ENABLED=0 go build -tags metrics -ldflags="-s -w" -o cheburnetd ./cmd/cheburnetd
```

Build tag `metrics` включает:

```text
internal/api/metrics_enabled.go
internal/api/system_collector.go
```

Без `-tags metrics` используется облегчённая реализация без Prometheus metrics.

---

# 🔧 Управление службой

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

Проверка конфигурации:

```bash
sing-box check -c /tmp/run/cheburnet/sing-box.json
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
│   ├── config/
│   ├── diagnostics/
│   ├── engine/
│   │   └── singbox/
│   ├── network/
│   ├── rules/
│   ├── ruleset/
│   ├── subscription/
│   ├── telemetry/
│   └── updater/
│
├── pkg/
│   ├── happ/
│   ├── hwid/
│   └── uri/
│
├── openwrt/
│   ├── Makefile
│   └── files/
│
├── install.sh
├── Makefile
├── go.mod
└── readme.md
```

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
│                     ┌──────────────┼──────────────┐     │
│                     │              │              │     │
│                  Routing         Health       Diagnostics│
│                     │              │              │     │
│                     └──────────────┼──────────────┘     │
│                                    │                    │
│                               Supervisor               │
│                                    │                    │
│                               Updater                  │
│                                    │                    │
└────────────────────────────────────┼────────────────────┘
                                     │
                                     ▼
                              ┌─────────────┐
                              │  sing-box   │
                              │   FakeIP    │
                              │    DNS      │
                              │   Proxy     │
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

# ⚙️ Пример UCI-конфигурации

```text
config cheburnet 'main'
    option enabled '1'
    option engine 'sing-box'
    option routing_mode 'rules'
    option source_iface 'br-lan'

    option tproxy_port '1602'
    option dns_port '1053'
    option mixed_port '4534'

    option auto_hwid '1'
    option auto_update '1'

    option sub_user_agent 'Happ/1.0.0'

    list rule_sets 'russia_inside'
    list rule_sets 'youtube'
```

### Клиентская политика

```text
config client_rule
    option enabled '1'
    option name 'Apple TV'
    option target '192.168.1.120'
    option mode 'full_proxy'
```

### Пример Route Policy

```text
config route_policy
    option enabled '1'
    option name 'AI'
    option outbound 'AUTO'

    list rule_sets 'google_ai'
    list domains 'example.ai'
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
          ├── HWID
          ├── policies
          ├── routing
          ├── health
          ├── diagnostics
          ├── telemetry
          └── configuration
                    │
                    ▼
                 sing-box
```

**Chebur.NET управляет системой.  
sing-box обрабатывает проксируемый трафик.  
OpenWrt предоставляет сетевую инфраструктуру.**

Такое разделение позволяет сохранять daemon небольшим и не дублировать функциональность proxy-core.

---

# 🧪 Текущее состояние

**v0.0.8.47-beta.5**

Текущая ветка ориентирована на:

- sing-box как единственный engine;
- автоматическое управление подписками;
- HWID;
- FakeIP;
- DNS routing;
- client policies;
- route policies;
- rulesets;
- балансировку и URLTest;
- health monitoring;
- Supervisor;
- безопасное применение конфигурации;
- rollback;
- минималистичную диагностику;
- WebSocket events;
- REST API;
- опциональный Prometheus metrics endpoint;
- автоматические обновления;
- persistent cache последнего успешно применённого пула нод;
- аварийный direct-bypass для загрузки подписок при проблемах proxy engine;
- CensorshipSentinel с Direct Foreign / Direct Domestic / Tunnel Foreign probes;
- hysteresis, dwell time и anti-flap backoff;
- Schedule Window для специальных групп;
- PoolClassifier с regex + priority;
- AdaptiveWorker L4 → HTTP → throughput selection;
- Reassert выбранной ноды после reload/restart;
- transactional SafeReload + rollback engine и nftables;
- 15 диагностических checks с problem lifecycle и WebSocket events;
- низкое потребление ресурсов на OpenWrt.

---

# 📜 Лицензия

Chebur.NET распространяется под лицензией **WTFPL**.

См. файл `LICENSE`.
