'use strict';
'require view';
'require form';
'require uci';
'require ui';
'require network';
'require tools.widgets as widgets';

var ALLOW_DOMAIN_CATEGORIES = [
    { tag: 'anime',          title: 'Anime' },
    { tag: 'block',          title: 'Block' },
    { tag: 'cloudflare',     title: 'Cloudflare' },
    { tag: 'cloudfront',     title: 'CloudFront' },
    { tag: 'digitalocean',   title: 'DigitalOcean' },
    { tag: 'discord',        title: 'Discord' },
    { tag: 'geoblock',       title: 'Geoblock' },
    { tag: 'google_ai',      title: 'Google AI' },
    { tag: 'google_meet',    title: 'Google Meet' },
    { tag: 'google_play',    title: 'Google Play' },
    { tag: 'hdrezka',        title: 'HDRezka' },
    { tag: 'hetzner',        title: 'Hetzner' },
    { tag: 'hodca',          title: 'Hodca' },
    { tag: 'meta',           title: 'Meta' },
    { tag: 'news',           title: 'News' },
    { tag: 'ovh',            title: 'OVH' },
    { tag: 'porn',           title: 'Porn' },
    { tag: 'roblox',         title: 'Roblox' },
    { tag: 'russia_inside',  title: 'Russia Inside / РКН' },
    { tag: 'russia_outside', title: 'Russia Outside' },
    { tag: 'telegram',       title: 'Telegram' },
    { tag: 'tiktok',         title: 'TikTok' },
    { tag: 'twitter',        title: 'Twitter / X' },
    { tag: 'ukraine_inside', title: 'Ukraine Inside' },
    { tag: 'youtube',        title: 'YouTube' }
];

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

        var statusSec = m.section(form.NamedSection, 'telemetry', 'cheburnet', _('Состояние, диагностика и телеметрия'));
        statusSec.anonymous = true;

        statusSec.render = function() {
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
                if (!banner || !content) {
                    return;
                }

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
                    var totalCount = snap.total_checks || 15;
                    content.innerHTML = '● Все системы работают штатно &middot; Проверено показателей: <strong>' + totalCount + '</strong>';
                    var container = document.getElementById('diag-problems-container');
                    if (container) {
                        container.innerHTML = '';
                    }
                    return;
                }

                var hasCrit = false;
                for (var i = 0; i < pList.length; i++) {
                    if (pList[i].severity === 'critical') {
                        hasCrit = true;
                        break;
                    }
                }

                if (hasCrit) {
                    banner.style.background = 'rgba(239, 68, 68, 0.15)';
                    banner.style.borderColor = 'rgba(239, 68, 68, 0.4)';
                    banner.style.color = '#f87171';
                } else {
                    banner.style.background = 'rgba(234, 179, 8, 0.15)';
                    banner.style.borderColor = 'rgba(234, 179, 8, 0.4)';
                    banner.style.color = '#fef08a';
                }

                content.innerHTML = '▲ Обнаружены проблемы: <strong>' + pList.length + '</strong>';
            }

            function executeProblemAction(action, btnEl) {
                if (!action) {
                    return;
                }
                btnEl.disabled = true;
                btnEl.textContent = _('Выполняется...');

                var controller = new AbortController();
                var timeoutId = setTimeout(function() { controller.abort(); }, 6000);

                fetch('http://' + window.location.hostname + ':8088/api/v1/actions/' + action, {
                    method: 'POST',
                    signal: controller.signal
                })
                .then(function(r) {
                    clearTimeout(timeoutId);
                    if (!r.ok) {
                        throw new Error('HTTP ' + r.status);
                    }
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
                if (!container) {
                    return;
                }
                container.innerHTML = '';

                var pList = Object.values(window.cheburProblems || {});
                if (pList.length === 0) {
                    return;
                }

                pList.forEach(function(prob) {
                    var isCrit = (prob.severity === 'critical');
                    var cardBg = isCrit ? 'rgba(239, 68, 68, 0.08)' : 'rgba(234, 179, 8, 0.08)';
                    var cardBorder = isCrit ? 'rgba(239, 68, 68, 0.3)' : 'rgba(234, 179, 8, 0.3)';
                    var msgColor = isCrit ? '#f87171' : '#fef08a';

                    var actionBtn = null;
                    if (prob.recoverable && prob.action) {
                        actionBtn = E('button', {
                            'class': 'btn cbi-button-action',
                            'style': 'margin: 0; font-size: 11px; padding: 4px 12px; white-space: nowrap;',
                            'click': function(e) {
                                e.preventDefault();
                                executeProblemAction(prob.action, this);
                            }
                        }, _('Исправить'));
                    }

                    var symptomsBlock = null;
                    if (prob.symptoms && Array.isArray(prob.symptoms)) {
                        var cleanSymptoms = prob.symptoms.filter(function(s) {
                            return s && s !== 'null' && typeof s === 'string';
                        });
                        if (cleanSymptoms.length > 0) {
                            var liNodes = [];
                            cleanSymptoms.forEach(function(item) {
                                liNodes.push(E('li', {}, item));
                            });

                            symptomsBlock = E('details', { 'style': 'margin-top: 6px; font-size: 11px; color: #a1a1aa;' }, [
                                E('summary', { 'style': 'cursor: pointer; user-select: none;' }, _('Сопутствующие симптомы (%d)').format(cleanSymptoms.length)),
                                E('ul', { 'style': 'margin: 4px 0 0 16px; padding: 0;' }, liNodes)
                            ]);
                        }
                    }

                    var leftChildren = [
                        E('div', { 'style': 'display: flex; align-items: center; gap: 8px;' }, [
                            E('span', { 'style': 'font-weight: bold; font-size: 13px; color: ' + msgColor + ';' }, prob.message),
                            E('span', { 'style': 'font-size: 10px; padding: 1px 6px; border-radius: 4px; background: rgba(255,255,255,0.1); color: #d4d4d8;' }, prob.component)
                        ])
                    ];
                    if (symptomsBlock) {
                        leftChildren.push(symptomsBlock);
                    }

                    var cardChildren = [
                        E('div', { 'style': 'display: flex; flex-direction: column;' }, leftChildren)
                    ];
                    if (actionBtn) {
                        cardChildren.push(E('div', {}, [actionBtn]));
                    } else {
                        cardChildren.push(E('span'));
                    }

                    var card = E('div', {
                        'id': 'problem-card-' + prob.id,
                        'style': 'padding: 10px 14px; border-radius: 6px; background: ' + cardBg + '; border: 1px solid ' + cardBorder + '; display: flex; justify-content: space-between; align-items: center; gap: 15px;'
                    }, cardChildren);

                    container.appendChild(card);
                });
            }

            function renderDiagnosticSnapshot(snap) {
                if (!snap) {
                    return;
                }
                window.cheburLastDiagSnapshot = snap;
                window.cheburProblems = {};
                if (snap.problems && Array.isArray(snap.problems)) {
                    snap.problems.forEach(function(p) {
                        window.cheburProblems[p.id] = p;
                    });
                }
                updateBannerContent();
                renderProblemsCards();
            }

            function fetchDiagnosticsOnce() {
                var host = window.location.hostname;
                var controller = new AbortController();
                var timeoutId = setTimeout(function() { controller.abort(); }, 3500);

                fetch('http://' + host + ':8088/api/v1/diagnostics', {
                    signal: controller.signal
                })
                .then(function(r) {
                    clearTimeout(timeoutId);
                    if (!r.ok) {
                        throw new Error('HTTP ' + r.status);
                    }
                    return r.json();
                })
                .then(function(snap) {
                    renderDiagnosticSnapshot(snap);
                })
                .catch(function() {
                    clearTimeout(timeoutId);
                });
            }

            function handleWsEvent(msg) {
                if (!msg || !msg.type) {
                    return;
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
                } else if (msg.type === 'upgrade_error' && msg.error) {
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
                        var btn = this;
                        var origText = btn.textContent;
                        btn.textContent = _('Опрос...');
                        btn.disabled = true;

                        var controller = new AbortController();
                        var timeoutId = setTimeout(function() { controller.abort(); }, 4000);

                        fetch('http://' + window.location.hostname + ':8088/api/v1/diagnostics', {
                            signal: controller.signal
                        })
                        .then(function(r) {
                            clearTimeout(timeoutId);
                            if (!r.ok) {
                                throw new Error('HTTP ' + r.status);
                            }
                            return r.json();
                        })
                        .then(function(snap) {
                            renderDiagnosticSnapshot(snap);
                        })
                        .catch(function(err) {
                            clearTimeout(timeoutId);
                            ui.addNotification(null, E('p', {}, _('Ошибка опроса диагностики: ') + err), 'error');
                        })
                        .finally(function() {
                            btn.textContent = origText;
                            btn.disabled = false;
                        });
                    }
                }, _('Опросить'))
            ]);

            var problemsContainer = E('div', {
                'id': 'diag-problems-container',
                'style': 'margin-bottom: 15px; display: flex; flex-direction: column; gap: 8px;'
            });

            var updateBanner = E('div', {
                'id': 'update-notification-banner',
                'style': 'display: none; align-items: center; justify-content: space-between; margin-bottom: 15px; padding: 12px 16px; border-radius: 6px; background: rgba(234, 179, 8, 0.15); border: 1px solid rgba(234, 179, 8, 0.4); color: #fef08a;'
            }, [
                E('span', { 'id': 'update-banner-text', 'style': 'font-size: 13px; font-weight: 500;' }, ''),
                E('button', {
                    'class': 'btn cbi-button-action',
                    'id': 'btn-run-update-banner',
                    'style': 'font-size: 12px; margin: 0; padding: 4px 12px;',
                    'click': function(e) {
                        e.preventDefault();
                        var btn = this;
                        btn.disabled = true;
                        ui.showIndicator('updating-system', _('Проверка свободного места и установка обновлений...'));

                        fetch('http://' + window.location.hostname + ':8088/api/v1/updates/upgrade', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ target: 'all' })
                        })
                        .then(function(r) { return r.json(); })
                        .then(function(data) {
                            ui.hideIndicator('updating-system');
                            btn.disabled = false;
                            if (data && data.error) {
                                ui.addNotification(null, E('p', {}, _('Ошибка установки: ') + data.error), 'error');
                                var txt = document.getElementById('update-banner-text');
                                var b = document.getElementById('update-notification-banner');
                                if (b && txt) {
                                    b.style.background = 'rgba(239, 68, 68, 0.15)';
                                    b.style.borderColor = 'rgba(239, 68, 68, 0.4)';
                                    b.style.color = '#f87171';
                                    txt.innerHTML = '✖ <strong>Сбой:</strong> ' + data.error;
                                }
                            } else {
                                ui.addNotification(null, E('p', {}, _('Процесс обновления успешно запущен в фоне.')), 'info');
                            }
                        })
                        .catch(function(err) {
                            ui.hideIndicator('updating-system');
                            btn.disabled = false;
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
                problemsContainer,
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

            function formatComponentStatus(name, comp) {
                if (!comp || !comp.installed) {
                    return '<li>' + name + ': <span style="color:#71717a;">Не установлен</span></li>';
                }
                if (comp.has_update) {
                    return '<li>' + name + ': <b>' + comp.current + '</b> → <span style="color:#4ade80; font-weight:bold;">' + comp.latest + ' (Доступно обновление)</span></li>';
                }
                return '<li>' + name + ': <b>' + comp.current + '</b> → <span style="color:#8c8c8c;">Актуально</span></li>';
            }

            function showUpdateNotification(data) {
                if (!data) {
                    return;
                }
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
                        txt.textContent = 'Доступны обновления компонентов: ' + alerts.join(' | ');
                        banner.style.display = 'flex';
                        banner.style.background = 'rgba(234, 179, 8, 0.15)';
                        banner.style.borderColor = 'rgba(234, 179, 8, 0.4)';
                        banner.style.color = '#fef08a';
                    }
                }
            }

            function renderUpdateReport(r) {
                var statusDiv = document.getElementById('ws-update-status');
                var btnUpgrade = document.getElementById('ws-btn-upgrade');

                if (statusDiv) {
                    var html = '<ul style="margin:0; padding-left:20px; line-height: 1.8; color:#c9d1d9;">';
                    html += formatComponentStatus('Chebur.NET', r.cheburnet);
                    html += formatComponentStatus('Sing-box', r.sing_box);
                    html += '</ul>';
                    statusDiv.innerHTML = html;
                }

                if (btnUpgrade) {
                    var hasAppUpdate = r.cheburnet && r.cheburnet.has_update;
                    var hasSbUpdate = r.sing_box && r.sing_box.installed && r.sing_box.has_update;

                    if (hasAppUpdate || hasSbUpdate) {
                        btnUpgrade.style.display = 'inline-block';
                    } else {
                        btnUpgrade.style.display = 'none';
                    }
                }
            }

            function checkUpdates() {
                var host = window.location.hostname;
                fetch('http://' + host + ':8088/api/v1/updates/check')
                    .then(function(r) { return r.json(); })
                    .then(function(data) {
                        showUpdateNotification(data);
                        renderUpdateReport(data);
                    })
                    .catch(function() {});
            }

            function highlightActiveNode(selectedTag, subResolvedTag) {
                if (!selectedTag) {
                    return;
                }

                var isAuto = (selectedTag === 'auto' || selectedTag === 'AUTO');
                window.cheburActiveNodeTag = isAuto ? 'auto' : selectedTag;
                var targetRowId = isAuto ? 'node-row-auto' : ('node-row-' + selectedTag);

                var nameEl = document.getElementById('active-server-name');
                if (nameEl) {
                    if (isAuto) {
                        nameEl.textContent = subResolvedTag ? ('⚡ Авто → ' + subResolvedTag) : '⚡ Автовыбор (auto)';
                    } else {
                        nameEl.textContent = selectedTag;
                    }
                }

                var rows = document.querySelectorAll('#chebur-nodes-table tr[id^="node-row-"]');
                rows.forEach(function(r) {
                    r.style.background = '';
                    r.style.boxShadow = '';
                    var badge = r.querySelector('.active-node-badge');
                    if (badge) {
                        badge.remove();
                    }
                });

                var activeRow = document.getElementById(targetRowId);
                if (activeRow) {
                    activeRow.style.background = 'rgba(56, 189, 248, 0.12)';
                    activeRow.style.boxShadow = 'inset 3px 0 0 0 #38bdf8';

                    var tagCell = activeRow.cells[0];
                    if (tagCell && !tagCell.querySelector('.active-node-badge')) {
                        var badge = E('span', {
                            'class': 'active-node-badge',
                            'style': 'margin-left: 8px; font-size: 10px; font-weight: bold; padding: 2px 6px; border-radius: 4px; background: #0284c7; color: #ffffff;'
                        }, _('АКТИВЕН'));
                        tagCell.appendChild(badge);
                    }

                    var protoSpan = activeRow.querySelector('.node-proto-label');
                    var cardProto = document.getElementById('active-server-proto');
                    if (cardProto) {
                        cardProto.textContent = isAuto
                            ? (subResolvedTag ? ('Динамический выбор: ' + subResolvedTag) : 'urltest')
                            : (protoSpan ? protoSpan.textContent : '');
                    }
                }
            }

            function selectProxyNode(nodeTag) {
                var host = window.location.hostname;
                var displayName = (nodeTag === 'auto') ? _('Автовыбор') : nodeTag;
                ui.showIndicator('selecting-node', _('Переключение на %s...').format(displayName));

                fetch('http://' + host + ':9090/proxies/PROXY', {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ name: nodeTag })
                })
                .then(function(r) {
                    ui.hideIndicator('selecting-node');
                    if (r.ok || r.status === 204) {
                        highlightActiveNode(nodeTag, '');
                        ui.addNotification(null, E('p', {}, _('Сервер переключен на: ') + displayName), 'info');
                        setTimeout(syncClashDelays, 300);
                    } else {
                        throw new Error('HTTP ' + r.status);
                    }
                })
                .catch(function() {
                    fetch('http://' + host + ':8088/api/v1/nodes/select', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ tag: nodeTag })
                    })
                    .then(function(r) {
                        ui.hideIndicator('selecting-node');
                        if (r.ok) {
                            highlightActiveNode(nodeTag, '');
                            ui.addNotification(null, E('p', {}, _('Сервер переключен на: ') + displayName), 'info');
                            setTimeout(syncClashDelays, 300);
                        }
                    })
                    .catch(function(err) {
                        ui.hideIndicator('selecting-node');
                        ui.addNotification(null, E('p', {}, _('Ошибка переключения узла: ') + err), 'error');
                    });
                });
            }

            function updateNodeUI(tag, latency) {
                var latEl = document.getElementById('node-lat-' + tag);
                var statusEl = document.getElementById('node-status-' + tag);
                if (!latEl || !statusEl) {
                    return;
                }

                if (latency > 0) {
                    latEl.textContent = latency + ' ms';
                    var color = '#4ade80';
                    if (latency >= 450) {
                        color = '#f87171';
                    } else if (latency >= 200) {
                        color = '#fb923c';
                    }
                    latEl.style.color = color;
                    statusEl.innerHTML = '<span style="color: #4ade80; font-weight: bold;">● Доступен</span>';
                } else {
                    latEl.textContent = 'Timeout';
                    latEl.style.color = '#71717a';
                    statusEl.innerHTML = '<span style="color: #f87171; font-weight: bold;">● Офлайн</span>';
                }

                if (tag === window.cheburActiveNodeTag || (window.cheburActiveNodeTag === 'auto' && tag === 'auto')) {
                    var cardLat = document.getElementById('active-server-lat');
                    var cardBadge = document.getElementById('active-server-badge');
                    if (cardLat) {
                        cardLat.textContent = latency > 0 ? (latency + ' ms') : 'Timeout';
                        var actColor = '#4ade80';
                        if (latency >= 450) {
                            actColor = '#f87171';
                        } else if (latency >= 200) {
                            actColor = '#fb923c';
                        }
                        cardLat.style.color = actColor;
                    }
                    if (cardBadge) {
                        cardBadge.innerHTML = latency > 0 ? '● Доступен' : '● Офлайн';
                        cardBadge.style.color = latency > 0 ? '#4ade80' : '#f87171';
                    }
                }
            }

            function syncClashDelays() {
                if (isSyncingDelays) {
                    return;
                }
                isSyncingDelays = true;

                var host = window.location.hostname;

                fetch('http://' + host + ':9090/proxies')
                    .then(function(r) {
                        if (!r.ok) {
                            throw new Error('HTTP ' + r.status);
                        }
                        return r.json();
                    })
                    .then(function(data) {
                        if (!data || !data.proxies) {
                            return;
                        }

                        var proxyGroup = data.proxies['PROXY'] || data.proxies['proxy'];
                        var autoGroup = data.proxies['auto'] || data.proxies['AUTO'];
                        var autoCurrentBest = (autoGroup && autoGroup.now) ? autoGroup.now : '';

                        if (proxyGroup && proxyGroup.now) {
                            if (proxyGroup.now === 'auto' || proxyGroup.now === 'AUTO') {
                                highlightActiveNode('auto', autoCurrentBest);
                            } else {
                                highlightActiveNode(proxyGroup.now, '');
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
                            updateNodeUI('auto', autoDelay);
                        }

                        var entries = Object.entries(data.proxies).filter(function(pair) {
                            var tag = pair[0];
                            return !['DIRECT', 'REJECT', 'PROXY', 'GLOBAL', 'auto', 'AUTO', 'auto-out'].includes(tag);
                        });

                        entries.forEach(function(pair) {
                            var tag = pair[0];
                            var info = pair[1];
                            if (info.history && info.history.length > 0) {
                                var last = info.history[info.history.length - 1];
                                if (last.delay !== undefined && last.delay > 0) {
                                    updateNodeUI(tag, last.delay);
                                } else if (last.delay === 0) {
                                    updateNodeUI(tag, 0);
                                }
                            } else {
                                updateNodeUI(tag, 0);
                            }
                        });
                    })
                    .catch(function() {})
                    .finally(function() {
                        isSyncingDelays = false;
                    });
            }

            function syncRealtimeStatus() {
                var host = window.location.hostname;
                fetch('http://' + host + ':8088/api/v1/status')
                    .then(function(r) { return r.json(); })
                    .then(function(data) {
                        var statusEl = document.getElementById('daemon-status');
                        if (statusEl) {
                            statusEl.textContent = '● Онлайн';
                            statusEl.style.color = '#4ade80';
                        }
                        var countEl = document.getElementById('total-nodes');
                        if (countEl && data.nodes_count !== undefined) {
                            countEl.textContent = data.nodes_count;
                        }
                        var ipEl = document.getElementById('outbound-ip');
                        if (ipEl && data.outbound_ip) {
                            ipEl.textContent = data.outbound_ip;
                        }

                        if (data.active_node && !window.cheburActiveNodeTag) {
                            highlightActiveNode(data.active_node, '');
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

            function connectWebSocket(host) {
                if (window.cheburWs && (window.cheburWs.readyState === WebSocket.OPEN || window.cheburWs.readyState === WebSocket.CONNECTING)) {
                    return;
                }

                var ws = new WebSocket('ws://' + host + ':8088/ws/telemetry');
                window.cheburWs = ws;

                ws.onopen = function() {
                    var statusEl = document.getElementById('daemon-status');
                    if (statusEl) {
                        statusEl.textContent = '● Онлайн';
                        statusEl.style.color = '#4ade80';
                    }
                };

                ws.onmessage = function(event) {
                    try {
                        var msg = JSON.parse(event.data);
                        if (msg.active_node && window.cheburActiveNodeTag !== 'auto') {
                            highlightActiveNode(msg.active_node, '');
                        }
                        if (msg.type === 'update_report' && msg.data) {
                            showUpdateNotification(msg.data);
                            renderUpdateReport(msg.data);
                        }
                        handleWsEvent(msg);
                    } catch (e) {}
                };

                ws.onerror = function() {
                    var statusEl = document.getElementById('daemon-status');
                    if (statusEl) {
                        statusEl.textContent = '● Ошибка связи';
                        statusEl.style.color = '#f87171';
                    }
                };

                ws.onclose = function() {
                    var statusEl = document.getElementById('daemon-status');
                    if (statusEl) {
                        statusEl.textContent = '● Офлайн';
                        statusEl.style.color = '#f87171';
                    }
                    setTimeout(function() { connectWebSocket(host); }, 5000);
                };
            }

            function initTelemetry() {
                var host = window.location.hostname;
                var tbl = document.getElementById('chebur-nodes-table');

                if (syncIntervalId) clearInterval(syncIntervalId);
                if (diagPollIntervalId) clearInterval(diagPollIntervalId);
                if (statusPollIntervalId) clearInterval(statusPollIntervalId);

                fetch('http://' + host + ':8088/api/v1/nodes')
                    .then(function(r) { return r.json(); })
                    .then(function(nodes) {
                        if (!nodes || nodes.length === 0) {
                            return;
                        }

                        var countEl = document.getElementById('total-nodes');
                        if (countEl) {
                            countEl.textContent = nodes.length;
                        }

                        while (tbl.rows.length > 1) {
                            tbl.deleteRow(1);
                        }

                        nodes.forEach(function(node) {
                            var row = tbl.insertRow(-1);
                            row.className = 'tr';
                            row.id = 'node-row-' + node.tag;
                            row.style.cursor = 'pointer';
                            row.title = (node.tag === 'auto')
                                ? _('Нажмите, чтобы включить автоматический выбор быстрейшего сервера')
                                : _('Нажмите, чтобы сделать этот сервер активным');

                            row.onclick = function() {
                                selectProxyNode(node.tag);
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
                    .catch(function(e) { console.error('Nodes fetch error:', e); });

                syncRealtimeStatus();
                fetchDiagnosticsOnce();
                connectWebSocket(host);

                syncIntervalId = setInterval(syncClashDelays, 4000);
                statusPollIntervalId = setInterval(syncRealtimeStatus, 5000);
                diagPollIntervalId = setInterval(fetchDiagnosticsOnce, 4000);
                setTimeout(checkUpdates, 1500);
            }

            setTimeout(initTelemetry, 250);
            return viewContainer;
        };

        window.cheburCheckUpdates = function(e) {
            e.preventDefault();
            var statusDiv = document.getElementById('ws-update-status');
            if (statusDiv) {
                statusDiv.innerHTML = '<span style="color:#fbbf24;">Запрос отправлен. Выполняется проверка GitHub и пакетов...</span>';
            }
            if (window.cheburWs && window.cheburWs.readyState === WebSocket.OPEN) {
                window.cheburWs.send(JSON.stringify({ action: 'check_updates' }));
            } else {
                if (statusDiv) {
                    statusDiv.innerHTML = '<span style="color:#f87171;">Ошибка: соединение с сервером не установлено.</span>';
                }
            }
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

        // ==========================================
        // 2. СЕКЦИЯ КОНФИГУРАЦИИ (АККОРДЕОН)
        // ==========================================
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
        o.value('https://www.gstatic.com/generate_204', 'https://www.gstatic.com/generate_204 (Google)');
        o.value('https://cp.cloudflare.com/generate_204', 'https://cp.cloudflare.com/generate_204 (Cloudflare)');
        o.default = 'https://www.gstatic.com/generate_204';

        o = s.taboption('general', form.Flag, 'auto_hwid', _('Автоматический HWID (MAC-bound)'));
        o.default = '1';

        o = s.taboption('general', form.Value, 'custom_hwid', _('Глобальный кастомный HWID (опционально)'));
        o.depends('auto_hwid', '0');
        o.placeholder = '00000000-0000-0000-0000-000000000000';

        function wrapGridSectionInDetails(sectionObj, titleText, descText, isOpen) {
            var orig = sectionObj.render;
            sectionObj.render = function() {
                var self = this;
                var args = arguments;
                return Promise.resolve(orig.apply(self, args)).then(function(node) {
                    var detailsNode = document.createElement('details');
                    detailsNode.className = 'cbi-section';
                    detailsNode.style.marginTop = '15px';
                    detailsNode.style.border = '1px solid rgba(255, 255, 255, 0.1)';
                    detailsNode.style.borderRadius = '6px';
                    detailsNode.style.padding = '10px';
                    detailsNode.style.background = 'rgba(255, 255, 255, 0.01)';
                    if (isOpen) {
                        detailsNode.open = true;
                    }

                    var summaryNode = E('summary', {
                        'style': 'font-size: 14px; font-weight: bold; cursor: pointer; padding: 6px 8px; user-select: none; color: #38bdf8; display: flex; justify-content: space-between; align-items: center;'
                    }, [
                        E('span', {}, titleText),
                        E('span', { 'style': 'font-size: 11px; font-weight: normal; color: #64748b;' }, _('(нажмите, чтобы развернуть/свернуть)'))
                    ]);

                    detailsNode.appendChild(summaryNode);
                    if (descText) {
                        var p = E('p', { 'class': 'cbi-section-descr', 'style': 'margin: 6px 8px 12px 8px; color: #94a3b8;' }, descText);
                        detailsNode.appendChild(p);
                    }
                    detailsNode.appendChild(node);
                    return detailsNode;
                });
            };
        }

        // --- ТАБЛИЦА ПОДПИСОК ---
        var subSec = m.section(form.GridSection, 'subscription', _('Таблица ссылок подписок'));
        subSec.anonymous = true;
        subSec.addremove = true;
        subSec.sortable = true;
        wrapGridSectionInDetails(subSec, _('▶ Таблица ссылок подписок'), null, false);

        o = subSec.option(form.Flag, 'enabled', _('Вкл'));
        o.default = '1';
        o.rmempty = false;
        o.editable = true;

        o = subSec.option(form.Value, 'name', _('Наименование провайдера'));
        o.placeholder = _('Например: EcoBuy / Мой VPN');
        o.datatype = 'string';
        o.rmempty = false;
        o.editable = true;
        o.renderWidget = function() {
            var node = form.Value.prototype.renderWidget.apply(this, arguments);
            var input = node.querySelector('input');
            if (input) {
                input.style.maxWidth = '260px';
                input.style.width = '100%';
            }
            return node;
        };

        o = subSec.option(form.Value, 'url', _('URL подписки'));
        o.placeholder = 'https://sub.domain.com/token';
        o.rmempty = false;
        o.modalonly = true;

        // Выбор режима фильтрации серверов
        o = subSec.option(form.ListValue, 'filter_mode', _('Режим фильтрации серверов'));
        o.value('exclude', _('Исключить по регулярному выражению (Blacklist)'));
        o.value('include', _('Оставить только совпадающие (Whitelist / Показать)'));
        o.default = 'exclude';
        o.rmempty = false;
        o.modalonly = true;

        o = subSec.option(form.DynamicList, 'exclude_regex', _('Регулярные выражения (RegExp)'));
        o.description = _('Шаблоны для фильтрации названий серверов. В режиме "Исключить" скрывает совпавшие, в режиме "Оставить" — отображает только их (например: обход|lte). Если список пуст — выводятся все.');
        o.placeholder = _('Например: обход|lte');
        o.datatype = 'string';
        o.rmempty = true;
        o.modalonly = true;

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
        o.modalonly = true;

        o = subSec.option(form.Value, 'hwid', _('HWID (опционально)'));
        o.placeholder = 'Оставьте пустым, если не нужен';
        o.rmempty = true;
        o.modalonly = true;

        // --- ТАБЛИЦА ПОЛИТИК КЛИЕНТОВ ---
        var clientSec = m.section(form.GridSection, 'client_rule', _('Политики для устройств (Client Policy)'),
            _('Индивидуальные правила маршрутизации для устройств локальной сети. Направляют трафик устройства мимо общих списков.'));
        clientSec.anonymous = true;
        clientSec.addremove = true;
        clientSec.sortable = true;
        wrapGridSectionInDetails(clientSec, _('▶ Политики для устройств (Client Policy)'), _('Индивидуальные правила маршрутизации для устройств локальной сети.'), false);

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

        for (var mac in hosts) {
            var host = hosts[mac];
            if (host.ipv4 && host.ipv4.length > 0) {
                var ip = host.ipv4[0];
                var title = (host.name ? host.name + ' (' + ip + ')' : ip) + ' [' + mac + ']';
                o.value(ip, title);
            }
        }

        o = clientSec.option(form.ListValue, 'mode', _('Политика'));
        o.value('rules', _('По спискам (Только заблокированные)'));
        o.value('full_proxy', _('Всё в прокси (Полный туннель)'));
        o.value('direct', _('Прямой (Мимо прокси / Direct)'));
        o.default = 'rules';
        o.editable = true;

        // --- ТАБЛИЦА СЕКЦИЙ МАРШРУТИЗАЦИИ СЕРВИСОВ ---
        var routeSec = m.section(form.GridSection, 'route_policy', _('Секции маршрутизации сервисов (Route Policies)'),
            _('Выборочная привязка сервисных списков, доменов и подсетей к конкретным нодам выхода.'));
        routeSec.anonymous = true;
        routeSec.addremove = true;
        routeSec.sortable = true;
        wrapGridSectionInDetails(routeSec, _('▶ Секции маршрутизации сервисов (Route Policies)'), _('Выборочная привязка сервисных списков, доменов и подсетей к конкретным нодам выхода.'), false);

        o = routeSec.option(form.Flag, 'enabled', _('Вкл'));
        o.default = '1';
        o.rmempty = false;
        o.editable = true;

        o = routeSec.option(form.Value, 'name', _('Название секции'));
        o.placeholder = 'ai / youtube-hu';
        o.rmempty = false;
        o.editable = true;

        o = routeSec.option(form.DynamicList, 'rulesets', _('Сервисные списки'));
        o.placeholder = _('Выберите списки');
        ALLOW_DOMAIN_CATEGORIES.forEach(function(cat) {
            o.value(cat.tag, cat.tag + ' — ' + cat.title);
        });
        o.editable = true;

        o = routeSec.option(form.ListValue, 'outbound', _('Сервер выхода (Outbound)'));
        o.placeholder = _('Выберите сервер выхода');
        o.value('PROXY', _('Глобальный автовыбор (PROXY / AUTO)'));
        o.value('direct-out', _('Прямой доступ (Мимо прокси / Direct)'));
        o.rmempty = false;
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

        o = routeSec.option(form.TextValue, 'custom_domains', _('Дополнительные домены секции'));
        o.rows = 4;
        o.wrap = 'off';
        o.placeholder = 'gemini.google.com\nai.google.dev';
        o.modalonly = true;

        o = routeSec.option(form.TextValue, 'custom_subnets', _('Дополнительные подсети секции'));
        o.rows = 4;
        o.wrap = 'off';
        o.placeholder = '142.250.0.0/15';
        o.modalonly = true;

        // --- ВКЛАДКА 2: МАРШРУТИЗАЦИЯ СПИСКОВ ---
        o = s.taboption('routing_rules', form.ListValue, 'ruleset_update_interval', _('Интервал обновления списков'));
        o.depends('routing_mode', 'rules');
        o.value('24h', _('24 часа (каждый день)'));
        o.value('72h', _('72 часа (раз в 3 дня)'));
        o.value('168h', _('1 неделя'));
        o.default = '72h';
        o.rmempty = false;

        o = s.taboption('routing_rules', form.DynamicList, 'rulesets', _('Service list (Предопределенные списки по умолчанию)'));
        o.depends('routing_mode', 'rules');
        ALLOW_DOMAIN_CATEGORIES.forEach(function(cat) {
            o.value(cat.tag, cat.tag + ' — ' + cat.title);
        });

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
            var node = form.TextValue.prototype.renderWidget.apply(this, arguments);
            var textarea = node.querySelector('textarea');
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
            var node = form.TextValue.prototype.renderWidget.apply(this, arguments);
            var textarea = node.querySelector('textarea');
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

        o = s.taboption('routing_rules', form.ListValue, 'custom_port_type', _('Тип пользовательского списка портов'));
        o.depends('routing_mode', 'rules');
        o.value('text', _('Текстовый список'));
        o.default = 'text';

        o = s.taboption('routing_rules', form.TextValue, 'custom_ports', _('Список пользовательских портов'));
        o.depends('routing_mode', 'rules');
        o.rows = 5;
        o.wrap = 'off';
        o.placeholder = '443\n50000:65535\n8080-8090';
        o.description = _('Укажите одиночные порты или диапазоны (через двоеточие или дефис), которые нужно перенаправлять в прокси.');
        o.renderWidget = function() {
            var node = form.TextValue.prototype.renderWidget.apply(this, arguments);
            var textarea = node.querySelector('textarea');
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

        // --- ВКЛАДКА 4: МЕНЕДЖЕР ОБНОВЛЕНИЙ ---
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
                    ui.showIndicator('reloading-cheburnet', _('Применение настроек в Chebur.NET...'));

                    var host = window.location.hostname;
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
                            ui.addNotification(null, E('p', {}, _('Настройки успешно применены!')), 'info');
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
    }
});