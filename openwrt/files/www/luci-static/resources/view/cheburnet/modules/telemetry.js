'use strict';
'require baseclass';
'require ui';

return baseclass.extend({
    createTelemetrySection: function(nodesModule) {
        window.cheburProblems = {};
        window.cheburLastDiagSnapshot = null;
        window.cheburActiveNodeTag = '';

        var isSyncingDelays = false;
        var syncIntervalId = null;
        var diagPollIntervalId = null;
        var statusPollIntervalId = null;

        function updateBannerContent() {
            var banner = document.getElementById('diag-banner');
            var content = document.getElementById('diag-banner-content');
            if (!banner || !content) return;

            var snap = window.cheburLastDiagSnapshot;
            if (!snap) {
                banner.style.background = 'rgba(255, 255, 255, 0.05)';
                banner.style.borderColor = 'rgba(255, 255, 255, 0.15)';
                banner.style.color = '#a1a1aa';
                content.textContent = _('● Инициализация системы диагностики...');
                return;
            }

            var pList = Object.values(window.cheburProblems || {});
            if (pList.length === 0) {
                banner.style.background = 'rgba(74, 222, 128, 0.1)';
                banner.style.borderColor = 'rgba(74, 222, 128, 0.25)';
                banner.style.color = '#4ade80';
                content.innerHTML = '● Все системы работают штатно &middot; Проверено показателей: <strong>' + (snap.total_checks || 15) + '</strong>';
                var container = document.getElementById('diag-problems-container');
                if (container) container.innerHTML = '';
                return;
            }

            var hasCrit = pList.some(function(p) { return p.severity === 'critical'; });
            banner.style.background = hasCrit ? 'rgba(239, 68, 68, 0.15)' : 'rgba(234, 179, 8, 0.15)';
            banner.style.borderColor = hasCrit ? 'rgba(239, 68, 68, 0.4)' : 'rgba(234, 179, 8, 0.4)';
            banner.style.color = hasCrit ? '#f87171' : '#fef08a';
            content.innerHTML = '▲ Обнаружены проблемы: <strong>' + pList.length + '</strong>';
        }

        function renderDiagnosticSnapshot(snap) {
            if (!snap) return;
            window.cheburLastDiagSnapshot = snap;
            window.cheburProblems = {};
            if (snap.problems && Array.isArray(snap.problems)) {
                snap.problems.forEach(function(p) { window.cheburProblems[p.id] = p; });
            }
            updateBannerContent();
        }

        function fetchDiagnosticsOnce() {
            var host = window.location.hostname;
            var controller = new AbortController();
            var timeoutId = setTimeout(function() { controller.abort(); }, 3500);

            fetch('http://' + host + ':8088/api/v1/diagnostics', { signal: controller.signal })
                .then(function(r) {
                    clearTimeout(timeoutId);
                    return r.ok ? r.json() : null;
                })
                .then(function(snap) { if (snap) renderDiagnosticSnapshot(snap); })
                .catch(function() { clearTimeout(timeoutId); });
        }

        function syncClashDelays() {
            if (isSyncingDelays) return;
            isSyncingDelays = true;

            fetch('http://' + window.location.hostname + ':9090/proxies')
                .then(function(r) { return r.ok ? r.json() : null; })
                .then(function(data) {
                    if (!data || !data.proxies) return;

                    var proxyGroup = data.proxies['PROXY'] || data.proxies['proxy'];
                    var autoGroup = data.proxies['auto'] || data.proxies['AUTO'];
                    var autoCurrentBest = (autoGroup && autoGroup.now) ? autoGroup.now : '';

                    if (proxyGroup && proxyGroup.now) {
                        if (proxyGroup.now === 'auto' || proxyGroup.now === 'AUTO') {
                            nodesModule.highlightActiveNode('auto', autoCurrentBest);
                        } else {
                            nodesModule.highlightActiveNode(proxyGroup.now, '');
                        }
                    }

                    var autoDelay = 0;
                    if (autoCurrentBest && data.proxies[autoCurrentBest]) {
                        var bestInfo = data.proxies[autoCurrentBest];
                        if (bestInfo.history && bestInfo.history.length > 0) {
                            autoDelay = bestInfo.history[bestInfo.history.length - 1].delay || 0;
                        }
                    }
                    if (autoDelay > 0) {
                        nodesModule.updateNodeUI('auto', autoDelay);
                    }

                    Object.entries(data.proxies).filter(function(pair) {
                        return !['DIRECT', 'REJECT', 'PROXY', 'GLOBAL', 'auto', 'AUTO', 'auto-out'].includes(pair[0]);
                    }).forEach(function(pair) {
                        var tag = pair[0];
                        var info = pair[1];
                        var delay = (info.history && info.history.length > 0) ? (info.history[info.history.length - 1].delay || 0) : 0;
                        nodesModule.updateNodeUI(tag, delay);
                    });
                })
                .catch(function() {})
                .finally(function() { isSyncingDelays = false; });
        }

        function syncRealtimeStatus() {
            fetch('http://' + window.location.hostname + ':8088/api/v1/status')
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    var statusEl = document.getElementById('daemon-status');
                    if (statusEl) {
                        statusEl.textContent = '● Онлайн';
                        statusEl.style.color = '#4ade80';
                    }
                    var countEl = document.getElementById('total-nodes');
                    if (countEl && data.nodes_count !== undefined) countEl.textContent = data.nodes_count;
                    var ipEl = document.getElementById('outbound-ip');
                    if (ipEl && data.outbound_ip) ipEl.textContent = data.outbound_ip;

                    if (data.active_node && !window.cheburActiveNodeTag) {
                        nodesModule.highlightActiveNode(data.active_node, '');
                    }
                })
                .catch(function() {
                    var statusEl = document.getElementById('daemon-status');
                    if (statusEl) {
                        statusEl.textContent = '● Офлайн';
                        statusEl.style.color = '#f87171';
                    }
                });
        }

        function connectWebSocket() {
            var host = window.location.hostname;
            var ws = new WebSocket('ws://' + host + ':8088/ws/telemetry');
            window.cheburWs = ws;

            ws.onmessage = function(event) {
                try {
                    var msg = JSON.parse(event.data);
                    if (msg.active_node && window.cheburActiveNodeTag !== 'auto') {
                        nodesModule.highlightActiveNode(msg.active_node, '');
                    }
                    if (msg.type === 'upgrade_error' && msg.error) {
                        ui.addNotification(null, E('p', {}, _('Ошибка обновления: ') + msg.error), 'error');
                        var b = document.getElementById('update-notification-banner');
                        var txt = document.getElementById('update-banner-text');
                        if (b && txt) {
                            b.style.display = 'flex';
                            b.style.background = 'rgba(239, 68, 68, 0.15)';
                            b.style.borderColor = 'rgba(239, 68, 68, 0.4)';
                            b.style.color = '#f87171';
                            txt.innerHTML = '✖ <strong>Сбой:</strong> ' + msg.error;
                        }
                    }
                } catch (e) {}
            };

            ws.onclose = function() { setTimeout(connectWebSocket, 5000); };
        }

        var diagBanner = E('div', {
            'id': 'diag-banner',
            'style': 'margin-bottom: 15px; padding: 12px 16px; border-radius: 6px; background: rgba(74, 222, 128, 0.1); border: 1px solid rgba(74, 222, 128, 0.25); color: #4ade80; display: flex; align-items: center; justify-content: space-between;'
        }, [
            E('div', { 'id': 'diag-banner-content', 'style': 'font-size: 13px; font-weight: 500;' }, '● Проверка диагностических показателей...'),
            E('button', {
                'class': 'btn cbi-button-neutral',
                'style': 'font-size: 11px; margin: 0; padding: 2px 10px;',
                'click': function(e) {
                    e.preventDefault();
                    fetchDiagnosticsOnce();
                }
            }, _('Опросить'))
        ]);

        var updateBanner = E('div', {
            'id': 'update-notification-banner',
            'style': 'display: none; align-items: center; justify-content: space-between; margin-bottom: 15px; padding: 12px 16px; border-radius: 6px; background: rgba(234, 179, 8, 0.15); border: 1px solid rgba(234, 179, 8, 0.4); color: #fef08a;'
        }, [
            E('span', { 'id': 'update-banner-text', 'style': 'font-size: 13px; font-weight: 500;' }, ''),
            E('button', {
                'class': 'btn cbi-button-action',
                'style': 'font-size: 12px; margin: 0; padding: 4px 12px;',
                'click': function(e) {
                    e.preventDefault();
                    ui.showIndicator('updating-system', _('Проверка свободного места и установка обновлений...'));
                    fetch('http://' + window.location.hostname + ':8088/api/v1/updates/upgrade', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ target: 'all' })
                    })
                    .then(function(r) { return r.json(); })
                    .then(function(data) {
                        ui.hideIndicator('updating-system');
                        if (data && data.error) {
                            ui.addNotification(null, E('p', {}, _('Ошибка установки: ') + data.error), 'error');
                        }
                    })
                    .catch(function(err) {
                        ui.hideIndicator('updating-system');
                        ui.addNotification(null, E('p', {}, _('Сетевая ошибка: ') + err), 'error');
                    });
                }
            }, _('Обновить сейчас'))
        ]);

        var activeServerCard = E('div', {
            'id': 'active-server-card',
            'style': 'margin-bottom: 12px; padding: 12px 16px; border-radius: 6px; background: rgba(56, 189, 248, 0.08); border: 1px solid rgba(56, 189, 248, 0.3); display: flex; justify-content: space-between; align-items: center; gap: 15px;'
        }, [
            E('div', { 'style': 'display: flex; flex-direction: column; gap: 4px;' }, [
                E('div', { 'style': 'display: flex; align-items: center; gap: 8px;' }, [
                    E('span', { 'style': 'font-size: 11px; text-transform: uppercase; font-weight: bold; color: #38bdf8; background: rgba(56, 189, 248, 0.2); padding: 2px 6px; border-radius: 4px;' }, _('АКТИВНЫЙ СЕРВЕР')),
                    E('span', { 'id': 'active-server-name', 'style': 'font-weight: bold; font-size: 14px; color: #ffffff;' }, _('Определение...'))
                ]),
                E('span', { 'id': 'active-server-proto', 'style': 'font-size: 12px; color: #a1a1aa;' }, '')
            ]),
            E('div', { 'style': 'display: flex; align-items: center; gap: 12px;' }, [
                E('span', { 'id': 'active-server-lat', 'style': 'font-size: 13px; font-weight: bold; color: #fbbf24; font-family: monospace;' }, '--- ms'),
                E('span', { 'id': 'active-server-badge', 'style': 'font-size: 12px; font-weight: bold; color: #4ade80;' }, '● Онлайн')
            ])
        ]);

        var table = E('table', { 'class': 'table', 'id': 'chebur-nodes-table' }, [
            E('tr', { 'class': 'tr table-titles' }, [
                E('th', { 'class': 'th' }, _('Сервер / Тег')),
                E('th', { 'class': 'th' }, _('Задержка')),
                E('th', { 'class': 'th' }, _('Статус'))
            ]),
            E('tr', { 'class': 'tr', 'id': 'loading-row' }, [
                E('td', { 'class': 'td', 'colspan': '3' }, _('Загрузка списка серверов...'))
            ])
        ]);

        var nodesDetails = E('details', {
            'class': 'cbi-section',
            'style': 'margin-bottom: 18px; border: 1px solid rgba(255, 255, 255, 0.1); border-radius: 6px; padding: 10px; background: rgba(255, 255, 255, 0.02);'
        }, [
            E('summary', {
                'style': 'font-size: 13px; font-weight: bold; cursor: pointer; padding: 6px 8px; user-select: none; color: #94a3b8; display: flex; justify-content: space-between; align-items: center;'
            }, [
                E('span', {}, _('Все доступные серверы и переключение')),
                E('span', { 'style': 'font-size: 11px; font-weight: normal; color: #64748b;' }, _('(нажмите, чтобы развернуть)'))
            ]),
            E('div', { 'style': 'margin-top: 10px;' }, [table])
        ]);

        var badgeStyle = 'background: rgba(255, 255, 255, 0.05); border: 1px solid rgba(255, 255, 255, 0.15); border-radius: 6px; padding: 8px 16px; min-width: 170px; display: flex; align-items: center; justify-content: space-between; gap: 10px;';

        var viewContainer = E('div', { 'class': 'cbi-section' }, [
            diagBanner,
            updateBanner,
            E('div', { 'style': 'display: flex; gap: 12px; margin-bottom: 15px; flex-wrap: wrap;' }, [
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
            activeServerCard,
            nodesDetails
        ]);

        setTimeout(function() {
            var host = window.location.hostname;
            var tbl = document.getElementById('chebur-nodes-table');

            fetch('http://' + host + ':8088/api/v1/nodes')
                .then(function(r) { return r.json(); })
                .then(function(nodes) {
                    if (!nodes || nodes.length === 0) return;

                    var countEl = document.getElementById('total-nodes');
                    if (countEl) countEl.textContent = nodes.length;

                    while (tbl.rows.length > 1) tbl.deleteRow(1);

                    nodes.forEach(function(node) {
                        var row = tbl.insertRow(-1);
                        row.className = 'tr';
                        row.id = 'node-row-' + node.tag;
                        row.style.cursor = 'pointer';
                        row.onclick = function() {
                            nodesModule.selectProxyNode(node.tag, syncClashDelays);
                        };

                        var cellTag = row.insertCell(0);
                        cellTag.className = 'td';
                        if (node.tag === 'auto') {
                            cellTag.innerHTML = '<strong style="color:#38bdf8;">⚡ Автовыбор сервера</strong> <span class="node-proto-label" style="color:#71717a; font-size: 0.85em;">(urltest)</span>';
                        } else {
                            cellTag.innerHTML = '<strong>' + node.tag + '</strong> <span class="node-proto-label" style="color:#71717a; font-size: 0.85em;">(' + node.protocol + ')</span>';
                        }

                        var cellLat = row.insertCell(1);
                        cellLat.className = 'td';
                        cellLat.id = 'node-lat-' + node.tag;
                        cellLat.textContent = node.latency > 0 ? (node.latency + ' ms') : 'Опрос...';

                        var cellStatus = row.insertCell(2);
                        cellStatus.className = 'td';
                        cellStatus.id = 'node-status-' + node.tag;
                        cellStatus.innerHTML = (node.latency > 0)
                            ? '<span style="color: #4ade80; font-weight: bold;">● Доступен</span>'
                            : '<span style="color: #fbbf24; font-weight: bold;">● Ожидание</span>';
                    });

                    setTimeout(syncClashDelays, 400);
                })
                .catch(function() {});

            syncRealtimeStatus();
            fetchDiagnosticsOnce();
            connectWebSocket();

            syncIntervalId = setInterval(syncClashDelays, 4000);
            statusPollIntervalId = setInterval(syncRealtimeStatus, 5000);
            diagPollIntervalId = setInterval(fetchDiagnosticsOnce, 4000);
        }, 250);

        return viewContainer;
    }
});