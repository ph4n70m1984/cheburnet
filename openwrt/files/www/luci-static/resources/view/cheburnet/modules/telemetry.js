'use strict';
'require baseclass';
'require ui';
'require uci';

return baseclass.extend({
    getChannelBadge: function(channel) {
        var ch = (channel || 'release').toLowerCase();
        var isBeta = (ch === 'beta' || ch === 'prerelease');
        var label = isBeta ? _('Бета (Beta)') : _('Релиз (Release)');
        var style = isBeta
            ? 'background:var(--cb-warn-bg); color:var(--cb-warn-text); border:1px solid var(--cb-warn-border);'
            : 'background:var(--cb-ok-bg); color:var(--cb-ok-text); border:1px solid var(--cb-ok-border);';

        return '<span style="display:inline-block; padding:1px 7px; border-radius:4px; font-weight:600; font-size:11px; ' + style + '">' + label + '</span>';
    },

    formatComponentStatus: function(name, comp) {
        if (!comp || !comp.installed) {
            return '<li>' + name + ': <span style="color:var(--cb-text-muted);">' + _('Не установлен') + '</span></li>';
        }
        var currentVer = comp.current ? ('v' + comp.current.replace(/^v/, '')) : _('неизвестно');
        var latestVer = comp.latest ? ('v' + comp.latest.replace(/^v/, '')) : currentVer;

        if (comp.has_update) {
            return '<li>' + name + ': <b>' + currentVer + '</b> → <span style="color:var(--cb-ok-text); font-weight:bold;">' + latestVer + ' (' + _('Доступно обновление') + ')</span></li>';
        }
        return '<li>' + name + ': <b>' + currentVer + '</b> → <span style="color:var(--cb-text-muted);">' + _('Актуально') + '</span></li>';
    },

    renderUpdateReport: function(r) {
        var statusDiv = document.getElementById('ws-update-status');
        var btnUpgrade = document.getElementById('ws-btn-upgrade');

        if (r && r.update_channel) {
            var chBadge = document.getElementById('update-channel-badge');
            if (chBadge) {
                chBadge.innerHTML = this.getChannelBadge(r.update_channel);
            }
        }

        if (!statusDiv) return;

        if (!r || (!r.cheburnet && !r.sing_box)) {
            statusDiv.innerHTML = '<span style="color:var(--cb-err-text);">' + _('Не удалось получить данные о версиях релизов.') + '</span>';
            return;
        }

        var html = '<ul style="margin:0; padding-left:20px; line-height: 1.8; color:var(--cb-text-main);">';
        html += this.formatComponentStatus('Chebur.NET', r.cheburnet);
        html += this.formatComponentStatus('Sing-box', r.sing_box);
        html += '</ul>';

        var hasAppUpdate = r.cheburnet && r.cheburnet.has_update;
        var hasSbUpdate = r.sing_box && r.sing_box.installed && r.sing_box.has_update;

        if (!hasAppUpdate && !hasSbUpdate) {
            html += '<div style="margin-top:8px; color:var(--cb-ok-text); font-size:12px;">✔ ' + _('Все компоненты обновлены до актуальных версий.') + '</div>';
        }
        statusDiv.innerHTML = html;

        if (btnUpgrade) {
            btnUpgrade.style.display = (hasAppUpdate || hasSbUpdate) ? 'inline-block' : 'none';
        }
    },

    showUpdateNotification: function(data) {
        if (!data) return;
        var alerts = [];
        if (data.cheburnet && data.cheburnet.has_update) {
            alerts.push('Chebur.NET: ' + data.cheburnet.current + ' → ' + data.cheburnet.latest);
        }
        if (data.sing_box && data.sing_box.installed && data.sing_box.has_update) {
            alerts.push('Sing-box: ' + data.sing_box.current + ' → ' + data.sing_box.latest);
        }
        if (alerts.length > 0) {
            var banner = document.getElementById('update-notification-banner');
            var txt = document.getElementById('update-banner-text');
            if (banner && txt) {
                txt.textContent = _('Доступны обновления компонентов: ') + alerts.join(' | ');
                banner.style.display = 'flex';
                banner.style.background = 'var(--cb-warn-bg)';
                banner.style.border = '1px solid var(--cb-warn-border)';
                banner.style.color = 'var(--cb-warn-text)';
            }
        }
    },

    createTelemetrySection: function(nodesModule) {
        var self = this;
        window.cheburProblems = {};
        window.cheburLastDiagSnapshot = null;
        window.cheburActiveNodeTag = '';

        var isSyncingDelays = false;
        var syncIntervalId = null;
        var diagPollIntervalId = null;
        var statusPollIntervalId = null;

        function getAuthHeaders(customHeaders) {
            var headers = customHeaders || {};
            var apiToken = uci.get('cheburnet', 'main', 'api_token') || '';
            if (apiToken) {
                headers['X-API-Token'] = apiToken;
            }
            return headers;
        }

        function updateBannerContent() {
            var banner = document.getElementById('diag-banner');
            var content = document.getElementById('diag-banner-content');
            if (!banner || !content) return;

            var snap = window.cheburLastDiagSnapshot;
            if (!snap) {
                banner.style.background = 'var(--cb-badge-bg)';
                banner.style.border = '1px solid var(--cb-badge-border)';
                banner.style.color = 'var(--cb-text-muted)';
                content.textContent = _('● Инициализация системы диагностики...');
                return;
            }

            var pList = Object.values(window.cheburProblems || {});
            if (pList.length === 0) {
                banner.style.background = 'var(--cb-ok-bg)';
                banner.style.border = '1px solid var(--cb-ok-border)';
                banner.style.color = 'var(--cb-ok-text)';
                content.innerHTML = '● ' + _('Все системы работают штатно &middot; Проверено показателей: ') + '<strong>' + (snap.total_checks || 15) + '</strong>';
                var container = document.getElementById('diag-problems-container');
                if (container) container.innerHTML = '';
                return;
            }

            var hasCrit = pList.some(function(p) { return p.severity === 'critical'; });
            banner.style.background = hasCrit ? 'var(--cb-err-bg)' : 'var(--cb-warn-bg)';
            banner.style.border = hasCrit ? '1px solid var(--cb-err-border)' : '1px solid var(--cb-warn-border)';
            banner.style.color = hasCrit ? 'var(--cb-err-text)' : 'var(--cb-warn-text)';
            content.innerHTML = '▲ ' + _('Обнаружены проблемы: ') + '<strong>' + pList.length + '</strong>';
        }

        function executeProblemAction(action, btnEl) {
            if (!action) return;
            btnEl.disabled = true;
            btnEl.textContent = _('Выполняется...');

            var controller = new AbortController();
            var timeoutId = setTimeout(function() { controller.abort(); }, 6000);

            fetch('http://' + window.location.hostname + ':8088/api/v1/actions/' + action, {
                method: 'POST',
                headers: getAuthHeaders(),
                signal: controller.signal
            })
            .then(function(r) {
                clearTimeout(timeoutId);
                if (!r.ok) throw new Error('HTTP ' + r.status);
                return r.json();
            })
            .then(function() {
                btnEl.textContent = _('Запрос отправлен');
                setTimeout(fetchDiagnosticsOnce, 1200);
            })
            .catch(function(err) {
                clearTimeout(timeoutId);
                btnEl.disabled = false;
                btnEl.textContent = _('Ошибка');
                ui.addNotification(null, E('p', {}, _('Ошибка вызова действия: ') + err), 'error');
            });
        }

        function renderProblemsCards() {
            var container = document.getElementById('diag-problems-container');
            if (!container) return;
            container.innerHTML = '';

            var pList = Object.values(window.cheburProblems || {});
            if (pList.length === 0) return;

            pList.forEach(function(prob) {
                var isCrit = (prob.severity === 'critical');
                var cardBg = isCrit ? 'var(--cb-err-bg)' : 'var(--cb-warn-bg)';
                var cardBorder = isCrit ? 'var(--cb-err-border)' : 'var(--cb-warn-border)';
                var msgColor = isCrit ? 'var(--cb-err-text)' : 'var(--cb-warn-text)';

                var actionBtn = null;
                if (prob.recoverable && prob.action) {
                    actionBtn = E('button', {
                        'class': 'btn cbi-button-action',
                        'style': 'margin: 0; font-size: 11px; padding: 4px 12px; white-space: nowrap; font-weight: bold;',
                        'click': function(e) {
                            e.preventDefault();
                            executeProblemAction(prob.action, this);
                        }
                    }, _('Исправить'));
                }

                var leftChildren = [
                    E('div', { 'style': 'display: flex; align-items: center; gap: 8px; flex-wrap: wrap;' }, [
                        E('span', { 'style': 'font-weight: bold; font-size: 13px; color: ' + msgColor + ';' }, prob.message || _('Ошибка системы')),
                        E('span', { 'style': 'font-size: 10px; padding: 1px 6px; border-radius: 4px; background: var(--cb-badge-bg); border: 1px solid var(--cb-border); color: var(--cb-text-muted); font-weight: 600;' }, prob.component || _('система'))
                    ])
                ];

                var cardChildren = [
                    E('div', { 'style': 'display: flex; flex-direction: column; gap: 4px;' }, leftChildren)
                ];
                if (actionBtn) {
                    cardChildren.push(E('div', {}, [actionBtn]));
                }

                var card = E('div', {
                    'id': 'problem-card-' + prob.id,
                    'style': 'padding: 10px 14px; border-radius: 6px; background: ' + cardBg + '; border: 1px solid ' + cardBorder + '; display: flex; justify-content: space-between; align-items: center; gap: 15px; margin-bottom: 8px;'
                }, cardChildren);

                container.appendChild(card);
            });
        }

        function fetchDiagnosticsOnce() {
            var host = window.location.hostname;
            var controller = new AbortController();
            var timeoutId = setTimeout(function() { controller.abort(); }, 3500);

            // /diagnostics является открытым read-only эндпоинтом
            fetch('http://' + host + ':8088/api/v1/diagnostics', { signal: controller.signal })
                .then(function(r) {
                    clearTimeout(timeoutId);
                    return r.ok ? r.json() : null;
                })
                .then(function(snap) { if (snap) renderDiagnosticSnapshot(snap); })
                .catch(function() { clearTimeout(timeoutId); });
        }

        function checkUpdatesOnce() {
            var host = window.location.hostname;
            fetch('http://' + host + ':8088/api/v1/updates/check', {
                headers: getAuthHeaders()
            })
            .then(function(r) { return r.ok ? r.json() : null; })
            .then(function(data) {
                if (data) {
                    self.showUpdateNotification(data);
                    self.renderUpdateReport(data);
                }
            })
            .catch(function() {});
        }

        function syncClashDelays() {
            if (isSyncingDelays) return;
            isSyncingDelays = true;

            var headers = {};
            var secretInput = document.getElementById('cb-clash-secret-input');
            var secret = (secretInput && secretInput.value) ? secretInput.value.trim() : (uci.get('cheburnet', 'main', 'clash_api_secret') || '');

            if (secret) {
                headers['Authorization'] = 'Bearer ' + secret;
            }

            fetch('http://' + window.location.hostname + ':9090/proxies', { headers: headers })
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
            // /status является открытым read-only эндпоинтом
            fetch('http://' + window.location.hostname + ':8088/api/v1/status')
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    var statusEl = document.getElementById('daemon-status');
                    if (statusEl) {
                        statusEl.textContent = '● ' + _('Онлайн');
                        statusEl.style.color = 'var(--cb-ok-text)';
                    }
                    var chBadge = document.getElementById('update-channel-badge');
                    if (chBadge && data.update_channel) {
                        chBadge.innerHTML = self.getChannelBadge(data.update_channel);
                    }
                    var countEl = document.getElementById('total-nodes');
                    if (countEl && data.total_nodes !== undefined) countEl.textContent = data.total_nodes;
                    else if (countEl && data.nodes_count !== undefined) countEl.textContent = data.nodes_count;
                    var ipEl = document.getElementById('outbound-ip');
                    if (ipEl && data.outbound_ip) ipEl.textContent = data.outbound_ip;

                    if (data.active_node && !window.cheburActiveNodeTag) {
                        nodesModule.highlightActiveNode(data.active_node, '');
                    }
                })
                .catch(function() {
                    var statusEl = document.getElementById('daemon-status');
                    if (statusEl) {
                        statusEl.textContent = '● ' + _('Офлайн');
                        statusEl.style.color = 'var(--cb-err-text)';
                    }
                });
        }

        function connectWebSocket() {
            var host = window.location.hostname;
            var apiToken = uci.get('cheburnet', 'main', 'api_token') || '';
            var wsUrl = 'ws://' + host + ':8088/ws/telemetry' + (apiToken ? ('?token=' + encodeURIComponent(apiToken)) : '');

            var ws = new WebSocket(wsUrl);
            window.cheburWs = ws;

            ws.onmessage = function(event) {
                try {
                    var msg = JSON.parse(event.data);
                    if (msg.active_node && window.cheburActiveNodeTag !== 'auto') {
                        nodesModule.highlightActiveNode(msg.active_node, '');
                    }
                    if (msg.type === 'update_report' && msg.data) {
                        self.showUpdateNotification(msg.data);
                        self.renderUpdateReport(msg.data);
                    }
                    if (msg.type === 'diagnostic.snapshot' && msg.snapshot) {
                        renderDiagnosticSnapshot(msg.snapshot);
                    } else if ((msg.type === 'diagnostic.problem_created' || msg.type === 'diagnostic.problem_updated') && msg.problem) {
                        window.cheburProblems[msg.problem.id] = msg.problem;
                        renderProblemsCards();
                        updateBannerContent();
                    } else if (msg.type === 'diagnostic.problem_resolved' && msg.problem_id) {
                        delete window.cheburProblems[msg.problem_id];
                        renderProblemsCards();
                        updateBannerContent();
                    }
                    if (msg.type === 'upgrade_error' && msg.error) {
                        ui.addNotification(null, E('p', {}, _('Ошибка обновления: ') + msg.error), 'error');
                        var b = document.getElementById('update-notification-banner');
                        var txt = document.getElementById('update-banner-text');
                        if (b && txt) {
                            b.style.display = 'flex';
                            b.style.background = 'var(--cb-err-bg)';
                            b.style.border = '1px solid var(--cb-err-border)';
                            b.style.color = 'var(--cb-err-text)';
                            txt.innerHTML = '✖ <strong>' + _('Сбой:') + '</strong> ' + msg.error;
                        }
                    }
                } catch (e) {}
            };

            ws.onclose = function() { setTimeout(connectWebSocket, 5000); };
        }

        var diagBanner = E('div', {
            'id': 'diag-banner',
            'style': 'margin-bottom: 15px; padding: 12px 16px; border-radius: 6px; background: var(--cb-ok-bg); border: 1px solid var(--cb-ok-border); color: var(--cb-ok-text); display: flex; align-items: center; justify-content: space-between;'
        }, [
            E('div', { 'id': 'diag-banner-content', 'style': 'font-size: 13px; font-weight: 500;' }, '● ' + _('Проверка диагностических показателей...')),
            E('button', {
                'class': 'btn cbi-button-neutral',
                'style': 'font-size: 11px; margin: 0; padding: 2px 10px;',
                'click': function(e) {
                    e.preventDefault();
                    fetchDiagnosticsOnce();
                }
            }, _('Опросить'))
        ]);

        var problemsContainer = E('div', {
            'id': 'diag-problems-container',
            'style': 'margin-bottom: 15px; display: flex; flex-direction: column; gap: 8px;'
        });

        var updateBanner = E('div', {
            'id': 'update-notification-banner',
            'style': 'display: none; align-items: center; justify-content: space-between; margin-bottom: 15px; padding: 12px 16px; border-radius: 6px; background: var(--cb-warn-bg); border: 1px solid var(--cb-warn-border); color: var(--cb-warn-text);'
        }, [
            E('span', { 'id': 'update-banner-text', 'style': 'font-size: 13px; font-weight: 600;' }, ''),
            E('button', {
                'class': 'btn cbi-button-action',
                'style': 'font-size: 12px; margin: 0; padding: 4px 12px;',
                'click': function(e) {
                    e.preventDefault();
                    ui.showIndicator('updating-system', _('Проверка свободного места и установка обновлений...'));
                    fetch('http://' + window.location.hostname + ':8088/api/v1/updates/upgrade', {
                        method: 'POST',
                        headers: getAuthHeaders({ 'Content-Type': 'application/json' }),
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
            'style': 'margin-bottom: 12px; padding: 12px 16px; border-radius: 6px; background: var(--cb-card-active-bg); border: 1px solid var(--cb-card-active-border); display: flex; justify-content: space-between; align-items: center; gap: 15px;'
        }, [
            E('div', { 'style': 'display: flex; flex-direction: column; gap: 4px;' }, [
                E('div', { 'style': 'display: flex; align-items: center; gap: 8px;' }, [
                    E('span', { 'style': 'font-size: 11px; text-transform: uppercase; font-weight: bold; color: var(--cb-text-accent); background: var(--cb-badge-bg); border: 1px solid var(--cb-border); padding: 2px 6px; border-radius: 4px;' }, _('АКТИВНЫЙ СЕРВЕР')),
                    E('span', { 'id': 'active-server-name', 'style': 'font-weight: bold; font-size: 14px; color: var(--cb-text-main);' }, _('Определение...'))
                ]),
                E('span', { 'id': 'active-server-proto', 'style': 'font-size: 12px; color: var(--cb-text-muted); font-weight: 500;' }, '')
            ]),
            E('div', { 'style': 'display: flex; align-items: center; gap: 12px;' }, [
                E('span', { 'id': 'active-server-lat', 'style': 'font-size: 13px; font-weight: bold; color: var(--cb-warn-text); font-family: monospace;' }, '--- ms'),
                E('span', { 'id': 'active-server-badge', 'style': 'font-size: 12px; font-weight: bold; color: var(--cb-ok-text);' }, '● ' + _('Онлайн'))
            ])
        ]);

        var table = E('table', { 'class': 'table', 'id': 'chebur-nodes-table', 'style': 'width: 100%; border-collapse: collapse;' }, [
            E('tr', { 'class': 'tr table-titles', 'style': 'background: var(--cb-bg-surface);' }, [
                E('th', { 'class': 'th', 'style': 'padding: 8px; color: var(--cb-text-muted); border-bottom: 1px solid var(--cb-border);' }, _('Сервер / Тег')),
                E('th', { 'class': 'th', 'style': 'padding: 8px; color: var(--cb-text-muted); border-bottom: 1px solid var(--cb-border);' }, _('Задержка')),
                E('th', { 'class': 'th', 'style': 'padding: 8px; color: var(--cb-text-muted); border-bottom: 1px solid var(--cb-border);' }, _('Статус'))
            ]),
            E('tr', { 'class': 'tr', 'id': 'loading-row' }, [
                E('td', { 'class': 'td', 'colspan': '3', 'style': 'padding: 12px; color: var(--cb-text-muted);' }, _('Загрузка списка серверов...'))
            ])
        ]);

        var nodesDetails = E('details', {
            'class': 'cbi-section cb-details',
            'style': 'margin-bottom: 18px;'
        }, [
            E('summary', {
                'style': 'font-size: 13px; font-weight: bold; padding: 6px 8px; display: flex; justify-content: space-between; align-items: center;'
            }, [
                E('span', {}, _('Все доступные серверы и переключение')),
                E('span', { 'style': 'font-size: 11px; font-weight: normal; color: var(--cb-text-muted);' }, _('(нажмите, чтобы развернуть)'))
            ]),
            E('div', { 'style': 'margin-top: 10px; overflow-x: auto;' }, [table])
        ]);

        var badgeStyle = 'background: var(--cb-badge-bg); border: 1px solid var(--cb-badge-border); border-radius: 6px; padding: 8px 16px; min-width: 170px; display: flex; align-items: center; justify-content: space-between; gap: 10px; flex: 1;';

        var viewContainer = E('div', { 'class': 'cbi-section' }, [
            diagBanner,
            problemsContainer,
            updateBanner,
            E('div', { 'style': 'display: flex; gap: 12px; margin-bottom: 15px; flex-wrap: wrap;' }, [
                E('div', { 'style': badgeStyle }, [
                    E('span', { 'style': 'color: var(--cb-text-muted); font-size: 13px;' }, _('Демон:')),
                    E('span', { 'id': 'daemon-status', 'style': 'color: var(--cb-text-muted); font-weight: bold; font-size: 13px;' }, '● ' + _('Проверка...'))
                ]),
                E('div', { 'style': badgeStyle }, [
                    E('span', { 'style': 'color: var(--cb-text-muted); font-size: 13px;' }, _('Канал:')),
                    E('span', { 'id': 'update-channel-badge' }, this.getChannelBadge('release'))
                ]),
                E('div', { 'style': badgeStyle }, [
                    E('span', { 'style': 'color: var(--cb-text-muted); font-size: 13px;' }, _('IP:')),
                    E('span', { 'id': 'outbound-ip', 'style': 'color: var(--cb-text-accent); font-weight: bold; font-family: monospace; font-size: 13px;' }, _('Определение...'))
                ]),
                E('div', { 'style': badgeStyle }, [
                    E('span', { 'style': 'color: var(--cb-text-muted); font-size: 13px;' }, _('Серверов:')),
                    E('span', { 'id': 'total-nodes', 'style': 'color: var(--cb-text-main); font-weight: bold; font-size: 14px;' }, '0')
                ])
            ]),
            activeServerCard,
            nodesDetails
        ]);

        setTimeout(function() {
            var host = window.location.hostname;
            var tbl = document.getElementById('chebur-nodes-table');

            fetch('http://' + host + ':8088/api/v1/nodes', {
                headers: getAuthHeaders()
            })
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
                    row.style.borderBottom = '1px solid var(--cb-border)';
                    row.onclick = function() {
                        nodesModule.selectProxyNode(node.tag, syncClashDelays);
                    };

                    var cellTag = row.insertCell(0);
                    cellTag.className = 'td';
                    cellTag.style.padding = '8px';
                    if (node.tag === 'auto') {
                        cellTag.innerHTML = '<strong style="color:var(--cb-text-accent);">⚡ ' + _('Автовыбор сервера') + '</strong> <span class="node-proto-label" style="color:var(--cb-text-muted); font-size: 0.85em;">(urltest)</span>';
                    } else {
                        cellTag.innerHTML = '<strong style="color:var(--cb-text-main);">' + node.tag + '</strong> <span class="node-proto-label" style="color:var(--cb-text-muted); font-size: 0.85em;">(' + node.protocol + ')</span>';
                    }

                    var cellLat = row.insertCell(1);
                    cellLat.className = 'td';
                    cellLat.id = 'node-lat-' + node.tag;
                    cellLat.style.padding = '8px';
                    cellLat.style.fontWeight = 'bold';
                    cellLat.style.fontFamily = 'monospace';
                    cellLat.style.color = 'var(--cb-text-main)';
                    cellLat.textContent = node.latency > 0 ? (node.latency + ' ms') : _('Опрос...');

                    var cellStatus = row.insertCell(2);
                    cellStatus.className = 'td';
                    cellStatus.id = 'node-status-' + node.tag;
                    cellStatus.style.padding = '8px';
                    cellStatus.innerHTML = (node.latency > 0)
                        ? '<span style="color: var(--cb-ok-text); font-weight: bold;">● ' + _('Доступен') + '</span>'
                        : '<span style="color: var(--cb-warn-text); font-weight: bold;">● ' + _('Ожидание') + '</span>';
                });

                setTimeout(syncClashDelays, 400);
            })
            .catch(function() {});

            syncRealtimeStatus();
            fetchDiagnosticsOnce();
            checkUpdatesOnce();
            connectWebSocket();

            syncIntervalId = setInterval(syncClashDelays, 4000);
            statusPollIntervalId = setInterval(syncRealtimeStatus, 5000);
            diagPollIntervalId = setInterval(fetchDiagnosticsOnce, 4000);
        }, 250);

        return viewContainer;
    }
});