var REFRESH = 3000;
var REPO_URL = 'https://github.com/naiba/nbdns';
var LIST_PREVIEW = 10; // Rows shown before "显示全部"
var refreshTimer, countdownTimer, countdown = 0;
var checkingUpdate = false, resetting = false, diagnosing = false;
var clientNames = {};
var recentBlocked = 0;
var expandedLists = {};

var THEME_CYCLE = ['auto', 'light', 'dark'];
var THEME_ICON = { auto: '◐', light: '☀', dark: '☾' };

function applyTheme(mode) {
    if (mode === 'auto') document.documentElement.removeAttribute('data-theme');
    else document.documentElement.setAttribute('data-theme', mode);
    var btn = document.getElementById('theme-toggle-btn');
    if (btn) btn.textContent = THEME_ICON[mode];
}

function initTheme() {
    applyTheme(localStorage.getItem('nbdns-theme') || 'auto');
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', function() {
        if ((localStorage.getItem('nbdns-theme') || 'auto') === 'auto') applyTheme('auto');
    });
}

function toggleTheme() {
    var cur = localStorage.getItem('nbdns-theme') || 'auto';
    var next = THEME_CYCLE[(THEME_CYCLE.indexOf(cur) + 1) % THEME_CYCLE.length];
    if (next === 'auto') localStorage.removeItem('nbdns-theme');
    else localStorage.setItem('nbdns-theme', next);
    applyTheme(next);
}

function fmt(n) { return (n || 0).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ','); }
function pct(n) { return (n || 0).toFixed(1) + '%'; }
function ratio(part, total) { return total > 0 ? part / total * 100 : 0; }
function setText(id, value) { document.getElementById(id).textContent = value; }
function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
}
function emptyRow(tbody, columns, text) {
    var row = el('tr'); var cell = el('td', 'empty', text);
    cell.colSpan = columns; row.appendChild(cell); tbody.appendChild(row);
}
function validTime(iso) { var t = Date.parse(iso); return iso && !isNaN(t) && t > 0; }
function localTime(iso) { return validTime(iso) ? new Date(iso).toLocaleString() : ''; }

function relativeTime(iso) {
    if (!validTime(iso)) return '-';
    var sec = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 1000));
    if (sec < 60) return '刚刚';
    if (sec < 3600) return Math.floor(sec / 60) + ' 分钟前';
    if (sec < 86400) return Math.floor(sec / 3600) + ' 小时前';
    return Math.floor(sec / 86400) + ' 天前';
}

function ms(value) {
    if (value < 0) return '> 5 s'; // LatencyOverflow: above the largest histogram bound.
    if (!value) return '-';
    return value >= 100 ? Math.round(value) + ' ms' : value.toFixed(1) + ' ms';
}

// "tcp-tls://dns.example:853" -> "dns.example"; labels stay short in tables.
function hostOf(address) {
    var match = /^[a-z+-]+:\/\/\[?([^\]\/:]+)/i.exec(address || '');
    return match ? match[1] : address;
}

// Client addresses come from ECS or the socket; names from client_name_server PTR lookups.
function isLoopback(address) { return /^127\./.test(address) || address === '::1'; }
function clientLabel(address) {
    if (!address) return '-';
    if (clientNames[address]) return clientNames[address];
    if (isLoopback(address)) return '本机';
    return address;
}
function clientTitle(address) {
    var label = clientLabel(address);
    return label === address ? address : label + '（' + address + '）';
}

// Long lists show LIST_PREVIEW rows; the button below toggles the rest and the
// choice survives the periodic refresh.
function applyRowLimit(tbodyId, total) {
    var tbody = document.getElementById(tbodyId);
    var button = document.querySelector('.more-btn[data-list="' + tbodyId + '"]');
    var expanded = !!expandedLists[tbodyId];
    tbody.classList.toggle('expanded', expanded);
    Array.prototype.forEach.call(tbody.rows, function(row, i) { row.classList.toggle('is-extra', i >= LIST_PREVIEW); });
    if (!button) return;
    button.hidden = total <= LIST_PREVIEW;
    button.textContent = expanded ? '收起' : '显示全部 ' + total + ' 条';
    button.setAttribute('aria-expanded', expanded ? 'true' : 'false');
}

function initMoreButtons() {
    document.querySelectorAll('.more-btn').forEach(function(button) {
        button.addEventListener('click', function() {
            var id = button.getAttribute('data-list');
            expandedLists[id] = !expandedLists[id];
            applyRowLimit(id, document.getElementById(id).rows.length);
            if (!expandedLists[id]) button.closest('.card').scrollIntoView({ block: 'nearest' });
        });
    });
}

function updateRuntime(r) {
    setText('uptime', r.uptime_str || '-');
    setText('goroutines', fmt(r.goroutines));
    setText('mem-alloc', fmt(r.mem_alloc_mb) + ' MB');
    setText('mem-sys', fmt(r.mem_sys_mb) + ' MB');
    setText('num-gc', fmt(r.num_gc));
    setText('stats-duration', r.stats_duration_str ? '累计 ' + r.stats_duration_str : '累计');
}

function updateLifetime(q) {
    setText('total-queries', fmt(q.total));
    setText('hit-rate', pct(q.hit_rate));
    setText('blocked-queries', fmt(q.blocked));
    setText('failed-queries', fmt(q.failed));
    setText('prefetched-queries', fmt(q.prefetched));
    setText('doh-queries', fmt(q.doh));
}

function updateRecent(r) {
    r = r || { hours: [] };
    recentBlocked = r.blocked || 0;
    setText('recent-queries', fmt(r.queries));
    setText('recent-hit-rate', pct(r.hit_rate));
    setText('recent-cache', fmt(r.cache_hits) + ' / ' + fmt(r.cache_misses));
    setText('recent-blocked', fmt(r.blocked));
    setText('recent-prefetched', fmt(r.prefetched));
    var failed = document.getElementById('recent-failed');
    failed.replaceChildren(document.createTextNode(fmt(r.failed) + ' '), el('span', 'muted', pct(ratio(r.failed, r.queries))));
    failed.classList.toggle('bad', (r.failed || 0) > 0);
    drawTrend(r.hours || []);
}

var SVG_NS = 'http://www.w3.org/2000/svg';
function svgRect(x, y, w, h, className) {
    var rect = document.createElementNS(SVG_NS, 'rect');
    rect.setAttribute('x', x); rect.setAttribute('y', y);
    rect.setAttribute('width', w); rect.setAttribute('height', Math.max(h, 0));
    rect.setAttribute('class', className);
    return rect;
}

function drawTrend(hours) {
    var svg = document.getElementById('trend-chart');
    var axis = document.getElementById('trend-axis');
    svg.replaceChildren(); axis.replaceChildren();
    var width = 720, height = 96, slot = width / Math.max(hours.length, 1), bar = slot * 0.7;
    var peak = hours.reduce(function(m, h) { return Math.max(m, h.queries || 0); }, 0);
    var scale = peak > 0 ? (height - 2) / peak : 0;
    hours.forEach(function(h, i) {
        var x = i * slot + (slot - bar) / 2;
        var group = document.createElementNS(SVG_NS, 'g');
        var title = document.createElementNS(SVG_NS, 'title');
        var start = new Date(h.start);
        title.textContent = start.toLocaleString([], { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) +
            '\n查询 ' + fmt(h.queries) + '，命中 ' + fmt(h.cache_hits) + '，回源 ' + fmt(h.cache_misses) +
            '\n拦截 ' + fmt(h.blocked) + '，失败 ' + fmt(h.failed) + '，预取 ' + fmt(h.prefetched);
        group.appendChild(title);
        // Hover target spans the whole slot so empty hours still show their tooltip.
        group.appendChild(svgRect(i * slot, 0, slot, height, 'trend-hit'));
        var total = (h.queries || 0) * scale, blocked = (h.blocked || 0) * scale, failed = (h.failed || 0) * scale;
        group.appendChild(svgRect(x, height - total, bar, total, 'trend-queries'));
        group.appendChild(svgRect(x, height - blocked, bar, blocked, 'trend-blocked'));
        group.appendChild(svgRect(x, height - blocked - failed, bar, failed, 'trend-failed'));
        svg.appendChild(group);
        if (i % 6 === 0 || i === hours.length - 1) {
            var label = el('span', '', i === hours.length - 1 ? '现在' : String(start.getHours()).padStart(2, '0') + ':00');
            label.style.left = (i * slot + slot / 2) / width * 100 + '%';
            axis.appendChild(label);
        }
    });
}

function updateUpstream(list) {
    var tb = document.getElementById('upstream-tbody');
    tb.replaceChildren();
    if (!list || !list.length) { emptyRow(tb, 6, '暂无数据'); return; }
    list.forEach(function(u) {
        var row = el('tr');
        var queries = el('td', 'num', fmt(u.recent_queries));
        queries.title = '累计 ' + fmt(u.total_queries) + ' 次';
        var errors = el('td', 'num' + (u.recent_error_rate > 10 ? ' error-high' : ''), u.recent_queries ? pct(u.recent_error_rate) : '-');
        errors.title = '近 24 小时错误 ' + fmt(u.recent_errors) + ' 次\n累计错误 ' + fmt(u.errors) + ' 次（' + pct(u.error_rate) + '）';
        var p95 = el('td', 'num' + (u.p95_latency_ms < 0 || u.p95_latency_ms > 1000 ? ' error-high' : ''), ms(u.p95_latency_ms));
        var used = el('td', 'hide-sm muted', u.last_used ? relativeTime(u.last_used) : '从未');
        used.title = localTime(u.last_used);
        row.append(el('td', 'source-cell mono', u.address || '-'), queries, errors, el('td', 'num hide-sm', ms(u.avg_latency_ms)), p95, used);
        tb.appendChild(row);
    });
}

function subscriptionState(source) {
    if (source.last_error) return source.from_cache ? '更新失败，使用缓存' : '更新失败';
    if (source.from_cache) return '使用缓存';
    return validTime(source.last_success) ? '正常' : '等待首次下载';
}

function subscriptionRow(source, columns) {
    var row = el('tr');
    var state = el('td', source.last_error ? 'error-high' : '', subscriptionState(source));
    if (source.last_error) state.title = source.last_error;
    var success = el('td', 'hide-sm muted', validTime(source.last_success) ? relativeTime(source.last_success) : '-');
    success.title = localTime(source.last_success);
    row.appendChild(el('td', 'source-cell mono', source.source || '-'));
    columns.forEach(function(value) { row.appendChild(el('td', 'num', fmt(value))); });
    row.append(state, success);
    return row;
}

function updateFilters(data) {
    var sources = data.sources || [];
    var summary = fmt(data.rules) + ' 条规则';
    if (data.unsupported) summary += '（' + fmt(data.unsupported) + ' 条不支持）';
    setText('filter-summary', sources.length ? summary + '，近 24 小时拦截 ' + fmt(recentBlocked) : '');
    document.getElementById('filter-sources-wrap').hidden = !sources.length;
    document.getElementById('filter-empty').hidden = !!sources.length;
    var tbody = document.getElementById('filter-sources');
    tbody.replaceChildren();
    sources.forEach(function(source) { tbody.appendChild(subscriptionRow(source, [source.rules, source.unsupported])); });
}

function updateMatchLists(data) {
    var sources = (data && data.sources) || [];
    document.getElementById('match-lists').hidden = !sources.length;
    var tbody = document.getElementById('match-lists-tbody');
    tbody.replaceChildren();
    sources.forEach(function(source) { tbody.appendChild(subscriptionRow(source, [source.rules])); });
}

var ECS_FIX_STATUS = {
    enabled: { label: '已开启', empty: '暂未发现被 ECS 屏蔽的域名' },
    disabled: { label: '已关闭', empty: '自动修复已关闭；删除配置中的 "ecs_blackhole_fallback": false 即可开启' },
    no_ecs: { label: '未配置 ECS', empty: '未向上游发送 ECS，无需修复' }
};

function makeAddressList(addresses, kind, emptyText, emptyTitle) {
    var wrap = el('div', 'addr-list');
    var list = addresses || [];
    if (!list.length) {
        var none = el('span', 'muted', emptyText || '-');
        if (emptyTitle) none.title = emptyTitle;
        wrap.appendChild(none); return wrap;
    }
    list.slice(0, 3).forEach(function(address) { wrap.appendChild(el('span', 'addr ' + kind, address)); });
    if (list.length > 3) {
        var more = el('span', 'addr more', '+' + (list.length - 3));
        more.title = list.slice(3).join('\n');
        wrap.appendChild(more);
    }
    return wrap;
}

function updateECSFixes(e) {
    e = e || {};
    var info = ECS_FIX_STATUS[e.status] || { label: '', empty: '暂无修复记录' };
    var status = document.getElementById('ecs-fix-status');
    status.textContent = info.label; status.setAttribute('data-status', e.status || '');
    var domains = e.domains || [];
    var summary = document.getElementById('ecs-fix-summary');
    summary.textContent = domains.length ? '累计修复 ' + fmt(e.fixed) + ' 次，最近 ' + relativeTime(domains[0].last_seen) : '';
    summary.title = domains.length ? '列表保留最近修复的 ' + domains.length + ' 个域名；次数不含缓存命中' : '';

    var tb = document.getElementById('ecs-fix-tbody');
    tb.replaceChildren();
    domains.forEach(function(d) {
        var row = el('tr');
        var shown = (d.domain || '-').replace(/\.$/, '');
        var name = el('td', '', shown);
        // Small screens hide the blackhole column; show it under the domain instead.
        name.appendChild(el('div', 'addr-inline-sm', d.reason === 'refused' ? 'REFUSED' : (d.blackhole_addresses || []).join(', ')));
        name.title = shown + (d.upstream ? '\n上游：' + d.upstream : '') + (d.first_seen ? '\n首次发现：' + localTime(d.first_seen) : '');
        var blackhole = el('td', 'hide-sm');
        // Refused names have no blackhole addresses: show the rejection itself.
        blackhole.appendChild(makeAddressList(d.reason === 'refused' ? ['REFUSED'] : d.blackhole_addresses, 'blackhole'));
        // HTTPS/AAAA repairs often succeed with no records (NODATA): name the query
        // types so an empty cell is not mistaken for a failed repair.
        var types = d.query_types || [];
        var fixed = el('td');
        fixed.appendChild(makeAddressList(d.public_addresses, 'public',
            types.length ? '无地址（' + types.join('、') + '）' : '无地址',
            '去掉 ECS 后上游正常应答，但这类查询没有地址记录（例如 HTTPS 记录不存在），客户端收到正常的空应答而不是 SERVFAIL'));
        if (types.length) fixed.title = '修复过的查询类型：' + types.join('、');
        var seen = el('td', 'hide-sm muted', relativeTime(d.last_seen)); seen.title = localTime(d.last_seen);
        row.append(name, blackhole, fixed, el('td', 'num', fmt(d.count)), seen);
        tb.appendChild(row);
    });
    if (!domains.length) emptyRow(tb, 5, info.empty);
    applyRowLimit('ecs-fix-tbody', domains.length);
}

function updateTopClients(list) {
    var ol = document.getElementById('top-clients-list');
    ol.replaceChildren();
    if (!list || !list.length) { ol.appendChild(el('li', 'empty', '暂无数据')); return; }
    list.slice(0, 10).forEach(function(c, i) {
        var item = el('li');
        var name = el('span', 'client-name', clientLabel(c.key)); name.title = clientTitle(c.key);
        item.append(el('span', 'rank-no', String(i + 1)), name, el('span', 'client-total', fmt(c.count)));
        ol.appendChild(item);
    });
}

function clientListFor(domain) {
    if (Array.isArray(domain.top_clients) && domain.top_clients.length) return domain.top_clients.slice(0, 10);
    if (domain.top_client) return [{ client: domain.top_client, count: domain.top_client_count || 0 }];
    return [];
}

function makeClientBreakdown(domain, wasOpen) {
    var clients = clientListFor(domain);
    if (!clients.length) return el('span', 'muted', '-');
    var primary = el('span', 'client-primary', clientLabel(clients[0].client)); primary.title = clientTitle(clients[0].client);
    var count = el('span', 'client-count', fmt(clients[0].count));
    if (clients.length === 1) {
        var single = el('div', 'client-summary single'); single.append(primary, count); return single;
    }
    var details = el('details', 'client-breakdown'); details.open = wasOpen;
    var summary = el('summary', 'client-summary');
    summary.title = '查看全部客户端';
    summary.append(primary, count, el('span', 'client-more', '+' + (clients.length - 1)));
    var list = el('ol', 'client-detail');
    clients.forEach(function(item) {
        var row = el('li');
        var name = el('span', '', clientLabel(item.client)); name.title = clientTitle(item.client);
        row.append(name, el('b', '', fmt(item.count)));
        list.appendChild(row);
    });
    details.append(summary, list);
    return details;
}

function failureReasonText(reason) {
    if (reason === 'overload') return '并发已满';
    if (reason === 'no_upstream') return '无上游';
    if (reason === 'cached_failure') return '退避中';
    return reason.split(', ').map(hostOf).join('、') + ' 失败';
}

function failureReasonTitle(reason) {
    if (reason === 'overload') return '达到 max_concurrent_queries，未发往上游';
    if (reason === 'no_upstream') return '没有可用的上游';
    if (reason === 'cached_failure') return '上游刚失败过，退避期内未重试（RFC 9520）';
    return '上游均失败：' + reason;
}

function makeFailureReasons(domain) {
    var reasons = domain.reasons || [];
    var note = el('span', 'domain-note');
    note.textContent = reasons.map(function(r) {
        return failureReasonText(r.reason) + (reasons.length > 1 ? ' ' + fmt(r.count) : '');
    }).join(' · ');
    note.title = reasons.map(function(r) { return failureReasonTitle(r.reason) + '：' + fmt(r.count) + ' 次'; }).join('\n');
    return note;
}

function updateDomainRanking(list, tbodyId, emptyText, withReasons) {
    var tb = document.getElementById(tbodyId);
    var openDomains = {};
    Array.prototype.forEach.call(tb.querySelectorAll('tr[data-domain]'), function(row) {
        if (row.querySelector('details[open]')) openDomains[row.getAttribute('data-domain')] = true;
    });
    tb.replaceChildren();
    (list || []).slice(0, 50).forEach(function(domain, i) {
        var row = el('tr');
        var domainName = domain.key || '-';
        row.setAttribute('data-domain', domainName);
        var name = el('td');
        name.appendChild(el('span', 'domain-text', domainName.replace(/\.$/, '')));
        name.title = domainName;
        if (withReasons) name.appendChild(makeFailureReasons(domain));
        var clients = el('td', 'client-cell');
        clients.appendChild(makeClientBreakdown(domain, !!openDomains[domainName]));
        row.append(el('td', 'rank', String(i + 1)), name, el('td', 'num', fmt(domain.count)), clients);
        tb.appendChild(row);
    });
    if (!tb.rows.length) emptyRow(tb, 4, emptyText);
    applyRowLimit(tbodyId, (list || []).slice(0, 50).length);
}

var SOURCE_LABEL = { custom: '自定义记录', cache: '缓存' };

// servedSource mirrors HandleDnsMsg precedence: custom records, then filter
// rules, then a fresh cache entry, then the fastest valid upstream answer.
function servedSource(result) {
    var sources = result.sources || [];
    var custom = sources.filter(function(s) { return s.kind === 'custom'; })[0];
    if (custom) return custom;
    if (result.blocked) return null;
    var cache = sources.filter(function(s) { return s.kind === 'cache' && s.ttl_seconds > 0; })[0];
    if (cache) return cache;
    var valid = sources.filter(function(s) { return s.kind === 'upstream' && !s.error && (s.rcode === 'NOERROR' || s.rcode === 'NXDOMAIN'); });
    valid.sort(function(a, b) { return a.latency_ms - b.latency_ms; });
    return valid[0] || null;
}

function renderDiagnosis(result) {
    var box = document.getElementById('diagnose-result');
    box.replaceChildren(); box.hidden = false;
    var summary = el('ul', 'diagnose-summary');
    summary.appendChild(el('li', '', result.route === 'dedicated' ? '命中 match / match_lists，交给专用上游' : '交给默认上游'));
    var sources = result.sources || [];
    if (sources.some(function(s) { return s.kind === 'custom'; })) summary.appendChild(el('li', '', '有自定义记录：直接返回，不查询上游，也不受拦截规则影响'));
    else if (result.blocked) summary.appendChild(el('li', 'bad', '命中拦截规则：客户端收到 NXDOMAIN'));
    var served = servedSource(result);
    if (served) {
        var answer = served.answers && served.answers.length ? served.answers.join('，') : (served.rcode || '空应答');
        summary.appendChild(el('li', '', '客户端收到（' + (SOURCE_LABEL[served.kind] || hostOf(served.address)) + '）：' + answer));
        if (served.rebind_blocked) {
            summary.appendChild(el('li', 'bad', '含内网/保留地址：经开启防重绑定的 dnsmasq 转发时会被丢弃，需要时把域名加入 rebind_domain'));
        }
    } else if (!result.blocked) {
        summary.appendChild(el('li', 'bad', '没有上游返回有效应答：客户端收到 SERVFAIL（有过期缓存时返回旧应答）'));
    }
    box.appendChild(summary);

    var wrap = el('div', 'tbl-wrap');
    var table = el('table', 'diagnose-table');
    var head = el('tr');
    [['来源', ''], ['结果', ''], ['应答', ''], ['耗时 / TTL', 'num'], ['dnsmasq', 'hide-sm']].forEach(function(h) { head.appendChild(el('th', h[1], h[0])); });
    var thead = el('thead'); thead.appendChild(head);
    var tbody = el('tbody');
    sources.forEach(function(s) {
        var row = el('tr');
        var label = el('td', '', SOURCE_LABEL[s.kind] || hostOf(s.address) || '-');
        if (s.address) label.title = s.address;
        var failed = s.error || (s.rcode && s.rcode !== 'NOERROR' && s.rcode !== 'NXDOMAIN');
        var outcome = el('td', failed ? 'error-high' : '', s.error ? (/deadline|timeout/i.test(s.error) ? '超时' : '错误') : (s.rcode || '-'));
        if (s.error) outcome.title = s.error;
        var answers = el('td');
        if (s.error) answers.appendChild(el('span', 'muted', '-'));
        else answers.appendChild(makeAddressList(s.answers, 'public', '无记录'));
        var timing = el('td', 'num');
        if (s.kind === 'upstream') timing.textContent = ms(s.latency_ms);
        else if (s.kind === 'cache') timing.textContent = s.ttl_seconds > 0 ? s.ttl_seconds + ' s' : '已过期';
        else timing.textContent = '-';
        var rebind = el('td', 'hide-sm ' + (s.rebind_blocked ? 'error-high' : 'muted'), s.rebind_blocked ? '会被丢弃' : '正常');
        row.append(label, outcome, answers, timing, rebind);
        tbody.appendChild(row);
    });
    if (!sources.length) emptyRow(tbody, 5, '没有可查询的上游');
    table.append(thead, tbody); wrap.appendChild(table); box.appendChild(wrap);
}

async function diagnose(event) {
    event.preventDefault();
    if (diagnosing) return;
    var name = document.getElementById('diagnose-name').value.trim();
    if (!name) return;
    var type = document.getElementById('diagnose-type').value;
    var btn = document.getElementById('diagnose-btn');
    var box = document.getElementById('diagnose-result');
    try {
        diagnosing = true; btn.disabled = true; btn.textContent = '诊断中…';
        var r = await fetch('/api/diagnose?name=' + encodeURIComponent(name) + '&type=' + encodeURIComponent(type));
        if (!r.ok) {
            box.hidden = false; box.replaceChildren(el('p', 'error-high', r.status === 400 ? '域名或记录类型无效' : '诊断失败：' + (await r.text())));
            return;
        }
        renderDiagnosis(await r.json());
    } catch (e) {
        box.hidden = false; box.replaceChildren(el('p', 'error-high', '诊断失败'));
    } finally { diagnosing = false; btn.disabled = false; btn.textContent = '诊断'; }
}

function tick() {
    countdown = Math.max(0, countdown - 1);
    setText('last-update', countdown + 's');
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
        clientNames = d.client_names || {};
        updateRuntime(d.runtime);
        updateLifetime(d.queries);
        updateRecent(d.recent);
        updateUpstream(d.upstreams);
        updateECSFixes(d.ecs_blackhole);
        updateTopClients(d.top_clients);
        updateDomainRanking(d.top_domains, 'top-domains-tbody', '暂无解析记录');
        updateDomainRanking(d.top_blocked_domains, 'top-blocked-domains-tbody', '暂无拦截记录');
        updateDomainRanking(d.top_failed_domains, 'top-failed-domains-tbody', '暂无失败记录', true);
        updateDomainRanking(d.top_private_answer_domains, 'top-private-answer-domains-tbody', '暂无私有地址应答');
        var filters = await fetch('/api/filters');
        if (filters.ok) updateFilters(await filters.json());
        var lists = await fetch('/api/match-lists');
        if (lists.ok) updateMatchLists(await lists.json());
        resetCD();
    } catch (e) {
        setText('last-update', '失败');
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
        var link = document.getElementById('version-display');
        link.textContent = 'v' + d.version;
        if (d.version && d.version !== '0.0.0') link.href = REPO_URL + '/releases/tag/v' + encodeURIComponent(d.version);
    } catch (e) {}
}

async function checkUpdate() {
    if (checkingUpdate) return;
    var btn = document.getElementById('check-update-btn');
    try {
        checkingUpdate = true; btn.disabled = true; btn.textContent = '检查中…';
        var r = await fetch('/api/check-update', { method: 'POST' });
        if (!r.ok) throw 0;
        var d = await r.json();
        alert(d.has_update
            ? d.message + '\n当前: v' + d.current_version + '\n最新: v' + d.latest_version
            : d.message + '\n当前: v' + d.current_version);
    } catch (e) { alert('检查失败'); }
    finally { checkingUpdate = false; btn.disabled = false; btn.textContent = '检查更新'; }
}

async function resetStats() {
    if (resetting) return;
    if (!confirm('清空全部统计（累计、近 24 小时和所有排行）？')) return;
    var btn = document.getElementById('reset-stats-btn');
    try {
        resetting = true; btn.disabled = true; btn.textContent = '重置中…';
        var r = await fetch('/api/stats/reset', { method: 'POST' });
        if (!r.ok) throw 0;
        var d = await r.json();
        if (d.success) await load();
        else alert('失败: ' + (d.message || ''));
    } catch (e) { alert('重置失败'); }
    finally { resetting = false; btn.disabled = false; btn.textContent = '重置'; }
}

document.addEventListener('DOMContentLoaded', function() {
    initTheme();
    initMoreButtons();
    load(); loadVer(); start();
    document.getElementById('theme-toggle-btn').addEventListener('click', toggleTheme);
    document.getElementById('check-update-btn').addEventListener('click', checkUpdate);
    document.getElementById('reset-stats-btn').addEventListener('click', resetStats);
    document.getElementById('diagnose-form').addEventListener('submit', diagnose);
    document.addEventListener('visibilitychange', function() {
        if (document.hidden) stop(); else { load(); start(); }
    });
});

window.addEventListener('beforeunload', stop);
