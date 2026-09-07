'use strict';
'require view';
'require form';
'require uci';
'require ui';
'require network';
'require tools.widgets as widgets';

return view.extend({
    load: function() {
        return Promise.all([
            network.getHostHints()
        ]);
    },

    render: function(data) {
        const hosts = (data && data[0]) ? data[0] : {};
        const m = new form.Map('cheburnet', _('Chebur.NET'),
            _('Управление прозрачным проксированием трафика на базе Sing-box и Xray-core'));

        // ==========================================
        // 1. СЕКЦИЯ ЖИВОЙ ТЕЛЕМЕТРИИ (LIVE STATUS)
        // ==========================================
        const statusSec = m.section(form.NamedSection, 'telemetry', 'cheburnet', _('Состояние и телеметрия в реальном времени'));
        statusSec.anonymous = true;

        statusSec.render = function() {
            const updateBanner = E('div', {
                'id': 'update-notification-banner',
                'style': 'display: none; align-items: center; justify-content: space-between; margin-bottom: 15px; padding: 12px 16px; border-radius: 6px; background: rgba(234, 179, 8, 0.15); border: 1px solid rgba(234, 179, 8, 0.4); color: #fef08a;'
            }, [
                E('span', { 'id': 'update-banner-text', 'style': 'font-size: 13px; font-weight: 500;' }, ''),
                E('button', {
                    'class': 'btn cbi-button-action',
                    'style': 'font-size: 12px; margin: 0; padding: 4px 12px;',
                    'click': function() {
                        ui.showIndicator('updating-system', _('Выполняется обновление компонентов...'));
                        fetch('http://' + window.location.hostname + ':8088/api/v1/updates/upgrade', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ target: 'all' })
                        })
                        .then(r => r.json())
                        .then(() => {
                            ui.hideIndicator('updating-system');
                            ui.addNotification(null, E('p', {}, _('Процесс обновления запущен в фоне.')), 'info');
                            const banner = document.getElementById('update-notification-banner');
                            if (banner) banner.style.display = 'none';
                        })
                        .catch(err => {
                            ui.hideIndicator('updating-system');
                            ui.addNotification(null, E('p', {}, _('Ошибка запуска: ') + err), 'error');
                        });
                    }
                }, _('Обновить сейчас'))
            ]);

            const table = E('table', { 'class': 'table', 'id': 'chebur-nodes-table' }, [
                E('tr', { 'class': 'tr table-titles' }, [
                    E('th', { 'class': 'th' }, _('Сервер / Тег')),
                    E('th', { 'class': 'th' }, _('Задержка')),
                    E('th', { 'class': 'th' }, _('Статус'))
                ]),
                E('tr', { 'class': 'tr', 'id': 'loading-row' }, [
                    E('td', { 'class': 'td', 'colspan': '3' }, _('Загрузка списка серверов...'))
                ])
            ]);

            const badgeStyle = 'background: rgba(255, 255, 255, 0.05); border: 1px solid rgba(255, 255, 255, 0.15); border-radius: 6px; padding: 8px 16px; min-width: 170px; display: flex; align-items: center; justify-content: space-between; gap: 10px;';

            const viewContainer = E('div', { 'class': 'cbi-section' }, [
                updateBanner,
                E('div', { 'style': 'display: flex; gap: 12px; margin-bottom: 18px; flex-wrap: wrap;' }, [
                    E('div', { 'style': badgeStyle }, [
                        E('span', { 'style': 'color: #8c8c8c; font-size: 13px;' }, _('Сервис демона:')),
                        E('span', { 'id': 'daemon-status', 'style': 'color: #8c8c8c; font-weight: bold; font-size: 13px;' }, '● Проверка...')
                    ]),
                    E('div', { 'style': badgeStyle }, [
                        E('span', { 'style': 'color: #8c8c8c; font-size: 13px;' }, _('Внешний IP:')),
                        E('span', { 'id': 'outbound-ip', 'style': 'color: #38bdf8; font-weight: bold; font-family: monospace; font-size: 13px;' }, 'Определение...')
                    ]),
                    E('div', { 'style': badgeStyle }, [
                        E('span', { 'style': 'color: #8c8c8c; font-size: 13px;' }, _('Всего серверов:')),
                        E('span', { 'id': 'total-nodes', 'style': 'color: #fbbf24; font-weight: bold; font-size: 14px;' }, '0')
                    ])
                ]),
                table
            ]);

            function checkUpdates() {
                const host = window.location.hostname;
                fetch('http://' + host + ':8088/api/v1/updates/check')
                    .then(r => r.json())
                    .then(data => {
                        if (!data) return;
                        let alerts = [];
                        if (data.cheburnet && data.cheburnet.has_update) {
                            alerts.push('Chebur.NET: ' + data.cheburnet.current + ' → ' + data.cheburnet.latest);
                        }
                        if (data.sing_box && data.sing_box.has_update) {
                            alerts.push('Sing-box: ' + data.sing_box.current + ' → ' + data.sing_box.latest);
                        }
                        if (data.xray && data.xray.has_update) {
                            alerts.push('Xray: ' + data.xray.current + ' → ' + data.xray.latest);
                        }
                        if (!data.auto_update && alerts.length > 0) {
                            const banner = document.getElementById('update-notification-banner');
                            const txt = document.getElementById('update-banner-text');
                            if (banner && txt) {
                                txt.textContent = 'Доступны обновления компонентов: ' + alerts.join(' | ');
                                banner.style.display = 'flex';
                            }
                        }
                    })
                    .catch(() => {});
            }

            function initTelemetry() {
                const host = window.location.hostname;
                const tbl = document.getElementById('chebur-nodes-table');

                function updateNodeUI(tag, latency) {
                    const latEl = document.getElementById('node-lat-' + tag);
                    const statusEl = document.getElementById('node-status-' + tag);
                    if (!latEl || !statusEl) return;

                    if (latency > 0) {
                        latEl.textContent = latency + ' ms';
                        latEl.style.color = latency < 200 ? '#4ade80' : (latency < 450 ? '#fb923c' : '#f87171');
                        statusEl.innerHTML = '<span style="color: #4ade80; font-weight: bold;">● Доступен</span>';
                    } else {
                        latEl.textContent = 'Timeout';
                        latEl.style.color = '#71717a';
                        statusEl.innerHTML = '<span style="color: #f87171; font-weight: bold;">● Офлайн</span>';
                    }
                }

                function syncClashDelays() {
                    fetch('http://' + host + ':9090/proxies')
                        .then(r => r.json())
                        .then(data => {
                            if (!data || !data.proxies) return;
                            Object.entries(data.proxies).forEach(([tag, info]) => {
                                if (info.history && info.history.length > 0) {
                                    const last = info.history[info.history.length - 1];
                                    if (last && last.delay !== undefined) {
                                        updateNodeUI(tag, last.delay);
                                    }
                                }
                            });
                        })
                        .catch(() => {});
                }

                fetch('http://' + host + ':8088/api/v1/nodes')
                    .then(r => r.json())
                    .then(nodes => {
                        if (!nodes || nodes.length === 0) {
                            const loadingRow = document.getElementById('loading-row');
                            if (loadingRow) loadingRow.cells[0].textContent = 'Нет активных серверов.';
                            return;
                        }

                        const countEl = document.getElementById('total-nodes');
                        if (countEl) countEl.textContent = nodes.length;

                        while (tbl.rows.length > 1) {
                            tbl.deleteRow(1);
                        }

                        nodes.forEach(node => {
                            const row = tbl.insertRow(-1);
                            row.className = 'tr';
                            row.id = 'node-row-' + node.tag;

                            const cellTag = row.insertCell(0);
                            cellTag.className = 'td';
                            cellTag.innerHTML = '<strong>' + node.tag + '</strong> <span style="color:#71717a; font-size: 0.85em;">(' + node.protocol + ')</span>';

                            const cellLat = row.insertCell(1);
                            cellLat.className = 'td';
                            cellLat.id = 'node-lat-' + node.tag;
                            cellLat.textContent = 'Опрос...';

                            const cellStatus = row.insertCell(2);
                            cellStatus.className = 'td';
                            cellStatus.id = 'node-status-' + node.tag;
                            cellStatus.innerHTML = '<span style="color: #fbbf24; font-weight: bold;">● Ожидание</span>';
                        });

                        syncClashDelays();
                    })
                    .catch(e => console.error('Nodes fetch error:', e));

                fetch('http://' + host + ':8088/api/v1/status')
                    .then(r => r.json())
                    .then(data => {
                        const countEl = document.getElementById('total-nodes');
                        if (countEl && data.nodes_count !== undefined) {
                            countEl.textContent = data.nodes_count;
                        }
                    })
                    .catch(() => {});

                fetch('https://api.ipify.org?format=json')
                    .then(r => r.json())
                    .then(data => {
                        const ipEl = document.getElementById('outbound-ip');
                        if (ipEl && data.ip) {
                            ipEl.textContent = data.ip;
                        }
                    })
                    .catch(() => {
                        const ipEl = document.getElementById('outbound-ip');
                        if (ipEl) ipEl.textContent = 'Не определен';
                    });

                const ws = new WebSocket('ws://' + host + ':8088/ws/telemetry');

                ws.onopen = function() {
                    const statusEl = document.getElementById('daemon-status');
                    if (statusEl) {
                        statusEl.textContent = '● Онлайн';
                        statusEl.style.color = '#4ade80';
                    }
                };

                ws.onmessage = function(event) {
                    try {
                        const msg = JSON.parse(event.data);
                        if (!msg.node_latencies) return;
                        for (const [tag, latency] of Object.entries(msg.node_latencies)) {
                            updateNodeUI(tag, latency);
                        }
                    } catch (e) {}
                };

                ws.onerror = function() {
                    const statusEl = document.getElementById('daemon-status');
                    if (statusEl) {
                        statusEl.textContent = '● Ошибка';
                        statusEl.style.color = '#f87171';
                    }
                };

                ws.onclose = function() {
                    const statusEl = document.getElementById('daemon-status');
                    if (statusEl) {
                        statusEl.textContent = '● Офлайн';
                        statusEl.style.color = '#f87171';
                    }
                    setTimeout(initTelemetry, 4000);
                };

                setInterval(syncClashDelays, 3000);
                setTimeout(checkUpdates, 1000);
            }

            setTimeout(initTelemetry, 250);
            return viewContainer;
        };

        // ==========================================
        // 2. СЕКЦИЯ КОНФИГУРАЦИИ (АККОРДЕОН)
        // ==========================================
        const s = m.section(form.NamedSection, 'main', 'cheburnet');
        s.anonymous = true;
        s.addremove = false;

        const origRender = s.render;
        s.render = function() {
            return Promise.resolve(origRender.apply(this, arguments)).then(function(contentNode) {
                return E('details', {
                    'class': 'cbi-section',
                    'style': 'margin-top: 20px; border: 1px solid rgba(255, 255, 255, 0.12); border-radius: 6px; padding: 10px; background: rgba(255, 255, 255, 0.02);'
                }, [
                    E('summary', {
                        'style': 'font-size: 15px; font-weight: bold; cursor: pointer; padding: 8px 10px; user-select: none; color: #38bdf8;'
                    }, _('▶ Параметры маршрутизации и прокси (нажмите, чтобы развернуть)')),
                    contentNode
                ]);
            });
        };

        s.tab('general', _('Прокси и ядро'));
        s.tab('routing_rules', _('Маршрутизация списков'));
        s.tab('dns_settings', _('Настройки DNS и сети'));

        // --- ВКЛАДКА 1: ПРОКСИ И ЯДРО ---
        let o = s.taboption('general', form.ListValue, 'engine', _('Движок ядра'));
        o.value('sing-box', 'Sing-box');
        o.value('xray', 'Xray-core');
        o.default = 'sing-box';

        o = s.taboption('general', form.ListValue, 'routing_mode', _('Режим маршрутизации'));
        o.value('rules', _('По спискам (Избирательный обход)'));
        o.value('global', _('Весь трафик (Полный туннель / Global VPN)'));
        o.default = 'rules';

        o = s.taboption('general', form.Flag, 'auto_update', _('Автоматическое обновление'));
        o.default = '0';

        o = s.taboption('general', form.ListValue, 'config_type', _('Тип конфигурации'));
        o.value('urltest', 'URLTest (Автовыбор по задержке)');
        o.value('manual', 'Manual (Ручной выбор ноды)');
        o.default = 'urltest';

        o = s.taboption('general', form.ListValue, 'source_mode', _('Источник серверов'));
        o.value('subscription', _('По ссылкам подписок (URL)'));
        o.value('manual', _('Прямой ввод ссылок (vless://, hy2:// и др.)'));
        o.default = 'subscription';

        o = s.taboption('general', form.DynamicList, 'manual_nodes', _('Строки прокси для URLTest'));
        o.depends('source_mode', 'manual');
        o.placeholder = 'vless://... или hysteria2://...';

        o = s.taboption('general', form.ListValue, 'urltest_interval', _('Интервал проверки URLTest'));
        o.depends('config_type', 'urltest');
        o.value('1m', _('Каждую минуту'));
        o.value('3m', _('Каждые 3 минуты'));
        o.value('5m', _('Каждые 5 минут'));
        o.default = '3m';

        o = s.taboption('general', form.Value, 'urltest_tolerance', _('URLTest допустимое отклонение'));
        o.depends('config_type', 'urltest');
        o.datatype = 'uinteger';
        o.default = '50';

        o = s.taboption('general', form.Value, 'urltest_url', _('URLTest ссылка для проверки'));
        o.depends('config_type', 'urltest');
        o.value('https://www.gstatic.com/generate_204', 'https://www.gstatic.com/generate_204 (Google)');
        o.value('https://cp.cloudflare.com/generate_204', 'https://cp.cloudflare.com/generate_204 (Cloudflare)');
        o.default = 'https://www.gstatic.com/generate_204';

        o = s.taboption('general', form.Flag, 'auto_hwid', _('Автоматический HWID (MAC-bound)'));
        o.default = '1';

        o = s.taboption('general', form.Value, 'custom_hwid', _('Глобальный кастомный HWID (опционально)'));
        o.depends('auto_hwid', '0');
        o.placeholder = '00000000-0000-0000-0000-000000000000';

        // ==========================================
        // ТАБЛИЦА ПОДПИСОК (USER-AGENT, HWID, URL)
        // ==========================================
        const subSec = m.section(form.GridSection, 'subscription', _('Таблица ссылок подписок'));
        subSec.anonymous = true;
        subSec.addremove = true;
        subSec.sortable = true;

        o = subSec.option(form.Flag, 'enabled', _('Вкл'));
        o.default = '1';
        o.rmempty = false;
        o.editable = true;

        o = subSec.option(form.Value, 'user_agent', _('User-Agent'));
        o.placeholder = 'Выберите из списка или введите свой';
        o.value('Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)', 'Happ/4.1.3 (iOS)');
        o.value('Happ/4.1.3 (Linux; Android 14)', 'Happ/4.1.3 (Android)');
        o.value('Happ/4.1.3', 'Happ/4.1.3 (Краткий)');
        o.value('Happ/3.2.0', 'Happ/3.2.0');
        o.value('FlClash/1.19.18', 'FlClash/1.19.18');
        o.value('clash.meta', 'clash.meta (Mihomo)');
        o.value('sing-box/1.9.0', 'sing-box/1.9.0');
        o.value('Xray-core/1.8.24', 'Xray-core');
        o.value('v2rayN/6.42', 'v2rayN/6.42');
        o.value('v2rayNG/1.8.19', 'v2rayNG/1.8.19');
        o.value('NekoBox/1.2.9', 'NekoBox');
        o.default = 'Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)';
        o.rmempty = false;
        o.editable = true;

        o = subSec.option(form.Value, 'hwid', _('HWID (опционально)'));
        o.placeholder = 'Оставьте пустым, если не нужен';
        o.rmempty = true;
        o.editable = true;

        o = subSec.option(form.Value, 'url', _('URL подписки'));
        o.placeholder = 'https://sub.domain.com/token';
        o.rmempty = false;
        o.editable = true;

        // ==========================================
        // ТАБЛИЦА ПОЛИТИК КЛИЕНТОВ (CLIENT POLICY)
        // ==========================================
        const clientSec = m.section(form.GridSection, 'client_rule', _('Политики для устройств (Client Policy)'),
            _('Индивидуальные правила маршрутизации для устройств локальной сети. Направляют трафик устройства мимо общих списков.'));
        clientSec.anonymous = true;
        clientSec.addremove = true;
        clientSec.sortable = true;

        o = clientSec.option(form.Flag, 'enabled', _('Вкл'));
        o.default = '1';
        o.rmempty = false;
        o.editable = true;

        o = clientSec.option(form.Value, 'name', _('Имя устройства'));
        o.placeholder = 'Smart TV / Рабочий ПК';
        o.editable = true;

        o = clientSec.option(form.Value, 'target', _('IP или MAC адрес'));
        o.placeholder = '192.168.1.150 или 00:11:22:...';
        o.rmempty = false;
        o.editable = true;

        for (let mac in hosts) {
            let host = hosts[mac];
            if (host.ipv4 && host.ipv4.length > 0) {
                let ip = host.ipv4[0];
                let title = (host.name ? host.name + ' (' + ip + ')' : ip) + ' [' + mac + ']';
                o.value(ip, title);
            }
        }

        o = clientSec.option(form.ListValue, 'mode', _('Политика'));
        o.value('rules', _('По спискам (Только заблокированные)'));
        o.value('full_proxy', _('Всё в прокси (Полный туннель)'));
        o.value('direct', _('Прямой (Мимо прокси / Direct)'));
        o.default = 'rules';
        o.editable = true;

        // --- ВКЛАДКА 2: МАРШРУТИЗАЦИЯ СПИСКОВ ---
        o = s.taboption('routing_rules', form.DynamicList, 'rulesets', _('Service list (Предопределенные списки)'));
        o.depends('routing_mode', 'rules');
        o.value('russia_inside', 'russia_inside (Россия / РКН)');
        o.value('youtube', 'youtube (Google Video)');
        o.value('meta', 'meta (Instagram, Facebook)');
        o.value('telegram', 'telegram (Telegram CDN & Calls)');
        o.value('google_ai', 'google_ai (Gemini)');
        o.value('discord', 'discord');
        o.value('twitter', 'twitter');

        o = s.taboption('routing_rules', form.ListValue, 'custom_domain_type', _('Тип пользовательского списка доменов'));
        o.depends('routing_mode', 'rules');
        o.value('text', _('Текстовый список'));
        o.default = 'text';

        o = s.taboption('routing_rules', form.TextValue, 'custom_domains', _('Список пользовательских доменов'));
        o.depends('routing_mode', 'rules');
        o.rows = 10;
        o.wrap = 'off';
        o.placeholder = 'evadex.com\namazonaws.com\nweatherapi.com\nletsencrypt.org\nsms222.us\nmexc.com\ngoogleusercontent.com\n2ip.io';
        o.renderWidget = function() {
            const node = form.TextValue.prototype.renderWidget.apply(this, arguments);
            const textarea = node.querySelector('textarea');
            if (textarea) {
                textarea.style.fontFamily = 'monospace, "Courier New", Courier';
                textarea.style.fontSize = '12px';
                textarea.style.backgroundColor = '#181a1f';
                textarea.style.color = '#abb2bf';
                textarea.style.borderRadius = '4px';
                textarea.style.padding = '8px';
                textarea.style.lineHeight = '1.4';
                textarea.style.border = '1px solid #3c4049';
            }
            return node;
        };

        o = s.taboption('routing_rules', form.ListValue, 'custom_subnet_type', _('Тип пользовательского списка подсетей'));
        o.depends('routing_mode', 'rules');
        o.value('text', _('Текстовый список'));
        o.default = 'text';

        o = s.taboption('routing_rules', form.TextValue, 'custom_subnets', _('Список пользовательских подсетей'));
        o.depends('routing_mode', 'rules');
        o.rows = 6;
        o.wrap = 'off';
        o.placeholder = '185.252.177.39\n104.16.0.0/12\n1.1.1.1/32';
        o.renderWidget = function() {
            const node = form.TextValue.prototype.renderWidget.apply(this, arguments);
            const textarea = node.querySelector('textarea');
            if (textarea) {
                textarea.style.fontFamily = 'monospace, "Courier New", Courier';
                textarea.style.fontSize = '12px';
                textarea.style.backgroundColor = '#181a1f';
                textarea.style.color = '#e5c07b';
                textarea.style.borderRadius = '4px';
                textarea.style.padding = '8px';
                textarea.style.lineHeight = '1.4';
                textarea.style.border = '1px solid #3c4049';
            }
            return node;
        };

        o = s.taboption('routing_rules', form.DynamicList, 'local_list_files', _('Локальные списки доменов'));
        o.depends('routing_mode', 'rules');
        o.placeholder = '/path/file.lst';

        // --- ВКЛАДКА 3: НАСТРОЙКИ DNS И СЕТИ ---
        o = s.taboption('dns_settings', form.ListValue, 'dns_protocol', _('Тип протокола DNS'));
        o.value('udp', _('UDP (Незащищённый DNS)'));
        o.value('doh', _('DNS через HTTPS (DoH)'));
        o.value('dot', _('DNS через TLS (DoT)'));
        o.default = 'doh';

        o = s.taboption('dns_settings', form.Value, 'dns_server', _('DNS-сервер'));
        o.value('1.1.1.1', '1.1.1.1 (Cloudflare)');
        o.value('8.8.8.8', '8.8.8.8 (Google)');
        o.value('77.88.8.8', '77.88.8.8 (Yandex DNS)');
        o.value('9.9.9.9', '9.9.9.9 (Quad9)');
        o.default = '1.1.1.1';

        o = s.taboption('dns_settings', form.ListValue, 'bootstrap_dns', _('Bootstrap DNS-сервер'));
        o.value('77.88.8.8', '77.88.8.8 (Yandex DNS)');
        o.value('77.88.8.1', '77.88.8.1 (Yandex DNS)');
        o.value('1.1.1.1', '1.1.1.1 (Cloudflare DNS)');
        o.value('1.0.0.1', '1.0.0.1 (Cloudflare DNS)');
        o.value('8.8.8.8', '8.8.8.8 (Google DNS)');
        o.value('8.8.4.4', '8.8.4.4 (Google DNS)');
        o.value('9.9.9.9', '9.9.9.9 (Quad9 DNS)');
        o.value('9.9.9.11', '9.9.9.11 (Quad9 DNS)');
        o.default = '77.88.8.1';

        o = s.taboption('dns_settings', form.Value, 'dns_ttl', _('Перезапись TTL для DNS'));
        o.datatype = 'uinteger';
        o.default = '60';

        o = s.taboption('dns_settings', widgets.NetworkSelect, 'source_interface', _('Сетевой интерфейс источника'));
        o.multiple = false;
        o.default = 'br-lan';

        o = s.taboption('dns_settings', form.Flag, 'enable_yacd', _('Включить YACD'));
        o.default = '1';

        return m.render();
    },

    handleSaveApply: function(ev, mode) {
        const self = this;
        ui.showIndicator('saving-cheburnet', _('Сохранение конфигурации...'));

        return this.handleSave(ev).then(function() {
            return uci.save().then(function() {
                return uci.apply().then(function() {
                    ui.hideIndicator('saving-cheburnet');
                    ui.showIndicator('reloading-cheburnet', _('Применение настроек в Chebur.NET...'));

                    return new Promise(function(resolve) {
                        setTimeout(resolve, 800);
                    }).then(function() {
                        const host = window.location.hostname;
                        return fetch('http://' + host + ':8088/api/v1/reload', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' }
                        })
                        .then(function(res) { return res.json(); })
                        .then(function(data) {
                            ui.hideIndicator('reloading-cheburnet');
                            if (data && data.error) {
                                ui.addNotification(null, E('p', {}, _('Ошибка применения: ') + data.error), 'error');
                            } else {
                                ui.addNotification(null, E('p', {}, _('Настройки успешно применены! Нод загружено: ') + (data.nodes || 0)), 'info');
                                window.location.reload();
                            }
                        })
                        .catch(function(err) {
                            ui.hideIndicator('reloading-cheburnet');
                            ui.addNotification(null, E('p', {}, _('Сетевая ошибка при перезагрузке: ') + err), 'warning');
                        });
                    });
                });
            });
        });
    }
});