var REFRESH = 3000;
var refreshTimer, countdownTimer, countdown = 0;
var checkingUpdate = false, resetting = false;

var THEME_CYCLE = ['auto', 'light', 'dark'];
var THEME_ICON = { auto: '◐', light: '☀', dark: '☾' };

function applyTheme(mode) {
    if (mode === 'auto') {
        document.documentElement.removeAttribute('data-theme');
    } else {
        document.documentElement.setAttribute('data-theme', mode);
    }
    var btn = document.getElementById('theme-toggle-btn');
    if (btn) btn.textContent = THEME_ICON[mode];
}

function initTheme() {
    var t = localStorage.getItem('nbdns-theme') || 'auto';
    applyTheme(t);
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', function() {
        if ((localStorage.getItem('nbdns-theme') || 'auto') === 'auto') applyTheme('auto');
    });
}

function toggleTheme() {
    var cur = localStorage.getItem('nbdns-theme') || 'auto';
    var idx = (THEME_CYCLE.indexOf(cur) + 1) % THEME_CYCLE.length;
    var next = THEME_CYCLE[idx];
    if (next === 'auto') localStorage.removeItem('nbdns-theme');
    else localStorage.setItem('nbdns-theme', next);
    applyTheme(next);
}

function fmt(n) { return n.toString().replace(/\B(?=(\d{3})+(?!\d))/g, ','); }
function pct(n) { return n.toFixed(2) + '%'; }

function updateRuntime(r) {
    document.getElementById('uptime').textContent = r.uptime_str || '-';
    document.getElementById('goroutines').textContent = fmt(r.goroutines || 0);
    document.getElementById('mem-alloc').textContent = fmt(r.mem_alloc_mb || 0) + ' MB';
    document.getElementById('mem-sys').textContent = fmt(r.mem_sys_mb || 0) + ' MB';
    document.getElementById('mem-total').textContent = fmt(r.mem_total_mb || 0) + ' MB';
    document.getElementById('num-gc').textContent = fmt(r.num_gc || 0);
    document.getElementById('stats-duration').textContent = r.stats_duration_str || '-';
}

function updateQueries(q) {
    document.getElementById('total-queries').textContent = fmt(q.total || 0);
    document.getElementById('doh-queries').textContent = fmt(q.doh || 0);
    document.getElementById('cache-hits').textContent = fmt(q.cache_hits || 0);
    document.getElementById('cache-misses').textContent = fmt(q.cache_misses || 0);
    document.getElementById('failed-queries').textContent = fmt(q.failed || 0);
    document.getElementById('hit-rate').textContent = pct(q.hit_rate || 0);
}

function updateUpstream(list) {
    var tb = document.getElementById('upstream-tbody');
    if (!list || !list.length) { tb.innerHTML = '<tr><td colspan="5" class="empty">暂无数据</td></tr>'; return; }
    tb.innerHTML = list.map(function(u) {
        var c = u.error_rate > 10 ? ' class="error-high"' : '';
        return '<tr><td>' + (u.address || '-') + '</td><td>' + fmt(u.total_queries || 0) +
            '</td><td' + c + '>' + fmt(u.errors || 0) + '</td><td' + c + '>' + pct(u.error_rate || 0) +
            '</td><td class="hide-sm">' + (u.last_used || '-') + '</td></tr>';
    }).join('');
}

function updateFilters(data) {
    document.getElementById('filter-rules').textContent = fmt(data.rules || 0);
    document.getElementById('filter-unsupported').textContent = fmt(data.unsupported || 0);
    document.getElementById('filter-blocked').textContent = fmt(data.blocked_queries || 0);
    var tbody = document.getElementById('filter-sources');
    tbody.replaceChildren();
    if (!data.sources || !data.sources.length) {
        var empty = document.createElement('tr');
        var cell = document.createElement('td');
        cell.colSpan = 5; cell.className = 'empty'; cell.textContent = '暂无订阅';
        empty.appendChild(cell); tbody.appendChild(empty); return;
    }
    data.sources.forEach(function(source) {
        var row = document.createElement('tr');
        var state = source.last_error ? (source.from_cache ? '更新失败 · 使用缓存' : '更新失败')
            : source.from_cache ? '使用缓存' : source.last_success && source.last_success !== '0001-01-01T00:00:00Z' ? '正常' : '等待首次下载';
        var fields = [source.source || '-', fmt(source.rules || 0), fmt(source.unsupported || 0), state,
            source.last_success && source.last_success !== '0001-01-01T00:00:00Z' ? new Date(source.last_success).toLocaleString() : '-'];
        fields.forEach(function(value, i) {
            var cell = document.createElement('td');
            cell.textContent = value;
            if (i === 0) cell.className = 'filter-source';
            if (i === 3 && source.last_error) { cell.className = 'error-high'; cell.title = source.last_error; }
            row.appendChild(cell);
        });
        tbody.appendChild(row);
    });
}

function updateTopClients(list) {
    var leaderboard = document.getElementById('top-clients-list');
    leaderboard.replaceChildren();
    if (!list || !list.length) {
        var empty = document.createElement('p');
        empty.className = 'client-leaderboard-empty'; empty.textContent = '暂无数据';
        leaderboard.appendChild(empty); return;
    }
    list.slice(0, 10).forEach(function(c, i) {
        var item = document.createElement('article');
        item.className = 'client-rank-item' + (i < 3 ? ' rank-' + (i + 1) : '');
        item.setAttribute('role', 'listitem');
        var rank = document.createElement('span');
        rank.className = 'client-rank-number'; rank.textContent = String(i + 1).padStart(2, '0');
        var client = document.createElement('span');
        client.className = 'client-address'; client.textContent = c.key || '-'; client.title = c.key || '';
        var metric = document.createElement('span'); metric.className = 'client-rank-metric';
        var count = document.createElement('strong'); count.textContent = fmt(c.count || 0);
        var unit = document.createElement('small'); unit.textContent = '请求';
        metric.append(count, unit); item.append(rank, client, metric); leaderboard.appendChild(item);
    });
}

function clientListFor(domain) {
    if (Array.isArray(domain.top_clients) && domain.top_clients.length) return domain.top_clients.slice(0, 10);
    if (domain.top_client) return [{ client: domain.top_client, count: domain.top_client_count || 0 }];
    return [];
}

function makeClientBreakdown(domain, wasOpen) {
    var clients = clientListFor(domain);
    if (!clients.length) {
        var none = document.createElement('span'); none.className = 'dim'; none.textContent = '-'; return none;
    }

    var primary = document.createElement('span');
    primary.className = 'client-primary'; primary.textContent = clients[0].client || '-';
    primary.title = clients[0].client || '';
    var count = document.createElement('span');
    count.className = 'client-count'; count.textContent = fmt(clients[0].count || 0);
    count.title = '该客户端请求数';
    if (clients.length === 1) {
        var single = document.createElement('div'); single.className = 'client-summary single';
        single.append(primary, count); return single;
    }

    var details = document.createElement('details'); details.className = 'client-breakdown'; details.open = wasOpen;
    var summary = document.createElement('summary'); summary.className = 'client-summary';
    summary.setAttribute('aria-label', '查看该域名的客户端 Top 10');
    var more = document.createElement('span'); more.className = 'client-more'; more.textContent = '+' + (clients.length - 1);
    summary.append(primary, count, more); details.appendChild(summary);

    var panel = document.createElement('div'); panel.className = 'client-detail-panel';
    var label = document.createElement('div'); label.className = 'client-detail-title'; label.textContent = '客户端 Top 10';
    var list = document.createElement('ol');
    clients.forEach(function(item) {
        var row = document.createElement('li');
        var address = document.createElement('span'); address.textContent = item.client || '-'; address.title = item.client || '';
        var requests = document.createElement('strong'); requests.textContent = fmt(item.count || 0);
        row.append(address, requests); list.appendChild(row);
    });
    panel.append(label, list); details.appendChild(panel); return details;
}

function updateDomainRanking(list, tbodyId, emptyText) {
    var tb = document.getElementById(tbodyId);
    var openDomains = {};
    Array.prototype.forEach.call(tb.querySelectorAll('tr[data-domain]'), function(row) {
        var details = row.querySelector('details[open]');
        if (details) openDomains[row.getAttribute('data-domain')] = true;
    });
    tb.replaceChildren();
    if (!list || !list.length) {
        var emptyRow = document.createElement('tr');
        var emptyCell = document.createElement('td');
        emptyCell.colSpan = 4; emptyCell.className = 'empty'; emptyCell.textContent = emptyText;
        emptyRow.appendChild(emptyCell); tb.appendChild(emptyRow); return;
    }
    list.slice(0, 10).forEach(function(domain, i) {
        var row = document.createElement('tr');
        var domainName = domain.key || '-';
        row.setAttribute('data-domain', domainName);
        if (i < 3) row.className = 'rank-' + (i + 1);
        var rank = document.createElement('td'); rank.className = 'rank-cell'; rank.textContent = i + 1;
        var name = document.createElement('td'); name.className = 'domain-cell'; name.textContent = domainName; name.title = domainName;
        var requests = document.createElement('td'); requests.className = 'number-cell'; requests.textContent = fmt(domain.count || 0);
        var clients = document.createElement('td'); clients.className = 'client-breakdown-cell';
        clients.appendChild(makeClientBreakdown(domain, !!openDomains[domainName]));
        row.append(rank, name, requests, clients); tb.appendChild(row);
    });
}

function tick() {
    countdown--;
    if (countdown < 0) countdown = 0;
    document.getElementById('last-update').textContent = countdown + 's';
}

function resetCD() {
    countdown = REFRESH / 1000;
    if (countdownTimer) clearInterval(countdownTimer);
    countdownTimer = setInterval(tick, 1000);
    tick();
}

async function load() {
    try {
        var r = await fetch('/api/stats');
        if (!r.ok) throw 0;
        var d = await r.json();
        updateRuntime(d.runtime);
        updateQueries(d.queries);
        updateUpstream(d.upstreams);
        updateTopClients(d.top_clients);
        updateDomainRanking(d.top_domains, 'top-domains-tbody', '暂无解析记录');
        updateDomainRanking(d.top_blocked_domains, 'top-blocked-domains-tbody', '暂无拦截记录');
        var filters = await fetch('/api/filters');
        if (filters.ok) updateFilters(await filters.json());
        resetCD();
    } catch(e) {
        document.getElementById('last-update').textContent = '失败';
    }
}

function start() { if (refreshTimer) clearInterval(refreshTimer); refreshTimer = setInterval(load, REFRESH); }
function stop() {
    if (refreshTimer) { clearInterval(refreshTimer); refreshTimer = null; }
    if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null; }
}

async function loadVer() {
    try {
        var r = await fetch('/api/version');
        if (!r.ok) throw 0;
        var d = await r.json();
        document.getElementById('version-display').textContent = 'v' + d.version;
    } catch(e) {}
}

async function checkUpdate() {
    if (checkingUpdate) return;
    var btn = document.getElementById('check-update-btn');
    try {
        checkingUpdate = true; btn.disabled = true; btn.textContent = '检查中…';
        var r = await fetch('/api/check-update');
        if (!r.ok) throw 0;
        var d = await r.json();
        alert(d.has_update
            ? d.message + '\n当前: v' + d.current_version + '\n最新: v' + d.latest_version
            : d.message + '\n当前: v' + d.current_version);
    } catch(e) { alert('检查失败'); }
    finally { checkingUpdate = false; btn.disabled = false; btn.textContent = '检查更新'; }
}

async function resetStats() {
    if (resetting) return;
    if (!confirm('重置所有统计数据？')) return;
    var btn = document.getElementById('reset-stats-btn');
    try {
        resetting = true; btn.disabled = true; btn.textContent = '重置中…';
        var r = await fetch('/api/stats/reset', { method: 'POST' });
        if (!r.ok) throw 0;
        var d = await r.json();
        if (d.success) { alert(d.message || '已重置'); await load(); }
        else alert('失败: ' + (d.message || ''));
    } catch(e) { alert('重置失败'); }
    finally { resetting = false; btn.disabled = false; btn.textContent = '重置'; }
}

document.addEventListener('DOMContentLoaded', function() {
    initTheme();
    load(); loadVer(); start();
    document.getElementById('theme-toggle-btn').addEventListener('click', toggleTheme);
    document.getElementById('check-update-btn').addEventListener('click', checkUpdate);
    document.getElementById('reset-stats-btn').addEventListener('click', resetStats);
    document.addEventListener('visibilitychange', function() {
        if (document.hidden) stop(); else { load(); start(); }
    });
});

window.addEventListener('beforeunload', stop);
