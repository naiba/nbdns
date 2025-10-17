// 自动刷新间隔（毫秒）
const REFRESH_INTERVAL = 3000;
let refreshTimer = null;
let countdownTimer = null;
let countdown = 0;

// 格式化数字，添加千位分隔符
function formatNumber(num) {
    return num.toString().replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

// 格式化百分比
function formatPercent(num) {
    return num.toFixed(2) + '%';
}

// 更新运行时信息
function updateRuntimeStats(runtime) {
    document.getElementById('uptime').textContent = runtime.uptime_str || '-';
    document.getElementById('goroutines').textContent = formatNumber(runtime.goroutines || 0);
    document.getElementById('mem-alloc').textContent = formatNumber(runtime.mem_alloc_mb || 0) + ' MB';
    document.getElementById('mem-sys').textContent = formatNumber(runtime.mem_sys_mb || 0) + ' MB';
    document.getElementById('mem-total').textContent = formatNumber(runtime.mem_total_mb || 0) + ' MB';
    document.getElementById('num-gc').textContent = formatNumber(runtime.num_gc || 0);
}

// 更新查询统计
function updateQueryStats(queries) {
    document.getElementById('total-queries').textContent = formatNumber(queries.total || 0);
    document.getElementById('cache-hits').textContent = formatNumber(queries.cache_hits || 0);
    document.getElementById('cache-misses').textContent = formatNumber(queries.cache_misses || 0);
    document.getElementById('failed-queries').textContent = formatNumber(queries.failed || 0);
    document.getElementById('hit-rate').textContent = formatPercent(queries.hit_rate || 0);
}

// 更新上游服务器表格
function updateUpstreamTable(upstreams) {
    const tbody = document.getElementById('upstream-tbody');

    if (!upstreams || upstreams.length === 0) {
        tbody.innerHTML = '<tr><td colspan="5" class="no-data">暂无数据</td></tr>';
        return;
    }

    let html = '';
    upstreams.forEach(upstream => {
        const errorClass = upstream.error_rate > 10 ? 'error-high' : '';
        html += `
            <tr>
                <td>${upstream.address || '-'}</td>
                <td>${formatNumber(upstream.total_queries || 0)}</td>
                <td class="${errorClass}">${formatNumber(upstream.errors || 0)}</td>
                <td class="${errorClass}">${formatPercent(upstream.error_rate || 0)}</td>
                <td>${upstream.last_used || 'Never'}</td>
            </tr>
        `;
    });
    tbody.innerHTML = html;
}

// 更新倒计时显示
function updateCountdown() {
    countdown--;
    if (countdown <= 0) {
        countdown = 0;
    }
    document.getElementById('last-update').textContent = `下次刷新: ${countdown}秒`;
}

// 重置倒计时
function resetCountdown() {
    countdown = REFRESH_INTERVAL / 1000;
    if (countdownTimer) {
        clearInterval(countdownTimer);
    }
    countdownTimer = setInterval(updateCountdown, 1000);
    updateCountdown();
}

// 加载统计数据
async function loadStats() {
    try {
        const response = await fetch('/api/stats');
        if (!response.ok) {
            throw new Error('获取统计数据失败');
        }

        const data = await response.json();

        // 更新各部分数据
        updateRuntimeStats(data.runtime);
        updateQueryStats(data.queries);
        updateUpstreamTable(data.upstreams);

        // 重置倒计时
        resetCountdown();

    } catch (error) {
        console.error('加载统计数据出错:', error);
        document.getElementById('last-update').textContent = '加载失败';
    }
}

// 启动自动刷新
function startAutoRefresh() {
    if (refreshTimer) {
        clearInterval(refreshTimer);
    }
    refreshTimer = setInterval(loadStats, REFRESH_INTERVAL);
}

// 停止自动刷新
function stopAutoRefresh() {
    if (refreshTimer) {
        clearInterval(refreshTimer);
        refreshTimer = null;
    }
    if (countdownTimer) {
        clearInterval(countdownTimer);
        countdownTimer = null;
    }
}

// 页面加载完成后初始化
document.addEventListener('DOMContentLoaded', function() {
    // 立即加载一次数据
    loadStats();

    // 启动自动刷新
    startAutoRefresh();

    // 页面可见性变化时控制刷新
    document.addEventListener('visibilitychange', function() {
        if (document.hidden) {
            stopAutoRefresh();
        } else {
            loadStats();
            startAutoRefresh();
        }
    });
});

// 页面卸载时停止刷新
window.addEventListener('beforeunload', function() {
    stopAutoRefresh();
});
