'use strict';
const http = require('http');
const fs   = require('fs');
const path = require('path');
const { spawn } = require('child_process');

// ─── .env loader (fallback when not running via docker-compose) ───────────────
const envFile = path.join(__dirname, '.env');
if (fs.existsSync(envFile)) {
  fs.readFileSync(envFile, 'utf8').split('\n').forEach(line => {
    const m = line.match(/^([^#=\s][^=]*)=(.*)$/);
    if (m) {
      const k = m[1].trim();
      const v = m[2].trim().replace(/^["']|["']$/g, '');
      if (!process.env[k]) process.env[k] = v;
    }
  });
}

// ─── State ────────────────────────────────────────────────────────────────────
let sseClients = new Set();
let job = null; // { procs, runId, mode, done, total }

function broadcast(obj) {
  const msg = `data: ${JSON.stringify(obj)}\n\n`;
  sseClients.forEach(res => { try { res.write(msg); } catch (_) {} });
}

// ─── k6 Runner ────────────────────────────────────────────────────────────────
const BUCKETS = {
  classic:    'images-raw-classic',
  serverless: 'images-raw-serverless',
};

const IMAGES = {
  light:  'light-smoke.png',
  medium: 'medium-smoke.png',
  heavy:  'heavy-smoke.png',
};

function stripAnsi(str) {
  return str.replace(/\x1b\[[0-9;]*m/g, '');
}

function startJob({ vus, duration, mode, image }) {
  if (job) return { error: 'Test already running' };

  const runId    = Date.now();
  const services = mode === 'both'
    ? ['classic', 'serverless']
    : BUCKETS[mode] ? [mode] : null;
  if (!services) return { error: `Unknown test mode: ${mode}` };
  const imgFile  = `./${IMAGES[image] || 'light-smoke.png'}`;

  job = { procs: [], runId, mode, done: 0, total: services.length };
  broadcast({ type: 'start', mode, vus, duration, image, runId });

  services.forEach(svc => {
    const prefix      = `${svc}-smoke/${runId}`;
    const summaryFile = `/tmp/k6-summary-${svc}-${runId}.json`;

    const k6Env = {
      ...process.env,
      K6_VUS:            String(vus),
      K6_DURATION:       duration,
      MINIO_BUCKET_NAME: BUCKETS[svc],
      K6_OBJECT_PREFIX:  prefix,
      IMAGE_FILE:        imgFile,
    };

    const args = ['run', '--summary-export', summaryFile];
    if (process.env.PROMETHEUS_REMOTE_WRITE_URL) {
      k6Env.K6_PROMETHEUS_RW_SERVER_URL = process.env.PROMETHEUS_REMOTE_WRITE_URL;
      args.push('--out', 'experimental-prometheus-rw');
    }
    args.push('/app/loadtest.js');

    const proc = spawn('k6', args, { env: k6Env, cwd: '/app' });

    const handleOutput = (stream) => (chunk) => {
      chunk.toString().split('\n')
        .map(l => stripAnsi(l).trimEnd())
        .filter(l => l && !l.includes('--summary-export') && !l.includes('is deprecated'))
        .forEach(line => broadcast({ type: 'log', svc, line }));
    };

    proc.stdout.on('data', handleOutput('stdout'));
    proc.stderr.on('data', handleOutput('stderr'));

    proc.on('exit', (code) => {
      let summary = null;
      try { summary = JSON.parse(fs.readFileSync(summaryFile, 'utf8')); } catch (_) {}
      broadcast({ type: 'svcDone', svc, code, summary });

      job.done++;
      if (job.done >= job.total) {
        broadcast({ type: 'done', runId });
        job = null;
      }
    });

    job.procs.push(proc);
  });

  return { ok: true, runId };
}

function stopJob() {
  if (!job) return { error: 'No test running' };
  job.procs.forEach(p => { try { p.kill('SIGTERM'); } catch (_) {} });
  return { ok: true };
}

// ─── HTTP Server ──────────────────────────────────────────────────────────────
const INDEX_HTML = fs.readFileSync(path.join(__dirname, 'public', 'index.html'));

function parseBody(req) {
  return new Promise((resolve, reject) => {
    let data = '';
    req.on('data', c => { data += c; });
    req.on('end', () => { try { resolve(JSON.parse(data || '{}')); } catch (e) { reject(e); } });
  });
}

const server = http.createServer(async (req, res) => {
  const url = req.url.split('?')[0];

  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Access-Control-Allow-Methods', 'GET, POST, OPTIONS');
  res.setHeader('Access-Control-Allow-Headers', 'Content-Type');
  if (req.method === 'OPTIONS') { res.writeHead(204); return res.end(); }

  // ── Static ──
  if (req.method === 'GET' && (url === '/' || url === '/index.html')) {
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    return res.end(INDEX_HTML);
  }

  // ── SSE stream ──
  if (req.method === 'GET' && url === '/events') {
    res.writeHead(200, {
      'Content-Type':  'text/event-stream',
      'Cache-Control': 'no-cache',
      'Connection':    'keep-alive',
    });
    // Send current state immediately
    res.write(`data: ${JSON.stringify({ type: 'connected', running: !!job })}\n\n`);
    sseClients.add(res);
    req.on('close', () => sseClients.delete(res));
    // Keepalive ping every 25s
    const ping = setInterval(() => { try { res.write(': ping\n\n'); } catch (_) { clearInterval(ping); } }, 25000);
    return;
  }

  // ── API ──
  if (req.method === 'POST' && url === '/start') {
    try {
      const cfg    = await parseBody(req);
      const result = startJob(cfg);
      res.writeHead(200, { 'Content-Type': 'application/json' });
      return res.end(JSON.stringify(result));
    } catch (e) {
      res.writeHead(400, { 'Content-Type': 'application/json' });
      return res.end(JSON.stringify({ error: e.message }));
    }
  }

  if (req.method === 'POST' && url === '/stop') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    return res.end(JSON.stringify(stopJob()));
  }

  if (req.method === 'GET' && url === '/status') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    return res.end(JSON.stringify({ running: !!job, runId: job?.runId }));
  }

  res.writeHead(404);
  res.end('not found');
});

// ─── Graceful shutdown ────────────────────────────────────────────────────────
['SIGTERM', 'SIGINT'].forEach(sig => process.on(sig, () => {
  if (job) job.procs.forEach(p => { try { p.kill('SIGTERM'); } catch (_) {} });
  server.close(() => process.exit(0));
}));

const PORT = process.env.UI_PORT || 3000;
server.listen(PORT, '0.0.0.0', () => {
  console.log(`k6 UI ready → http://localhost:${PORT}`);
});
