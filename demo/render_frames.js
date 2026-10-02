#!/usr/bin/env node
// 零依赖 CDP 截帧驱动：本地静态服务 + Edge 无头 + 逐帧截图 + ffmpeg 合成 mp4。
// 契约：页面暴露 window.__READY 与 window.__renderFrame(t)（纯 f(t)，禁累积状态）。
// 用法：node render_frames.js <urlPath> <outDir> [fps] [dur] [width] [height] [--keep]
//   例：node render_frames.js iso-25d/index.html iso-25d/renders 30 12
'use strict';
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const { spawn, execFileSync } = require('node:child_process');

const DEMO_ROOT = __dirname; // 以 demo/ 为静态根，两个原型都能被 /<dir>/index.html 访问
const BROWSERS = [
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
];

const args = process.argv.slice(2);
const flags = args.filter(a => a.startsWith('--'));
const pos = args.filter(a => !a.startsWith('--'));
const urlPath = pos[0];
const outDir = path.resolve(pos[1]);
const FPS = +(pos[2] || 30);
const DUR = +(pos[3] || 12);
const W = +(pos[4] || 1920);
const H = +(pos[5] || 1080);
const KEEP = flags.includes('--keep');
if (!urlPath || !outDir) { console.error('用法: node render_frames.js <urlPath> <outDir> [fps] [dur] [w] [h] [--keep]'); process.exit(1); }

const MIME = { '.html': 'text/html', '.js': 'text/javascript', '.mjs': 'text/javascript', '.json': 'application/json', '.css': 'text/css', '.png': 'image/png', '.jpg': 'image/jpeg', '.svg': 'image/svg+xml', '.woff': 'font/woff', '.woff2': 'font/woff2' };

function startServer() {
  return new Promise(res => {
    const srv = http.createServer((req, resp) => {
      const p = path.join(DEMO_ROOT, decodeURIComponent(new URL(req.url, 'http://x').pathname));
      fs.readFile(p, (err, data) => {
        if (err) { resp.writeHead(404); resp.end('nf'); return; }
        resp.writeHead(200, { 'content-type': MIME[path.extname(p)] || 'application/octet-stream' });
        resp.end(data);
      });
    });
    srv.listen(0, '127.0.0.1', () => res({ srv, port: srv.address().port }));
  });
}

function findBrowser() { return BROWSERS.find(b => fs.existsSync(b)); }

async function launchBrowser(exe, extraFlags) {
  const port = 9333 + Math.floor(Math.random() * 40);
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'rf-profile-'));
  const flags = [
    '--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--disable-extensions', '--hide-scrollbars',
    '--force-device-scale-factor=1', `--window-size=${W},${H}`, '--allow-file-access-from-files',
    ...extraFlags, 'about:blank',
  ];
  const proc = spawn(exe, flags, { stdio: 'ignore' });
  // 等 DevTools 端口就绪
  for (let i = 0; i < 50; i++) {
    try { await fetch(`http://127.0.0.1:${port}/json/version`); return { proc, port, profile }; } catch { await sleep(200); }
  }
  throw new Error('DevTools 端口未就绪');
}

const sleep = ms => new Promise(r => setTimeout(r, ms));

class CDP {
  constructor(ws) { this.ws = ws; this.id = 0; this.pending = new Map(); this.ws.addEventListener('message', ev => {
    const m = JSON.parse(ev.data);
    if (m.id && this.pending.has(m.id)) { const { res, rej } = this.pending.get(m.id); this.pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); }
  }); }
  send(method, params = {}) {
    return new Promise((res, rej) => { const id = ++this.id; this.pending.set(id, { res, rej }); this.ws.send(JSON.stringify({ id, method, params })); });
  }
}

async function connectPage(port) {
  const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  const page = list.find(t => t.type === 'page');
  if (!page) throw new Error('无 page target');
  const ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.addEventListener('open', res); ws.addEventListener('error', rej); });
  return new CDP(ws);
}

async function renderOnce(exe, extraFlags, pageUrl, frameDir) {
  const { proc, port, profile } = await launchBrowser(exe, extraFlags);
  const total = Math.round(DUR * FPS);
  try {
    const cdp = await connectPage(port);
    await cdp.send('Page.enable');
    await cdp.send('Runtime.enable');
    await cdp.send('Emulation.setDeviceMetricsOverride', { width: W, height: H, deviceScaleFactor: 1, mobile: false });
    await cdp.send('Page.navigate', { url: pageUrl });
    // 轮询页面就绪（__READY 由页面初始化完成后置 1）
    let ready = false;
    for (let i = 0; i < 100; i++) {
      await sleep(200);
      const r = await cdp.send('Runtime.evaluate', { expression: 'window.__READY === 1', returnByValue: true });
      if (r.result.value === true) { ready = true; break; }
    }
    if (!ready) throw new Error('页面 20s 内未就绪（__READY 未置位）');
    fs.mkdirSync(frameDir, { recursive: true });
    for (let i = 0; i < total; i++) {
      const t = +(i / FPS).toFixed(4);
      const r = await cdp.send('Runtime.evaluate', { expression: `window.__renderFrame(${t})`, returnByValue: true });
      if (r.exceptionDetails) throw new Error('渲染异常 t=' + t + ': ' + JSON.stringify(r.exceptionDetails.exception?.description || r.exceptionDetails.text));
      const shot = await cdp.send('Page.captureScreenshot', { format: 'png' });
      fs.writeFileSync(path.join(frameDir, `frame_${String(i + 1).padStart(4, '0')}.png`), Buffer.from(shot.data, 'base64'));
      if ((i + 1) % 60 === 0 || i === total - 1) console.log(`  帧 ${i + 1}/${total} (t=${t}s)`);
    }
    await cdp.send('Browser.close').catch(() => {});
  } finally {
    try { proc.kill(); } catch {}
    await sleep(400);
    fs.rmSync(profile, { recursive: true, force: true });
  }
  return total;
}

async function main() {
  const exe = findBrowser();
  if (!exe) { console.error('未找到 Edge/Chrome'); process.exit(1); }
  const { srv, port } = await startServer();
  const pageUrl = `http://127.0.0.1:${port}/${urlPath}?driven=1`;
  const mp4 = path.join(outDir, path.basename(path.dirname(urlPath)) + '_demo.mp4');
  const frameDir = path.join(outDir, 'frames');
  console.log(`渲染 ${DUR}s @ ${FPS}fps = ${Math.round(DUR * FPS)} 帧 → ${outDir}`);
  let total;
  try {
    try { total = await renderOnce(exe, [], pageUrl, frameDir); }
    catch (e) { console.log('硬件 GL 失败，改用 swiftshader 软件渲染重试：', e.message); total = await renderOnce(exe, ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'], pageUrl, frameDir); }
  } finally { srv.close(); }
  console.log('ffmpeg 合成…');
  execFileSync('ffmpeg', ['-y', '-v', 'error', '-framerate', String(FPS), '-i', path.join(frameDir, 'frame_%04d.png'),
    '-c:v', 'libx264', '-crf', '18', '-preset', 'medium', '-pix_fmt', 'yuv420p', mp4], { stdio: 'inherit' });
  if (!KEEP) fs.rmSync(frameDir, { recursive: true, force: true });
  console.log('完成：' + mp4);
}

main().catch(e => { console.error(e); process.exit(1); });
