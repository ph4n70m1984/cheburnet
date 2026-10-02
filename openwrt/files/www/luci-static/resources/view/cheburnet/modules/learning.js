'use strict';
'require baseclass';
'require ui';
'require uci';

return baseclass.extend({
    createLearningSection: function() {
        var self = this;
        var pollIntervalId = null;

        // Проверяем реальное состояние опции в /etc/config/cheburnet
        var isEnabled = (uci.get('cheburnet', 'main', 'auto_learn_domains') === '1' ||
                         uci.get('cheburnet', 'main', 'domain_learning_enabled') === '1');

        if (!isEnabled) {
            // Если функция отключена, блок скрыт, фоновые сетевые запросы не создаются
            return E('div', { 'style': 'display: none;' });
        }

        function getAuthHeaders(customHeaders) {
            var headers = customHeaders || {};
            var apiToken = uci.get('cheburnet', 'main', 'api_token') || '';
            if (apiToken) {
                headers['X-API-Token'] = apiToken;
            }
            return headers;
        }

        function approveDomain(domain, btnEl) {
            if (!domain) return;
            btnEl.disabled = true;
            btnEl.textContent = _('Добавление...');

            fetch('http://' + window.location.hostname + ':8088/api/v1/learning/approve', {
                method: 'POST',
                headers: getAuthHeaders({ 'Content-Type': 'application/json' }),
                body: JSON.stringify({ domain: domain })
            })
            .then(function(r) {
                if (!r.ok) throw new Error('HTTP ' + r.status);
                return r.json();
            })
            .then(function() {
                ui.addNotification(null, E('p', {}, _('Домен добавлен в список обхода: ') + domain), 'info');
                fetchCandidates();
            })
            .catch(function(err) {
                btnEl.disabled = false;
                btnEl.textContent = _('Ошибка');
                ui.addNotification(null, E('p', {}, _('Сбой добавления: ') + err.message), 'error');
            });
        }

        function clearDomain(domain, btnEl) {
            if (!domain) return;
            btnEl.disabled = true;

            fetch('http://' + window.location.hostname + ':8088/api/v1/learning/clear', {
                method: 'POST',
                headers: getAuthHeaders({ 'Content-Type': 'application/json' }),
                body: JSON.stringify({ domain: domain })
            })
            .then(function() {
                fetchCandidates();
            })
            .catch(function() {
                btnEl.disabled = false;
            });
        }

        function renderCandidatesTable(candidates) {
            var tbody = document.getElementById('cb-learning-tbody');
            var counterEl = document.getElementById('cb-learning-count');
            if (!tbody) return;

            var list = Array.isArray(candidates) ? candidates.slice() : [];

            // Сортировка по количеству сбоев по убыванию
            list.sort(function(a, b) {
                return (b.fail_count || 0) - (a.fail_count || 0);
            });

            // Ограничение ровно до top 10
            if (list.length > 10) {
                list = list.slice(0, 10);
            }

            if (counterEl) {
                counterEl.textContent = list.length;
            }

            while (tbody.firstChild) {
                tbody.removeChild(tbody.firstChild);
            }

            if (list.length === 0) {
                tbody.appendChild(E('tr', { 'class': 'tr', 'id': 'cb-learning-empty' }, [
                    E('td', { 'class': 'td', 'colspan': '5', 'style': 'padding: 14px; text-align: center; color: var(--cb-text-muted); font-size: 13px;' },
                        _('Нет зафиксированных сбоев прямого доступа. Домены с TCP RST / таймаутами появятся здесь автоматически.')
                    )
                ]));
                return;
            }

            list.forEach(function(item) {
                var btnApprove = E('button', {
                    'class': 'btn cbi-button-action',
                    'style': 'padding: 2px 10px; font-size: 11px; font-weight: bold; margin-right: 6px;',
                    'click': function(e) {
                        e.preventDefault();
                        approveDomain(item.domain, this);
                    }
                }, [ E('span', {}, '✔ '), _('В обход') ]);

                var btnDismiss = E('button', {
                    'class': 'btn cbi-button-neutral',
                    'style': 'padding: 2px 8px; font-size: 11px;',
                    'click': function(e) {
                        e.preventDefault();
                        clearDomain(item.domain, this);
                    }
                }, '✕');

                var failBadgeStyle = item.fail_count >= 3
                    ? 'background: var(--cb-err-bg); color: var(--cb-err-text); border: 1px solid var(--cb-err-border);'
                    : 'background: var(--cb-warn-bg); color: var(--cb-warn-text); border: 1px solid var(--cb-warn-border);';

                var row = E('tr', { 'class': 'tr', 'style': 'border-bottom: 1px solid var(--cb-border);' }, [
                    E('td', { 'class': 'td', 'style': 'padding: 8px;' }, [
                        E('strong', { 'style': 'color: var(--cb-text-main); font-family: monospace; font-size: 13px;' }, item.domain)
                    ]),
                    E('td', { 'class': 'td', 'style': 'padding: 8px;' }, [
                        E('span', { 'style': 'padding: 2px 8px; border-radius: 4px; font-weight: bold; font-size: 11px; ' + failBadgeStyle },
                            item.fail_count + ' ' + _('сбоя(ев)')
                        )
                    ]),
                    E('td', { 'class': 'td', 'style': 'padding: 8px; font-family: monospace; font-size: 12px; color: var(--cb-text-muted);' },
                        item.last_client || '-'
                    ),
                    E('td', { 'class': 'td', 'style': 'padding: 8px; font-size: 12px; color: var(--cb-text-muted);' },
                        item.last_seen ? new Date(item.last_seen).toLocaleTimeString() : '-'
                    ),
                    E('td', { 'class': 'td', 'style': 'padding: 8px; text-align: right;' }, [
                        btnApprove,
                        btnDismiss
                    ])
                ]);

                tbody.appendChild(row);
            });
        }

        function fetchCandidates() {
            var host = window.location.hostname;
            var controller = new AbortController();
            var timeoutId = setTimeout(function() { controller.abort(); }, 3000);

            fetch('http://' + host + ':8088/api/v1/learning/candidates', {
                headers: getAuthHeaders(),
                signal: controller.signal
            })
            .then(function(r) {
                clearTimeout(timeoutId);
                return r.ok ? r.json() : [];
            })
            .then(function(data) {
                renderCandidatesTable(data);
            })
            .catch(function() {
                clearTimeout(timeoutId);
            });
        }

        var table = E('table', { 'class': 'table', 'style': 'width: 100%; border-collapse: collapse;' }, [
            E('thead', {}, [
                E('tr', { 'class': 'tr table-titles', 'style': 'background: var(--cb-bg-surface);' }, [
                    E('th', { 'class': 'th', 'style': 'padding: 8px; color: var(--cb-text-muted); border-bottom: 1px solid var(--cb-border);' }, _('Обнаруженный домен')),
                    E('th', { 'class': 'th', 'style': 'padding: 8px; color: var(--cb-text-muted); border-bottom: 1px solid var(--cb-border);' }, _('Сбоев прямого TCP')),
                    E('th', { 'class': 'th', 'style': 'padding: 8px; color: var(--cb-text-muted); border-bottom: 1px solid var(--cb-border);' }, _('Клиент в сети')),
                    E('th', { 'class': 'th', 'style': 'padding: 8px; color: var(--cb-text-muted); border-bottom: 1px solid var(--cb-border);' }, _('Последний сбой')),
                    E('th', { 'class': 'th', 'style': 'padding: 8px; text-align: right; color: var(--cb-text-muted); border-bottom: 1px solid var(--cb-border);' }, _('Действие'))
                ])
            ]),
            E('tbody', { 'id': 'cb-learning-tbody' }, [
                E('tr', { 'class': 'tr', 'id': 'cb-learning-empty' }, [
                    E('td', { 'class': 'td', 'colspan': '5', 'style': 'padding: 12px; text-align: center; color: var(--cb-text-muted);' }, _('Сбор статистики...'))
                ])
            ])
        ]);

        var container = E('details', {
            'class': 'cbi-section cb-details',
            'style': 'margin-bottom: 18px; border: 1px solid var(--cb-border); border-radius: 8px; background: var(--cb-bg-card); padding: 10px 14px;'
        }, [
            E('summary', {
                'style': 'font-size: 13px; font-weight: bold; cursor: pointer; display: flex; justify-content: space-between; align-items: center;'
            }, [
                E('div', { 'style': 'display: flex; align-items: center; gap: 8px;' }, [
                    E('span', { 'style': 'font-size: 15px;' }, '🧠'),
                    E('span', { 'style': 'color: var(--cb-text-main);' }, _('Автоматическое обнаружение блокировок (Top 10)')),
                    E('span', {
                        'id': 'cb-learning-count',
                        'style': 'font-size: 11px; padding: 1px 7px; border-radius: 10px; background: var(--cb-card-active-bg); border: 1px solid var(--cb-card-active-border); color: var(--cb-text-accent); font-weight: bold;'
                    }, '0')
                ]),
                E('span', { 'style': 'font-size: 11px; font-weight: normal; color: var(--cb-text-muted);' }, _('(развернуть для просмотра кандидатов)'))
            ]),
            E('div', { 'style': 'margin-top: 12px; overflow-x: auto;' }, [ table ])
        ]);

        setTimeout(function() {
            fetchCandidates();
            pollIntervalId = setInterval(fetchCandidates, 6000);
        }, 500);

        return container;
    }
});