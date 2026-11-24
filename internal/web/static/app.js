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

var ECS_FIX_STATUS = {
    enabled: { label: '已开启', empty: '暂未发现被 ECS 屏蔽的域名，一切正常', emptyClass: 'empty ok' },
    disabled: { label: '已关闭', empty: '自动修复已关闭；删除配置中的 "ecs_blackhole_fallback": false 即可开启', emptyClass: 'empty' },
    no_ecs: { label: '未配置 ECS', empty: '未向上游发送 ECS，不会出现 ECS 黑洞，无需修复', emptyClass: 'empty' }
};

function relativeTime(iso) {
    var t = Date.parse(iso);
    if (!iso || isNaN(t) || t <= 0) return '-';
    var sec = Math.max(0, Math.round((Date.now() - t) / 1000));
    if (sec < 60) return '刚刚';
    if (sec < 3600) return Math.floor(sec / 60) + ' 分钟前';
    if (sec < 86400) return Math.floor(sec / 3600) + ' 小时前';
    return Math.floor(sec / 86400) + ' 天前';
}

function makeAddressList(addresses, kind) {
    var wrap = document.createElement('div'); wrap.className = 'addr-list';
    var list = addresses || [];
    if (!list.length) {
        var none = document.createElement('span'); none.className = 'dim'; none.textContent = '-';
        wrap.appendChild(none); return wrap;
    }
    list.slice(0, 3).forEach(function(address) {
        var chip = document.createElement('span'); chip.className = 'addr ' + kind; chip.textContent = address;
        wrap.appendChild(chip);
    });
    if (list.length > 3) {
        var more = document.createElement('span'); more.className = 'addr more';
        more.textContent = '+' + (list.length - 3); more.title = list.slice(3).join('\n');
        wrap.appendChild(more);
    }
    return wrap;
}

function updateECSFixes(e) {
    e = e || {};
    var info = ECS_FIX_STATUS[e.status] || { label: '未知', empty: '暂无修复记录', emptyClass: 'empty' };
    var pill = document.getElementById('ecs-fix-status');
    pill.textContent = info.label; pill.setAttribute('data-status', e.status || '');
    var domains = e.domains || [];
    document.getElementById('ecs-fix-count').textContent = fmt(e.fixed || 0);
    document.getElementById('ecs-fix-domains').textContent = fmt(domains.length);
    var last = document.getElementById('ecs-fix-last');
    last.textContent = domains.length ? relativeTime(domains[0].last_seen) : '-';
    last.title = domains.length ? new Date(domains[0].last_seen).toLocaleString() : '';

    var tb = document.getElementById('ecs-fix-tbody');
    tb.replaceChildren();
    if (!domains.length) {
        var emptyRow = document.createElement('tr'); var emptyCell = document.createElement('td');
        emptyCell.colSpan = 5; emptyCell.className = info.emptyClass; emptyCell.textContent = info.empty;
        emptyRow.appendChild(emptyCell); tb.appendChild(emptyRow); return;
    }
    domains.forEach(function(d) {
        var row = document.createElement('tr');
        var name = document.createElement('td'); name.className = 'domain-cell';
        var shown = (d.domain || '-').replace(/\.$/, '');
        name.textContent = shown;
        // Small screens hide the blackhole column; show it under the domain instead.
        var inline = document.createElement('div'); inline.className = 'addr-inline-sm';
        var inlineLabel = document.createElement('span'); inlineLabel.textContent = 'ECS 返回 ';
        var inlineAddr = document.createElement('s');
        inlineAddr.textContent = d.reason === 'refused' ? 'REFUSED' : ((d.blackhole_addresses || []).join(', ') || '-');
        inline.append(inlineLabel, inlineAddr);
        name.appendChild(inline);
        name.title = shown + (d.upstream ? '\n上游: ' + d.upstream : '') +
            (d.first_seen ? '\n首次发现: ' + new Date(d.first_seen).toLocaleString() : '');
        var blackhole = document.createElement('td'); blackhole.className = 'hide-sm';
        // Refused names have no blackhole addresses: show the rejection itself.
        blackhole.appendChild(d.reason === 'refused' ? makeAddressList(['REFUSED'], 'blackhole') : makeAddressList(d.blackhole_addresses, 'blackhole'));
        var fixed = document.createElement('td'); fixed.appendChild(makeAddressList(d.public_addresses, 'public'));
        var count = document.createElement('td'); count.className = 'number-cell'; count.textContent = fmt(d.count || 0);
        var seen = document.createElement('td'); seen.className = 'hide-sm dim';
        seen.textContent = relativeTime(d.last_seen); seen.title = d.last_seen ? new Date(d.last_seen).toLocaleString() : '';
        row.append(name, blackhole, fixed, count, seen); tb.appendChild(row);
    });
}

function subscriptionState(source) {
    var succeeded = source.last_success && source.last_success !== '0001-01-01T00:00:00Z';
    if (source.last_error) return source.from_cache ? '更新失败 · 使用缓存' : '更新失败';
    if (source.from_cache) return '使用缓存';
    return succeeded ? '正常' : '等待首次下载';
}

function updateMatchLists(data) {
    var wrap = document.getElementById('match-lists');
    var sources = (data && data.sources) || [];
    wrap.hidden = !sources.length;
    var tbody = document.getElementById('match-lists-tbody');
    tbody.replaceChildren();
    sources.forEach(function(source) {
        var row = document.createElement('tr');
        var succeeded = source.last_success && source.last_success !== '0001-01-01T00:00:00Z';
        [source.source || '-', fmt(source.rules || 0), subscriptionState(source),
            succeeded ? relativeTime(source.last_success) : '-'].forEach(function(value, i) {
            var cell = document.createElement('td');
            cell.textContent = value;
            if (i === 0) cell.className = 'filter-source';
            if (i === 1) cell.className = 'number-cell';
            if (i === 2 && source.last_error) { cell.className = 'error-high'; cell.title = source.last_error; }
            if (i === 3) { cell.className = 'hide-sm dim'; if (succeeded) cell.title = new Date(source.last_success).toLocaleString(); }
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
    var viewport = tb.closest('.domain-list-scroll');
    var previousScrollTop = viewport ? viewport.scrollTop : 0;
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
        emptyRow.appendChild(emptyCell); tb.appendChild(emptyRow);
        if (viewport) {
            viewport.classList.remove('expanded'); viewport.scrollTop = 0;
            var emptyButton = document.querySelector('[data-target="' + viewport.id + '"]');
            syncRankingExpandButton(emptyButton, viewport);
            if (emptyButton) emptyButton.disabled = true;
        }
        return;
    }
    list.slice(0, 50).forEach(function(domain, i) {
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
    if (viewport) {
        viewport.scrollTop = previousScrollTop;
        syncRankingExpandButton(document.querySelector('[data-target="' + viewport.id + '"]'), viewport);
    }
}

function syncRankingExpandButton(button, target) {
    if (!button || !target) return;
    var expanded = target.classList.contains('expanded');
    button.textContent = expanded ? '收起列表' : '展开全部';
    button.setAttribute('aria-expanded', expanded ? 'true' : 'false');
    button.disabled = !expanded && target.scrollHeight <= target.clientHeight + 1;
}

function initRankingExpanders() {
    document.querySelectorAll('.ranking-expand-btn').forEach(function(button) {
        var target = document.getElementById(button.getAttribute('data-target'));
        if (!target) return;
        button.addEventListener('click', function() {
            target.classList.toggle('expanded');
            syncRankingExpandButton(button, target);
            if (!target.classList.contains('expanded')) target.scrollIntoView({ block: 'nearest' });
        });
        syncRankingExpandButton(button, target);
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
        updateECSFixes(d.ecs_blackhole);
        updateTopClients(d.top_clients);
        updateDomainRanking(d.top_domains, 'top-domains-tbody', '暂无解析记录');
        updateDomainRanking(d.top_blocked_domains, 'top-blocked-domains-tbody', '暂无拦截记录');
        updateDomainRanking(d.top_private_answer_domains, 'top-private-answer-domains-tbody', '暂无私有地址应答');
        var filters = await fetch('/api/filters');
        if (filters.ok) updateFilters(await filters.json());
        var lists = await fetch('/api/match-lists');
        if (lists.ok) updateMatchLists(await lists.json());
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
    initRankingExpanders();
    load(); loadVer(); start();
    document.getElementById('theme-toggle-btn').addEventListener('click', toggleTheme);
    document.getElementById('check-update-btn').addEventListener('click', checkUpdate);
    document.getElementById('reset-stats-btn').addEventListener('click', resetStats);
    document.addEventListener('visibilitychange', function() {
        if (document.hidden) stop(); else { load(); start(); }
    });
});

window.addEventListener('beforeunload', stop);
