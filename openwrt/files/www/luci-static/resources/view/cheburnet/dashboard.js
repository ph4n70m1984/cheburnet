'use strict';
'require view';
'require form';
'require uci';
'require ui';
'require network';
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

        window.cheburCheckUpdates = function(e) {
            e.preventDefault();
            var statusDiv = document.getElementById('ws-update-status');
            if (statusDiv) {
                statusDiv.innerHTML = '<span style="color:#fbbf24;">Выполняется проверка GitHub и пакетов...</span>';
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
                        statusDiv.innerHTML = '<span style="color:#f87171;">Ошибка проверки обновлений: ' + err.message + '</span>';
                    }
                });
        };

        window.cheburPerformUpgrade = function(e) {
            e.preventDefault();
            var statusDiv = document.getElementById('ws-update-status');
            var btnUpgrade = document.getElementById('ws-btn-upgrade');
            if (statusDiv) {
                statusDiv.innerHTML = '<span style="color:#38bdf8;">Процесс обновления запущен в фоне. Демон перезапустится автоматически...</span>';
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
                        statusDiv.innerHTML = '<span style="color:#f87171;">✖ Сбой: ' + data.error + '</span>';
                    }
                    if (btnUpgrade) {
                        btnUpgrade.style.display = 'inline-block';
                    }
                    ui.addNotification(null, E('p', {}, _('Ошибка обновления: ') + data.error), 'error');
                }
            })
            .catch(function(err) {
                if (statusDiv) {
                    statusDiv.innerHTML = '<span style="color:#f87171;">✖ Сетевая ошибка: ' + err + '</span>';
                }
                if (btnUpgrade) {
                    btnUpgrade.style.display = 'inline-block';
                }
            });
        };

        var statusSec = m.section(form.NamedSection, 'telemetry', 'cheburnet', _('Состояние, диагностика и телеметрия'));
        statusSec.anonymous = true;
        statusSec.render = function() {
            return telemetryModule.createTelemetrySection(nodesModule);
        };

        var s = m.section(form.NamedSection, 'main', 'cheburnet');
        s.anonymous = true;
        s.addremove = false;

        var origRender = s.render;
        s.render = function() {
            return Promise.resolve(origRender.apply(this, arguments)).then(function(contentNode) {
                return E('details', {
                    'class': 'cbi-section',
                    'style': 'margin-top: 20px; border: 1px solid rgba(255, 255, 255, 0.12); border-radius: 6px; padding: 10px; background: rgba(255, 255, 255, 0.02);'
                }, [
                    E('summary', {
                        'style': 'font-size: 15px; font-weight: bold; cursor: pointer; padding: 8px 10px; user-select: none; color: #38bdf8;'
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
                        'class': 'cbi-section',
                        'style': 'margin-top: 15px; border: 1px solid rgba(255, 255, 255, 0.1); border-radius: 6px; padding: 10px; background: rgba(255, 255, 255, 0.01);'
                    }, [
                        E('summary', {
                            'style': 'font-size: 14px; font-weight: bold; cursor: pointer; padding: 6px 8px; user-select: none; color: #38bdf8; display: flex; justify-content: space-between;'
                        }, [
                            E('span', {}, titleText),
                            E('span', { 'style': 'font-size: 11px; font-weight: normal; color: #64748b;' }, _('(нажмите, чтобы развернуть/свернуть)'))
                        ]),
                        node
                    ]);
                    if (isOpen) detailsNode.open = true;
                    return detailsNode;
                });
            };
        }

        // --- Подписки ---
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

        o = subSec.option(form.Value, 'user_agent', _('User-Agent'));
        o.default = 'Happ/4.1.3 (iPhone; iOS 17.5.1; Scale/3.00)';
        o.modalonly = true;

        o = subSec.option(form.Value, 'hwid', _('HWID (опционально)'));
        o.modalonly = true;

        // --- Клиенты ---
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

        // --- Маршруты сервисов ---
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

        // --- ВКЛАДКА 2: МАРШРУТИЗАЦИЯ СПИСКОВ ---
        o = s.taboption('routing_rules', form.ListValue, 'ruleset_update_interval', _('Интервал обновления'));
        o.value('24h', '24 часа');
        o.value('72h', '72 часа');
        o.default = '72h';

        o = s.taboption('routing_rules', form.DynamicList, 'rulesets', _('Списки по умолчанию'));
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
            '<div style="margin-bottom:15px; padding:15px; border:1px solid rgba(255,255,255,0.15); border-radius:6px; background:rgba(0,0,0,0.25);">' +
                '<div id="ws-update-status" style="margin-bottom:15px; font-family:monospace; color:#8c8c8c; font-size:13px;">' +
                    'Ожидание ручной проверки релизов...' +
                '</div>' +
                '<div style="display:flex; gap:10px; flex-wrap:wrap;">' +
                    '<button class="btn cbi-button-apply" onclick="window.cheburCheckUpdates(event)">Проверить наличие обновлений</button>' +
                    '<button class="btn cbi-button-action" id="ws-btn-upgrade" style="display:none;" onclick="window.cheburPerformUpgrade(event)">Установить все обновления</button>' +
                '</div>' +
            '</div>';

        o = s.taboption('updates', form.Flag, 'auto_update', _('Автоматическое обновление'));
        o.description = _('Фоновая периодическая проверка доступных релизов на GitHub и в opkg.');
        o.default = '0';

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