function parseMetrics(stdout) {
  var lines = stdout.split(/\r?\n/);
  var kernel = lines.length > 0 ? lines[0].trim() : "";

  var loadMatch = stdout.match(/load averages?:\s*([0-9]+(?:\.[0-9]+)?)/i);
  if (!loadMatch) {
    throw new Error("failed to parse load average from uptime output");
  }
  var load1m = parseFloat(loadMatch[1]);

  var memUsedPct = -1;
  var diskUsedPct = -1;

  for (var i = 0; i < lines.length; i++) {
    var line = lines[i].trim();
    if (line.indexOf("Mem:") === 0) {
      var memCols = line.split(/\s+/);
      if (memCols.length >= 3) {
        var total = parseFloat(memCols[1]);
        var used = parseFloat(memCols[2]);
        if (total > 0) {
          memUsedPct = Math.round((used / total) * 1000) / 10;
        }
      }
    }
    var diskMatch = line.match(/([0-9]{1,3})%\s+\/\s*$/);
    if (diskMatch) {
      diskUsedPct = parseInt(diskMatch[1], 10);
    }
  }

  if (memUsedPct < 0 || diskUsedPct < 0) {
    throw new Error("failed to parse memory or root disk usage from metrics output");
  }

  return {
    kernel: kernel,
    load_1m: load1m,
    memory_used_pct: memUsedPct,
    disk_used_pct: diskUsedPct
  };
}

function main(input) {
  var port = typeof input.port === "number" ? input.port : 22;
  var diskWarnPct = typeof input.disk_warn_pct === "number" ? input.disk_warn_pct : 85;
  var servicesList = Array.isArray(input.services) ? input.services : [];
  var portStr = String(port);

  var metricsRes = knurl.exec("ssh-system-metrics", {
    port: portStr,
    user: input.user,
    host: input.host
  });
  if (metricsRes.exit_code !== 0) {
    throw new Error(
      "ssh-system-metrics exited with code " +
        metricsRes.exit_code +
        ": " +
        (metricsRes.stderr || "").trim()
    );
  }

  var metrics = parseMetrics(metricsRes.stdout);
  var warnings = [];
  if (metrics.disk_used_pct >= diskWarnPct) {
    warnings.push(
      "root disk usage " + metrics.disk_used_pct + "% >= " + diskWarnPct + "%"
    );
  }

  var serviceResults = [];
  for (var i = 0; i < servicesList.length; i++) {
    var svc = servicesList[i];
    var svcRes = knurl.exec("ssh-service-status", {
      port: portStr,
      user: input.user,
      host: input.host,
      service: svc
    });
    var state = (svcRes.stdout || "").trim();
    if (!state) {
      state = svcRes.exit_code === 0 ? "active" : "unknown";
    }
    var active = svcRes.exit_code === 0 && state === "active";
    if (!active) {
      warnings.push("service " + svc + " is " + state);
    }
    serviceResults.push({
      name: svc,
      active: active,
      state: state
    });
  }

  var httpCheck = null;
  if (input.check_http_url) {
    var headers = {};
    if (knurl.secrets.has("VPS_HEALTH_TOKEN")) {
      headers.Authorization = "Bearer ${secret:VPS_HEALTH_TOKEN}";
    }
    var resp = knurl.http.get(input.check_http_url, {
      headers: headers,
      timeout_ms: 5000
    });
    var httpOk = resp.status >= 200 && resp.status < 300;
    if (!httpOk) {
      warnings.push("http check returned status " + resp.status);
    }
    httpCheck = {
      url: input.check_http_url,
      status: resp.status,
      ok: httpOk
    };
  }

  return {
    host: input.host,
    healthy: warnings.length === 0,
    kernel: metrics.kernel,
    load_1m: metrics.load_1m,
    memory_used_pct: metrics.memory_used_pct,
    disk_used_pct: metrics.disk_used_pct,
    services: serviceResults,
    http_check: httpCheck,
    warnings: warnings
  };
}
