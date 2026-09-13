'use strict';
'require baseclass';
'require ui';

return baseclass.extend({
    highlightActiveNode: function(selectedTag, subResolvedTag) {
        if (!selectedTag) return;

        var isAuto = (selectedTag === 'auto' || selectedTag === 'AUTO');
        window.cheburActiveNodeTag = isAuto ? 'auto' : selectedTag;
        var targetRowId = isAuto ? 'node-row-auto' : ('node-row-' + selectedTag);

        var nameEl = document.getElementById('active-server-name');
        if (nameEl) {
            nameEl.textContent = isAuto
                ? (subResolvedTag ? ('⚡ Авто → ' + subResolvedTag) : '⚡ Автовыбор (auto)')
                : selectedTag;
        }

        var rows = document.querySelectorAll('#chebur-nodes-table tr[id^="node-row-"]');
        rows.forEach(function(r) {
            r.style.background = '';
            r.style.boxShadow = '';
            var badge = r.querySelector('.active-node-badge');
            if (badge) badge.remove();
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
    },

    updateNodeUI: function(tag, latency) {
        var latEl = document.getElementById('node-lat-' + tag);
        var statusEl = document.getElementById('node-status-' + tag);
        if (!latEl || !statusEl) return;

        if (latency > 0) {
            latEl.textContent = latency + ' ms';
            var color = '#4ade80';
            if (latency >= 450) color = '#f87171';
            else if (latency >= 200) color = '#fb923c';

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
                if (latency >= 450) actColor = '#f87171';
                else if (latency >= 200) actColor = '#fb923c';
                cardLat.style.color = actColor;
            }
            if (cardBadge) {
                cardBadge.innerHTML = latency > 0 ? '● Доступен' : '● Офлайн';
                cardBadge.style.color = latency > 0 ? '#4ade80' : '#f87171';
            }
        }
    },

    selectProxyNode: function(nodeTag, onComplete) {
        var host = window.location.hostname;
        var displayName = (nodeTag === 'auto') ? _('Автовыбор') : nodeTag;
        ui.showIndicator('selecting-node', _('Переключение на %s...').format(displayName));

        var self = this;
        fetch('http://' + host + ':9090/proxies/PROXY', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name: nodeTag })
        })
        .then(function(r) {
            ui.hideIndicator('selecting-node');
            if (r.ok || r.status === 204) {
                self.highlightActiveNode(nodeTag, '');
                ui.addNotification(null, E('p', {}, _('Сервер переключен на: ') + displayName), 'info');
                if (onComplete) setTimeout(onComplete, 300);
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
                    self.highlightActiveNode(nodeTag, '');
                    ui.addNotification(null, E('p', {}, _('Сервер переключен на: ') + displayName), 'info');
                    if (onComplete) setTimeout(onComplete, 300);
                }
            })
            .catch(function(err) {
                ui.hideIndicator('selecting-node');
                ui.addNotification(null, E('p', {}, _('Ошибка переключения узла: ') + err), 'error');
            });
        });
    }
});