'use strict';
'require view';
'require form';
'require uci';
'require ui';
'require network';
'require fs';
'require tools.widgets as widgets';

'require view.cheburnet.modules.constants as constants';
'require view.cheburnet.modules.nodes as nodesModule';
'require view.cheburnet.modules.telemetry as telemetryModule';

return view.extend({
    load: function() {
        return Promise.all([
            network.getHostHints(),
            uci.load('cheburnet')
        ]);
    },

    render: function(data) {
        var hosts = (data && data[0]) ? data[0] : {};
        var m = new form.Map('cheburnet', _('Chebur.NET'),
            _('Управление прозрачным проксированием трафика на базе Sing-box'));

        // Внедрение универсальной дизайн-системы и стилей кнопок управления
        var styleId = 'cheburnet-theme-vars';
        if (!document.getElementById(styleId)) {
            var css = document.createElement('style');
            css.id = styleId;
            css.textContent = `
                :root {
                    --cb-bg-card: #ffffff;
                    --cb-bg-surface: #f8fafc;
                    --cb-border: #cbd5e1;
                    --cb-text-main: #0f172a;
                    --cb-text-muted: #475569;
                    --cb-text-accent: #5c5c5c;

                    --cb-badge-bg: #f1f5f9;
                    --cb-badge-border: #cbd5e1;
                    --cb-badge-text: #334155;

                    --cb-ok-bg: #f0fdf4;
                    --cb-ok-border: #86efac;
                    --cb-ok-text: #166534;

                    --cb-warn-bg: #fffbeb;
                    --cb-warn-border: #fcd34d;
                    --cb-warn-text: #92400e;

                    --cb-err-bg: #fef2f2;
                    --cb-err-border: #fca5a5;
                    --cb-err-text: #991b1b;

                    --cb-card-active-bg: #f0f9ff;
                    --cb-card-active-border: #7dd3fc;
                    --cb-table-hover: rgba(0, 0, 0, 0.04);
                }

                html[data-darkmode="true"],
                html[data-theme="dark"],
                html[data-bs-theme="dark"],
                body.dark,
                body.dark-mode,
                body[class*="dark"] {
                    --cb-bg-card: #222222 !important;
                    --cb-bg-surface: #323232 !important;
                    --cb-border: rgba(255, 255, 255, 0.12) !important;
                    --cb-text-main: #f8fafc !important;
                    --cb-text-muted: #94a3b8 !important;
                    --cb-text-accent: #a6aaaf !important;

                    --cb-badge-bg: rgba(255, 255, 255, 0.06) !important;
                    --cb-badge-border: rgba(255, 255, 255, 0.15) !important;
                    --cb-badge-text: #cbd5e1 !important;

                    --cb-ok-bg: rgba(74, 222, 128, 0.12) !important;
                    --cb-ok-border: rgba(74, 222, 128, 0.35) !important;
                    --cb-ok-text: #4ade80 !important;

                    --cb-warn-bg: rgba(234, 179, 8, 0.15) !important;
                    --cb-warn-border: rgba(234, 179, 8, 0.45) !important;
                    --cb-warn-text: #fde047 !important;

                    --cb-err-bg: rgba(239, 68, 68, 0.15) !important;
                    --cb-err-border: rgba(239, 68, 68, 0.45) !important;
                    --cb-err-text: #f87171 !important;

                    --cb-card-active-bg: rgba(56, 189, 248, 0.12) !important;
                    --cb-card-active-border: rgba(56, 189, 248, 0.4) !important;
                    --cb-table-hover: rgba(255, 255, 255, 0.05) !important;
                }

                .cb-control-panel {
                    display: flex;
                    align-items: center;
                    gap: 10px;
                    margin: 15px 0;
                    padding: 12px 16px;
                    border: 1px solid var(--cb-border);
                    border-radius: 8px;
                    background: var(--cb-bg-card);
                    box-shadow: 0 1px 3px rgba(0, 0, 0, 0.05);
                    flex-wrap: wrap;
                }
                .cb-control-title {
                    font-weight: bold;
                    margin-right: auto;
                    color: var(--cb-text-main);
                    display: flex;
                    align-items: center;
                    gap: 8px;
                }
                .cb-btn {
                    display: inline-flex;
                    align-items: center;
                    gap: 6px;
                    padding: 6px 14px;
                    border-radius: 6px;
                    font-size: 13px;
                    font-weight: 600;
                    cursor: pointer;
                    transition: all 0.2s ease;
                    border: 1px solid transparent;
                    text-decoration: none;
                }
                .cb-btn-start {
                    background: var(--cb-ok-bg);
                    color: var(--cb-ok-text);
                    border-color: var(--cb-ok-border);
                }
                .cb-btn-start:hover {
                    background: #dcfce7;
                    transform: translateY(-1px);
                }
                .cb-btn-stop {
                    background: var(--cb-err-bg);
                    color: var(--cb-err-text);
                    border-color: var(--cb-err-border);
                }
                .cb-btn-stop:hover {
                    background: #fee2e2;
                    transform: translateY(-1px);
                }
                .cb-btn-restart {
                    background: var(--cb-badge-bg);
                    color: var(--cb-text-accent);
                    border-color: var(--cb-badge-border);
                }
                .cb-btn-restart:hover {
                    background: var(--cb-card-active-bg);
                    border-color: var(--cb-card-active-border);
                    transform: translateY(-1px);
                }

                .cb-details {
                    border: 1px solid var(--cb-border) !important;
                    border-radius: 8px !important;
                    background: var(--cb-bg-card) !important;
                    padding: 12px !important;
                    margin-top: 15px !important;
                    box-shadow: 0 1px 3px rgba(0, 0, 0, 0.05) !important;
                }
                .cb-details summary {
                    color: var(--cb-text-accent) !important;
                    cursor: pointer;
                    user-select: none;
                }
                .cb-details .cbi-tabmenu {
                    border-bottom: 1px solid var(--cb-border) !important;
                    margin-bottom: 15px !important;
                }
                .cb-details .cbi-tabmenu > li > a {
                    color: var(--cb-text-muted) !important;
                }
                .cb-details .cbi-tabmenu > li.cbi-tab > a {
                    color: var(--cb-text-accent) !important;
                    font-weight: bold !important;
                    border-bottom: 2px solid var(--cb-text-accent) !important;
                }
                .cb-details input[type="text"],
                .cb-details input[type="password"],
                .cb-details select,
                .cb-details textarea,
                .cb-details .cbi-dynlist {
                    background-color: var(--cb-bg-surface) !important;
                    border: 1px solid var(--cb-border) !important;
                    color: var(--cb-text-main) !important;
                    border-radius: 4px !important;
                }
                .cb-details .cbi-dynlist > .item {
                    background: var(--cb-badge-bg) !important;
                    border: 1px solid var(--cb-badge-border) !important;
                    color: var(--cb-text-main) !important;
                }
                #chebur-nodes-table {
                    background: var(--cb-bg-card) !important;
                    color: var(--cb-text-main) !important;
                    border-collapse: collapse !important;
                    width: 100% !important;
                }
                #chebur-nodes-table tr.tr {
                    background: transparent !important;
                    transition: background 0.15s ease;
                }
                #chebur-nodes-table tr.tr:hover {
                    background-color: var(--cb-table-hover) !important;
                }
                #chebur-nodes-table th, 
                #chebur-nodes-table .table-titles {
                    background: var(--cb-bg-surface) !important;
                    color: var(--cb-text-muted) !important;
                    border-bottom: 1px solid var(--cb-border) !important;
                }
                #chebur-nodes-table td {
                    color: var(--cb-text-main) !important;
                    border-bottom: 1px solid var(--cb-border) !important;
                }
            `;
            document.head.appendChild(css);
        }

        window.cheburManageService = function(action) {
            var labels = {
                'start': _('Запуск службы...'),
                'stop': _('Остановка службы...'),
                'restart': _('Перезапуск службы...')
            };

            ui.showIndicator('chebur-service-mgr', labels[action] || _('Выполнение...'));

            fs.exec('/etc/init.d/cheburnet', [action])
                .then(function(res) {
                    setTimeout(function() {
                        ui.hideIndicator('chebur-service-mgr');
                        window.location.reload();
                    }, 1200);
                })
                .catch(function(err) {
                    ui.hideIndicator('chebur-service-mgr');
                    ui.addNotification(null, E('p', {}, _('Ошибка управления службой: ') + err.message), 'error');
                });
        };

        window.cheburCheckUpdates = function(e) {
            e.preventDefault();
            var statusDiv = document.getElementById('ws-update-status');
            if (statusDiv) {
                statusDiv.innerHTML = '<span style="color:var(--cb-warn-text);">' + _('Выполняется проверка GitHub и пакетов...') + '</span>';
            }

            var host = window.location.hostname;
            fetch('http://' + host + ':8088/api/v1/updates/check')
                .then(function(r) {
                    if (!r.ok) throw new Error('HTTP ' + r.status);
                    return r.json();
                })
                .then(function(data) {
                    if (telemetryModule && typeof telemetryModule.renderUpdateReport === 'function') {
                        telemetryModule.renderUpdateReport(data);
                    }
                    if (telemetryModule && typeof telemetryModule.showUpdateNotification === 'function') {
                        telemetryModule.showUpdateNotification(data);
                    }
                })
                .catch(function(err) {
                    if (statusDiv) {
                        statusDiv.innerHTML = '<span style="color:var(--cb-err-text);">' + _('Ошибка проверки обновлений: ') + err.message + '</span>';
                    }
                });
        };

        window.cheburPerformUpgrade = function(e) {
            e.preventDefault();
            var statusDiv = document.getElementById('ws-update-status');
            var btnUpgrade = document.getElementById('ws-btn-upgrade');
            if (statusDiv) {
                statusDiv.innerHTML = '<span style="color:var(--cb-text-accent);">' + _('Процесс обновления запущен в фоне. Демон перезапустится автоматически...') + '</span>';
            }
            if (btnUpgrade) {
                btnUpgrade.style.display = 'none';
            }

            fetch('http://' + window.location.hostname + ':8088/api/v1/updates/upgrade', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ target: 'all' })
            })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data && data.error) {
                    if (statusDiv) {
                        statusDiv.innerHTML = '<span style="color:var(--cb-err-text);">✖ ' + _('Сбой: ') + data.error + '</span>';
                    }
                    if (btnUpgrade) {
                        btnUpgrade.style.display = 'inline-block';
                    }
                    ui.addNotification(null, E('p', {}, _('Ошибка обновления: ') + data.error), 'error');
                }
            })
            .catch(function(err) {
                if (statusDiv) {
                    statusDiv.innerHTML = '<span style="color:var(--cb-err-text);">✖ ' + _('Сетевая ошибка: ') + err + '</span>';
                }
                if (btnUpgrade) {
                    btnUpgrade.style.display = 'inline-block';
                }
            });
        };

        // --- СЕКЦИЯ СОСТОЯНИЯ И КНОПКИ УПРАВЛЕНИЯ ДЕМОНОМ ---
        var statusSec = m.section(form.NamedSection, 'telemetry', 'cheburnet', _('Состояние, диагностика и телеметрия'));
        statusSec.anonymous = true;
        statusSec.render = function() {
            var controlPanel = E('div', { 'class': 'cb-control-panel' }, [
                E('div', { 'class': 'cb-control-title' }, [
                    E('span', { 'style': 'font-size: 16px;' }, '⚙'),
                    E('span', {}, _('Управление демоном Chebur.NET:'))
                ]),
                E('button', {
                    'class': 'cb-btn cb-btn-start',
                    'type': 'button',
                    'click': function() { window.cheburManageService('start'); }
                }, [ E('span', {}, '▶'), _('Запустить') ]),
                E('button', {
                    'class': 'cb-btn cb-btn-stop',
                    'type': 'button',
                    'click': function() { window.cheburManageService('stop'); }
                }, [ E('span', {}, '■'), _('Остановить') ]),
                E('button', {
                    'class': 'cb-btn cb-btn-restart',
                    'type': 'button',
                    'click': function() { window.cheburManageService('restart'); }
                }, [ E('span', {}, '⟳'), _('Перезапустить') ])
            ]);

            var telemetryNode = telemetryModule.createTelemetrySection(nodesModule);
            return E('div', {}, [ controlPanel, telemetryNode ]);
        };

        var s = m.section(form.NamedSection, 'main', 'cheburnet');
        s.anonymous = true;
        s.addremove = false;

        var origRender = s.render;
        s.render = function() {
            return Promise.resolve(origRender.apply(this, arguments)).then(function(contentNode) {
                return E('details', {
                    'class': 'cbi-section cb-details',
                    'style': 'margin-top: 20px;'
                }, [
                    E('summary', {
                        'style': 'font-size: 15px; font-weight: bold; padding: 8px 10px;'
                    }, _('▶ Параметры маршрутизации, сети и обновлений (нажмите, чтобы развернуть)')),
                    contentNode
                ]);
            });
        };

        s.tab('general', _('Прокси и ядро'));
        s.tab('routing_rules', _('Маршрутизация списков'));
        s.tab('dns_settings', _('Настройки DNS и сети'));
        s.tab('updates', _('Менеджер обновлений'));

        // --- ВКЛАДКА 1: ПРОКСИ И ЯДРО ---
        var o = s.taboption('general', form.ListValue, 'routing_mode', _('Режим маршрутизации'));
        o.value('rules', _('По спискам (Избирательный обход)'));
        o.value('global', _('Весь трафик (Полный туннель / Global VPN)'));
        o.default = 'rules';

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
        o.value('https://www.gstatic.com/generate_204', 'Google 204');
        o.value('https://cp.cloudflare.com/generate_204', 'Cloudflare 204');
        o.default = 'https://www.gstatic.com/generate_204';

        o = s.taboption('general', form.Flag, 'auto_hwid', _('Автоматический HWID (MAC-bound)'));
        o.default = '1';

        o = s.taboption('general', form.Value, 'custom_hwid', _('Глобальный кастомный HWID (опционально)'));
        o.depends('auto_hwid', '0');

        function wrapGridSection(sectionObj, titleText, isOpen) {
            var orig = sectionObj.render;
            sectionObj.render = function() {
                var self = this, args = arguments;
                return Promise.resolve(orig.apply(self, args)).then(function(node) {
                    var detailsNode = E('details', {
                        'class': 'cbi-section cb-details'
                    }, [
                        E('summary', {
                            'style': 'font-size: 14px; font-weight: bold; padding: 6px 8px; display: flex; justify-content: space-between; align-items: center;'
                        }, [
                            E('span', {}, titleText),
                            E('span', { 'style': 'font-size: 11px; font-weight: normal; color: var(--cb-text-muted);' }, _('(нажмите, чтобы развернуть/свернуть)'))
                        ]),
                        node
                    ]);
                    if (isOpen) detailsNode.open = true;
                    return detailsNode;
                });
            };
        }

        // --- ВКЛАДКА 2: МАРШРУТИЗАЦИЯ СПИСКОВ ---
        o = s.taboption('routing_rules', form.ListValue, 'ruleset_update_interval', _('Интервал обновления списков'));
        o.value('24h', _('24 часа (каждый день)'));
        o.value('72h', _('72 часа (раз в 3 дня)'));
        o.value('168h', _('1 неделя'));
        o.default = '72h';

        o = s.taboption('routing_rules', form.DynamicList, 'custom_srs_rulesets', _('Пользовательские SRS списки (Sing-box)'));
        o.description = _('Формат ввода: <code>Имя|URL|[direct|proxy]</code> (например: <code>antizapret|https://example.com/rule.srs|direct</code>). Загружаются демоном с валидацией и резервированием.');
        o.placeholder = 'my_list|https://example.com/rule.srs|direct';

        o = s.taboption('routing_rules', form.DynamicList, 'rulesets', _('Списки сервисов по умолчанию'));
        constants.categories.forEach(function(cat) {
            o.value(cat.tag, cat.tag + ' — ' + cat.title);
        });

        o = s.taboption('routing_rules', form.TextValue, 'custom_domains', _('Список доменов'));
        o.rows = 6;
        o = s.taboption('routing_rules', form.TextValue, 'custom_subnets', _('Список подсетей'));
        o.rows = 4;
        o = s.taboption('routing_rules', form.TextValue, 'custom_ports', _('Список портов'));
        o.rows = 4;

        // --- ВКЛАДКА 3: DNS И СЕТЬ ---
        o = s.taboption('dns_settings', form.ListValue, 'dns_protocol', _('Протокол DNS'));
        o.value('doh', 'DoH');
        o.value('dot', 'DoT');
        o.value('udp', 'UDP');
        o.default = 'doh';

        o = s.taboption('dns_settings', form.Value, 'dns_server', _('DNS-сервер'));
        o.default = '1.1.1.1';
        o = s.taboption('dns_settings', form.ListValue, 'bootstrap_dns', _('Bootstrap DNS'));
        o.value('77.88.8.8', 'Yandex');
        o.value('1.1.1.1', 'Cloudflare');
        o.default = '77.88.8.8';

        o = s.taboption('dns_settings', widgets.NetworkSelect, 'source_interface', _('Интерфейс'));
        o.default = 'br-lan';

        // --- ВКЛАДКА 4: ОБНОВЛЕНИЯ ---
        var o_upd = s.taboption('updates', form.DummyValue, '_update_panel', _('Управление версиями'));
        o_upd.rawhtml = true;
        o_upd.default = '' +
            '<div style="margin-bottom:15px; padding:15px; border:1px solid var(--cb-border); border-radius:6px; background:var(--cb-bg-card);">' +
                '<div id="ws-update-status" style="margin-bottom:15px; font-family:monospace; color:var(--cb-text-muted); font-size:13px;">' +
                    _('Ожидание ручной проверки релизов...') +
                '</div>' +
                '<div style="display:flex; gap:10px; flex-wrap:wrap;">' +
                    '<button class="btn cbi-button-apply" onclick="window.cheburCheckUpdates(event)">' + _('Проверить наличие обновлений') + '</button>' +
                    '<button class="btn cbi-button-action" id="ws-btn-upgrade" style="display:none;" onclick="window.cheburPerformUpgrade(event)">' + _('Установить все обновления') + '</button>' +
                '</div>' +
            '</div>';

        o = s.taboption('updates', form.Flag, 'auto_update', _('Автоматическое обновление'));
        o.description = _('Фоновая периодическая проверка доступных релизов на GitHub и в opkg.');
        o.default = '0';

        // --- ВНЕШНИЕ АККОРДЕОНЫ ---
        var subSec = m.section(form.GridSection, 'subscription', _('Таблица ссылок подписок'));
        subSec.anonymous = true;
        subSec.addremove = true;
        subSec.sortable = true;
        wrapGridSection(subSec, _('▶ Таблица ссылок подписок'), false);

        o = subSec.option(form.Flag, 'enabled', _('Вкл'));
        o.default = '1';
        o.editable = true;

        o = subSec.option(form.Value, 'name', _('Наименование провайдера'));
        o.editable = true;

        o = subSec.option(form.Value, 'url', _('URL подписки'));
        o.modalonly = true;

        o = subSec.option(form.ListValue, 'filter_mode', _('Режим фильтрации серверов'));
        o.value('exclude', _('Исключить (Blacklist)'));
        o.value('include', _('Оставить только совпадающие (Whitelist)'));
        o.default = 'exclude';
        o.modalonly = true;

        o = subSec.option(form.DynamicList, 'exclude_regex', _('Регулярные выражения (RegExp)'));
        o.modalonly = true;

        // Выпадающий список User-Agent
        o = subSec.option(form.ListValue, 'user_agent', _('User-Agent'));
        o.value('Happ/4.2.0 (iPhone; iOS 18.0; Scale/3.00)', 'Happ 4.2.0 (iOS)');
        o.value('Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)', 'Happ 4.1.3 (iOS)');
        o.value('v2rayNG/1.8.12', 'v2rayNG (Android)');
        o.value('ClashforWindows/0.20.39', 'Clash / Mihomo');
        o.value('sing-box', 'Sing-Box');
        o.value('Shadowrocket/2.2.35', 'Shadowrocket');
        o.value('curl/8.19.0', 'curl / Generic');
        o.default = 'Happ/4.2.0 (iPhone; iOS 18.0; Scale/3.00)';
        o.modalonly = true;

        o = subSec.option(form.Value, 'hwid', _('HWID (опционально)'));
        o.modalonly = true;

        var clientSec = m.section(form.GridSection, 'client_rule', _('Политики для устройств'));
        clientSec.anonymous = true;
        clientSec.addremove = true;
        clientSec.sortable = true;
        wrapGridSection(clientSec, _('▶ Политики для устройств (Client Policy)'), false);

        o = clientSec.option(form.Flag, 'enabled', _('Вкл'));
        o.default = '1';
        o.editable = true;

        o = clientSec.option(form.Value, 'name', _('Имя устройства'));
        o.editable = true;

        o = clientSec.option(form.Value, 'target', _('IP или MAC'));
        o.editable = true;
        for (var mac in hosts) {
            if (hosts[mac].ipv4 && hosts[mac].ipv4.length > 0) {
                var hostIP = hosts[mac].ipv4[0];
                var hostLabel = (hosts[mac].name ? hosts[mac].name + ' (' + hostIP + ')' : hostIP) + ' [' + mac + ']';
                o.value(hostIP, hostLabel);
            }
        }

        o = clientSec.option(form.ListValue, 'mode', _('Политика'));
        o.value('rules', _('По спискам'));
        o.value('full_proxy', _('Всё в прокси'));
        o.value('direct', _('Direct'));
        o.default = 'rules';
        o.editable = true;

        var routeSec = m.section(form.GridSection, 'route_policy', _('Секции маршрутизации'));
        routeSec.anonymous = true;
        routeSec.addremove = true;
        routeSec.sortable = true;
        wrapGridSection(routeSec, _('▶ Секции маршрутизации сервисов (Route Policies)'), false);

        o = routeSec.option(form.Flag, 'enabled', _('Вкл'));
        o.default = '1';
        o.editable = true;

        o = routeSec.option(form.Value, 'name', _('Имя секции'));
        o.editable = true;

        o = routeSec.option(form.DynamicList, 'rulesets', _('Списки'));
        constants.categories.forEach(function(cat) {
            o.value(cat.tag, cat.tag + ' — ' + cat.title);
        });
        o.editable = true;

        o = routeSec.option(form.ListValue, 'outbound', _('Сервер выхода'));
        o.value('PROXY', _('PROXY / AUTO'));
        o.value('direct-out', _('Direct'));
        o.editable = true;

        var outboundSelect = o;
        fetch('http://' + window.location.hostname + ':8088/api/v1/nodes')
            .then(function(r) { return r.json(); })
            .then(function(nodes) {
                if (Array.isArray(nodes)) {
                    nodes.forEach(function(n) {
                        outboundSelect.value(n.tag, n.tag + ' (' + n.protocol + ')');
                    });
                }
            })
            .catch(function() {});

        o = routeSec.option(form.TextValue, 'custom_domains', _('Дополнительные домены'));
        o.rows = 4;
        o.modalonly = true;

        o = routeSec.option(form.TextValue, 'custom_subnets', _('Дополнительные подсети'));
        o.rows = 4;
        o.modalonly = true;

        return m.render();
    },

    handleSaveApply: function(ev, mode) {
        ui.showIndicator('saving-cheburnet', _('Сохранение конфигурации...'));
        return this.handleSave(ev).then(function() {
            return uci.save().then(function() {
                return uci.apply().then(function() {
                    ui.hideIndicator('saving-cheburnet');
                    return fetch('http://' + window.location.hostname + ':8088/api/v1/reload', {
                        method: 'POST'
                    }).then(function() {
                        window.location.reload();
                    });
                });
            });
        });
    }
});