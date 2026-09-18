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
        ]).then(function(results) {
            var host = window.location.hostname;
            return fetch('http://' + host + ':8088/api/v1/status')
                .then(function(r) { return r.ok ? r.json() : {}; })
                .catch(function() { return {}; })
                .then(function(statusData) {
                    return [results[0], results[1], statusData];
                });
        });
    },

    render: function(data) {
        var hosts = (data && data[0]) ? data[0] : {};
        var daemonStatus = (data && data[2]) ? data[2] : {};
        var hasPublicSub = !!(daemonStatus.features && daemonStatus.features.public_sub);

        var m = new form.Map('cheburnet', _('Chebur.NET'),
            _('Управление прозрачным проксированием трафика на базе Sing-box'));

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
                    flex-direction: column;
                    align-items: stretch;
                    gap: 12px;
                    margin: 15px 0;
                    padding: 14px 16px;
                    border: 1px solid var(--cb-border);
                    border-radius: 8px;
                    background: var(--cb-bg-card);
                    box-shadow: 0 1px 3px rgba(0, 0, 0, 0.05);
                }
                .cb-control-title {
                    font-weight: bold;
                    width: 100%;
                    color: var(--cb-text-main);
                    display: flex;
                    align-items: center;
                    gap: 8px;
                    font-size: 14px;
                }
                .cb-control-buttons {
                    display: flex;
                    align-items: center;
                    flex-wrap: wrap;
                    gap: 8px;
                    width: 100%;
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

                .cb-accordion {
                    border: 1px solid var(--cb-border) !important;
                    border-radius: 8px !important;
                    background: var(--cb-bg-card) !important;
                    padding: 8px 12px !important;
                    margin-top: 15px !important;
                    box-shadow: 0 1px 3px rgba(0, 0, 0, 0.05) !important;
                }
                .cb-accordion summary {
                    color: var(--cb-text-accent) !important;
                    cursor: pointer !important;
                    user-select: none !important;
                    font-size: 14px !important;
                    font-weight: bold !important;
                    padding: 6px 4px !important;
                    outline: none !important;
                }
                .cb-accordion .cbi-tabmenu {
                    border-bottom: 1px solid var(--cb-border) !important;
                    margin-bottom: 15px !important;
                }
                .cb-accordion .cbi-tabmenu > li > a {
                    color: var(--cb-text-muted) !important;
                }
                .cb-accordion .cbi-tabmenu > li.cbi-tab > a {
                    color: var(--cb-text-accent) !important;
                    font-weight: bold !important;
                    border-bottom: 2px solid var(--cb-text-accent) !important;
                }
                .cb-accordion input[type="text"],
                .cb-accordion input[type="password"],
                .cb-accordion select,
                .cb-accordion textarea,
                .cb-accordion .cbi-dynlist {
                    background-color: var(--cb-bg-surface) !important;
                    border: 1px solid var(--cb-border) !important;
                    color: var(--cb-text-main) !important;
                    border-radius: 4px !important;
                }
                .cb-accordion .cbi-dynlist > .item {
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

                .cb-link-box {
                    font-family: monospace;
                    padding: 8px 12px;
                    border: 1px solid var(--cb-border);
                    border-radius: 6px;
                    background: var(--cb-bg-surface);
                    color: var(--cb-text-main);
                    word-break: break-all;
                    user-select: all;
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
                .then(function() {
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
            if (e) e.preventDefault();
            var statusDiv = document.getElementById('ws-update-status');
            if (statusDiv) {
                statusDiv.innerHTML = '<span style="color:var(--cb-warn-text);">' + _('Выполняется проверка GitHub и пакетов...') + '</span>';
            }

            var host = window.location.hostname;
            var apiToken = uci.get('cheburnet', 'main', 'api_token') || '';
            var headers = {};
            if (apiToken) {
                headers['X-API-Token'] = apiToken;
            }

            fetch('http://' + host + ':8088/api/v1/updates/check', { headers: headers })
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
            if (e) e.preventDefault();
            var statusDiv = document.getElementById('ws-update-status');
            var btnUpgrade = document.getElementById('ws-btn-upgrade');
            if (statusDiv) {
                statusDiv.innerHTML = '<span style="color:var(--cb-text-accent);">' + _('Процесс обновления запущен в фоне. Демон перезапустится автоматически...') + '</span>';
            }
            if (btnUpgrade) {
                btnUpgrade.style.display = 'none';
            }

            var apiToken = uci.get('cheburnet', 'main', 'api_token') || '';
            var headers = { 'Content-Type': 'application/json' };
            if (apiToken) {
                headers['X-API-Token'] = apiToken;
            }

            fetch('http://' + window.location.hostname + ':8088/api/v1/updates/upgrade', {
                method: 'POST',
                headers: headers,
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

        window.cheburGenSubToken = function() {
            var chars = '0123456789abcdef';
            var token = '';
            for (var i = 0; i < 32; i++) {
                token += chars.charAt(Math.floor(Math.random() * chars.length));
            }
            var input = document.getElementById('cb-sub-token-input');
            if (input) {
                input.value = token;
                input.dispatchEvent(new Event('input', { bubbles: true }));
                input.dispatchEvent(new Event('change', { bubbles: true }));
                input.dispatchEvent(new Event('blur', { bubbles: true }));
            }
            window.cheburUpdateSubLinkPreview();
        };

        window.cheburGenClashSecret = function() {
            var chars = '0123456789abcdef';
            var secret = '';
            for (var i = 0; i < 32; i++) {
                secret += chars.charAt(Math.floor(Math.random() * chars.length));
            }
            var input = document.getElementById('cb-clash-secret-input');
            if (input) {
                input.value = secret;
                input.type = 'text';
                var toggleBtn = document.getElementById('cb-clash-secret-toggle');
                if (toggleBtn) toggleBtn.textContent = '🙈';

                input.dispatchEvent(new Event('input', { bubbles: true }));
                input.dispatchEvent(new Event('change', { bubbles: true }));
                input.dispatchEvent(new Event('blur', { bubbles: true }));
            }
        };

        window.cheburToggleClashSecret = function() {
            var input = document.getElementById('cb-clash-secret-input');
            var toggleBtn = document.getElementById('cb-clash-secret-toggle');
            if (!input) return;

            if (input.type === 'password') {
                input.type = 'text';
                if (toggleBtn) toggleBtn.textContent = '🙈';
            } else {
                input.type = 'password';
                if (toggleBtn) toggleBtn.textContent = '👁';
            }
        };

        window.cheburCopyClashSecret = function() {
            var input = document.getElementById('cb-clash-secret-input');
            if (input && input.value) {
                navigator.clipboard.writeText(input.value.trim()).then(function() {
                    ui.addNotification(null, E('p', {}, _('Секрет Clash API скопирован в буфер обмена!')), 'info');
                });
            } else {
                ui.addNotification(null, E('p', {}, _('Секрет пуст')), 'warning');
            }
        };

        window.cheburCopySubLink = function() {
            var box = document.getElementById('cb-sub-link-text');
            if (box && box.innerText && !box.innerText.startsWith('(')) {
                navigator.clipboard.writeText(box.innerText).then(function() {
                    ui.addNotification(null, E('p', {}, _('Ссылка подписки скопирована в буфер обмена!')), 'info');
                });
            }
        };

        window.cheburUpdateSubLinkPreview = function() {
            var linkBox = document.getElementById('cb-sub-link-text');
            if (!linkBox) return;

            var tokenInput = document.getElementById('cb-sub-token-input');
            var portInput = document.getElementById('cb-sub-port-input');

            var token = (tokenInput && tokenInput.value) ? tokenInput.value.trim() : '';
            var port = (portInput && portInput.value) ? portInput.value.trim() : '';

            if (!token) {
                token = uci.get('cheburnet', 'main', 'public_sub_token') || '';
                if (!port) port = uci.get('cheburnet', 'main', 'public_sub_port') || '9443';
            }

            if (!port) port = '9443';

            var host = window.location.hostname;
            if (token) {
                linkBox.innerText = 'http://' + host + ':' + port + '/sub/' + token;
            } else {
                linkBox.innerText = _('(Сгенерируйте токен для формирования ссылки)');
            }
        };

        // --- 1. СЕКЦИЯ СТАТУСА И ТЕЛЕМЕТРИИ ---
        var statusSec = m.section(form.NamedSection, 'telemetry', 'cheburnet', _('Состояние, диагностика и телеметрия'));
        statusSec.anonymous = true;
        statusSec.render = function() {
            var controlPanel = E('div', { 'class': 'cb-control-panel' }, [
                E('div', { 'class': 'cb-control-title' }, [
                    E('span', { 'style': 'font-size: 16px;' }, '⚙'),
                    E('span', {}, _('Управление демоном Chebur.NET:'))
                ]),
                E('div', { 'class': 'cb-control-buttons' }, [
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
                ])
            ]);

            var telemetryNode = telemetryModule.createTelemetrySection(nodesModule);
            return E('div', {}, [ controlPanel, telemetryNode ]);
        };

        // --- 2. СЕКЦИЯ ОСНОВНЫХ НАСТРОЕК (MAIN) ---
        var s = m.section(form.NamedSection, 'main', 'cheburnet', _('Параметры маршрутизации, сети и ядра'));
        s.anonymous = true;
        s.addremove = false;

        s.tab('general', _('Прокси и ядро'));
        s.tab('routing_rules', _('Маршрутизация списков'));
        s.tab('dns_settings', _('Настройки DNS и сети'));

        if (hasPublicSub) {
            s.tab('public_sub', _('Публичная подписка (Happ)'));
        }

        s.tab('updates', _('Менеджер обновлений'));

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

        o = s.taboption('general', form.Value, 'clash_api_secret', _('Секрет для Clash API (sing-box)'));
        o.description = _('Опциональный Bearer-токен для защиты контроллера 127.0.0.1:9090. Оставьте пустым для доступа без пароля.');
        o.password = true;
        o.placeholder = 'Оставьте пустым или сгенерируйте токен';
        var origClashSecretRender = o.render;
        o.render = function() {
            return Promise.resolve(origClashSecretRender.apply(this, arguments)).then(function(node) {
                var input = node.querySelector('input');
                if (input && input.parentNode) {
                    input.id = 'cb-clash-secret-input';

                    var toggleBtn = E('button', {
                        'id': 'cb-clash-secret-toggle',
                        'class': 'btn cbi-button-neutral',
                        'type': 'button',
                        'title': _('Показать / Скрыть пароль'),
                        'style': 'margin-left: 6px; padding: 4px 10px; font-size: 13px;',
                        'click': window.cheburToggleClashSecret
                    }, '👁');

                    var copyBtn = E('button', {
                        'class': 'btn cbi-button-neutral',
                        'type': 'button',
                        'title': _('Копировать секрет'),
                        'style': 'margin-left: 6px; padding: 4px 10px; font-size: 13px;',
                        'click': window.cheburCopyClashSecret
                    }, '📋');

                    var genBtn = E('button', {
                        'class': 'btn cbi-button-action',
                        'type': 'button',
                        'style': 'margin-left: 6px; white-space: nowrap;',
                        'click': window.cheburGenClashSecret
                    }, [ E('span', {}, '🔑 '), _('Сгенерировать') ]);

                    input.parentNode.appendChild(toggleBtn);
                    input.parentNode.appendChild(copyBtn);
                    input.parentNode.appendChild(genBtn);
                }
                return node;
            });
        };

        o = s.taboption('routing_rules', form.ListValue, 'ruleset_update_interval', _('Интервал обновления списков'));
        o.value('24h', _('24 часа (каждый день)'));
        o.value('72h', _('72 часа (раз в 3 дня)'));
        o.value('168h', _('1 неделя'));
        o.default = '72h';

        o = s.taboption('routing_rules', form.DynamicList, 'custom_srs_rulesets', _('Пользовательские SRS списки (Sing-box)'));
        o.description = _('Формат ввода: <code>Имя|URL|[direct|proxy]</code> (например: <code>antizapret|https://example.com/rule.srs|direct</code>).');
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

        if (hasPublicSub) {
            o = s.taboption('public_sub', form.Flag, 'public_sub_enabled', _('Включить публичный сервер подписки'));
            o.description = _('Запускает изолированный HTTP-сервер на выделенном порту для мобильных клиентов (Happ, v2rayNG, Clash).');
            o.default = '0';

            o = s.taboption('public_sub', form.Value, 'public_sub_port', _('Порт сервера подписки'));
            o.depends('public_sub_enabled', '1');
            o.datatype = 'port';
            o.default = '9443';
            var origPortRender = o.render;
            o.render = function() {
                return Promise.resolve(origPortRender.apply(this, arguments)).then(function(node) {
                    var input = node.querySelector('input');
                    if (input) {
                        input.id = 'cb-sub-port-input';
                        input.addEventListener('input', window.cheburUpdateSubLinkPreview);
                        input.addEventListener('change', window.cheburUpdateSubLinkPreview);
                    }
                    return node;
                });
            };

            o = s.taboption('public_sub', form.Value, 'public_sub_token', _('Секретный токен подписки'));
            o.depends('public_sub_enabled', '1');
            o.description = _('Токен защищает подписку от несанкционированного доступа. Доступен только при передаче токена в пути URL.');
            o.placeholder = '32-значный hex-токен';

            var origTokenRender = o.render;
            o.render = function() {
                return Promise.resolve(origTokenRender.apply(this, arguments)).then(function(node) {
                    var input = node.querySelector('input');
                    if (input && input.parentNode) {
                        input.id = 'cb-sub-token-input';
                        input.addEventListener('input', window.cheburUpdateSubLinkPreview);
                        input.addEventListener('change', window.cheburUpdateSubLinkPreview);

                        var genBtn = E('button', {
                            'class': 'btn cbi-button-action',
                            'type': 'button',
                            'style': 'margin-left: 8px; white-space: nowrap;',
                            'click': window.cheburGenSubToken
                        }, [ E('span', {}, '🔑 '), _('Сгенерировать токен') ]);

                        input.parentNode.appendChild(genBtn);
                    }
                    return node;
                });
            };

            var o_sub_link = s.taboption('public_sub', form.DummyValue, '_sub_link_display', _('Ссылка для клиента Happ'));
            o_sub_link.depends('public_sub_enabled', '1');
            o_sub_link.rawhtml = true;
            o_sub_link.default = '' +
                '<div style="padding: 12px; border: 1px solid var(--cb-border); border-radius: 6px; background: var(--cb-bg-card); margin-bottom: 12px;">' +
                    '<div style="font-size: 12px; color: var(--cb-text-muted); margin-bottom: 6px;">' +
                        _('Скопируйте эту ссылку и вставьте в приложение Happ (или любой совместимый клиент) в качестве подписки:') +
                    '</div>' +
                    '<div id="cb-sub-link-text" class="cb-link-box" style="margin-bottom: 8px;">' +
                        _('(Загрузка ссылки...)') +
                    '</div>' +
                    '<button type="button" class="btn cbi-button-apply" onclick="window.cheburCopySubLink()">' +
                        '📋 ' + _('Копировать ссылку') +
                    '</button>' +
                '</div>';

            var origSubLinkRender = o_sub_link.render;
            o_sub_link.render = function() {
                return Promise.resolve(origSubLinkRender.apply(this, arguments)).then(function(node) {
                    setTimeout(window.cheburUpdateSubLinkPreview, 100);
                    return node;
                });
            };
        }

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

        o = s.taboption('updates', form.ListValue, 'update_channel', _('Канал обновлений'));
        o.value('release', _('Стабильный (Release)'));
        o.value('beta', _('Бета-версии (Beta / Pre-release)'));
        o.default = 'release';
        o.description = _('На стабильном канале приходят только проверенные версии. В бета-канале доступны самые свежие функции.');

        o = s.taboption('updates', form.Flag, 'auto_update', _('Автоматическое обновление'));
        o.description = _('Фоновая периодическая проверка доступных релизов на GitHub и в opkg/apk.');
        o.default = '0';

        // --- 3. СЕКЦИЯ ПОДПИСОК ---
        var subSec = m.section(form.GridSection, 'subscription', _('Таблица ссылок подписок'));
        subSec.anonymous = true;
        subSec.addremove = true;
        subSec.sortable = true;

        o = subSec.option(form.Flag, 'enabled', _('Вкл'));
        o.default = '1';
        o.editable = true;

        o = subSec.option(form.Value, 'name', _('Наименование провайдера'));
        o.editable = true;

        o = subSec.option(form.ListValue, 'update_interval', _('Интервал автообновления'));
        o.value('1h', _('Каждый час (1 ч)'));
        o.value('3h', _('Каждые 3 часа (3 ч)'));
        o.value('6h', _('Каждые 6 часов (6 ч)'));
        o.value('12h', _('Каждые 12 часов (12 ч)'));
        o.value('24h', _('Раз в сутки (24 ч)'));
        o.default = '24h';
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

        // --- 4. СЕКЦИЯ ПОЛИТИК УСТРОЙСТВ ---
        var clientSec = m.section(form.GridSection, 'client_rule', _('Политики для устройств (Client Policy)'));
        clientSec.anonymous = true;
        clientSec.addremove = true;
        clientSec.sortable = true;

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

        // --- 5. СЕКЦИЯ МАРШРУТИЗАЦИИ СЕРВИСОВ ---
        var routeSec = m.section(form.GridSection, 'route_policy', _('Секции маршрутизации сервисов (Route Policies)'));
        routeSec.anonymous = true;
        routeSec.addremove = true;
        routeSec.sortable = true;

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
        var apiToken = uci.get('cheburnet', 'main', 'api_token') || '';
        var nodeReqHeaders = {};
        if (apiToken) {
            nodeReqHeaders['X-API-Token'] = apiToken;
        }

        fetch('http://' + window.location.hostname + ':8088/api/v1/nodes', {
            headers: nodeReqHeaders
        })
            .then(function(r) { return r.ok ? r.json() : []; })
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

        // Оборачивание секций 2, 3, 4 и 5 в аккордеоны без разрушения CBA-привязок
        return m.render().then(function(mapNode) {
            var sections = mapNode.querySelectorAll('.cbi-section');
            sections.forEach(function(secNode) {
                var titleEl = secNode.querySelector('h2, h3, .cbi-section-title');
                if (!titleEl) return;

                var titleText = titleEl.textContent.trim();
                if (!titleText || titleText.indexOf('Состояние, диагностика') !== -1) return;

                var isMain = (titleText.indexOf('Параметры маршрутизации') !== -1);

                var details = E('details', {
                    'class': 'cb-accordion'
                }, [
                    E('summary', {}, [
                        E('span', {}, '▶ ' + titleText),
                        E('span', { 'style': 'font-size: 11px; font-weight: normal; color: var(--cb-text-muted); float: right;' }, _('(нажмите, чтобы развернуть/свернуть)'))
                    ])
                ]);

                if (!isMain) {
                    details.open = false;
                } else {
                    details.open = false;
                }

                titleEl.style.display = 'none';

                var bodyWrapper = E('div', { 'style': 'margin-top: 10px;' });
                while (secNode.firstChild) {
                    bodyWrapper.appendChild(secNode.firstChild);
                }

                details.appendChild(bodyWrapper);
                secNode.appendChild(details);
            });

            return mapNode;
        });
    },

    handleSaveApply: function(ev, mode) {
        ui.showIndicator('saving-cheburnet', _('Сохранение конфигурации...'));
        return this.handleSave(ev).then(function() {
            return uci.save().then(function() {
                return uci.apply().then(function() {
                    var token = uci.get('cheburnet', 'main', 'api_token') || '';
                    var headers = {};
                    if (token) {
                        headers['X-API-Token'] = token;
                    }
                    return fetch('http://' + window.location.hostname + ':8088/api/v1/reload', {
                        method: 'POST',
                        headers: headers
                    }).then(function() {
                        ui.hideIndicator('saving-cheburnet');
                        window.location.reload();
                    }).catch(function() {
                        ui.hideIndicator('saving-cheburnet');
                        window.location.reload();
                    });
                });
            });
        }).catch(function(err) {
            ui.hideIndicator('saving-cheburnet');
            ui.addNotification(null, E('p', {}, _('Ошибка сохранения: ') + err), 'error');
        });
    }
});