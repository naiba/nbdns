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

function updateTopClients(list) {
    var tb = document.getElementById('top-clients-tbody');
    if (!list || !list.length) { tb.innerHTML = '<tr><td colspan="3" class="empty">暂无数据</td></tr>'; return; }
    tb.innerHTML = list.map(function(c, i) {
        return '<tr class="' + (i < 3 ? 'rank-' + (i + 1) : '') + '"><td class="rank-cell">' +
            (i + 1) + '</td><td>' + (c.key || '-') + '</td><td>' + fmt(c.count || 0) + '</td></tr>';
    }).join('');
}

function updateTopDomains(list) {
    var tb = document.getElementById('top-domains-tbody');
    if (!list || !list.length) { tb.innerHTML = '<tr><td colspan="4" class="empty">暂无数据</td></tr>'; return; }
    tb.innerHTML = list.map(function(d, i) {
        return '<tr class="' + (i < 3 ? 'rank-' + (i + 1) : '') + '"><td class="rank-cell">' +
            (i + 1) + '</td><td class="domain-cell" title="' + d.key + '">' + (d.key || '-') +
            '</td><td>' + fmt(d.count || 0) + '</td><td>' + (d.top_client || '-') + '</td></tr>';
    }).join('');
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
        updateTopDomains(d.top_domains);
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
