// sheet.mjs <out.png> <label:path> [label:path ...] —— 拼对照网格照。
// 真迹帧与自渲帧同框带编号字幕，判卷官/画师一眼对齐（蒸馏方案 §4.1 规矩二：
// 一轮一次多模态调用看一张图，token 可控）。零依赖：网格本身是一页 HTML，
// 复用 chromium 截帧。label 里第一个冒号前是字幕，其余全是路径。
import { execFileSync } from "node:child_process";
import { writeFileSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const [out, ...pairs] = process.argv.slice(2);
if (!out || pairs.length === 0) {
  console.error("usage: sheet.mjs <out.png> label:path [label:path ...]");
  process.exit(2);
}
const fileURL = (p) => "file:///" + resolve(p).replaceAll("\\", "/");

const cells = pairs.map((p, i) => {
  const cut = p.indexOf(":");
  const label = p.slice(0, cut);
  const img = p.slice(cut + 1);
  return `<figure><img src="${fileURL(img)}"><figcaption>${i + 1}. ${label}</figcaption></figure>`;
}).join("\n");

const cols = pairs.length <= 4 ? 2 : 3;
const rows = Math.ceil(pairs.length / cols);
const cellH = 300; // 等高 contain，网格总高可算——headless 截图只吃视口
const html = `<!doctype html><meta charset="utf-8"><style>
  body{margin:0;background:#161616;color:#eee;font:13px/1.4 sans-serif}
  .grid{display:grid;grid-template-columns:repeat(${cols},1fr);gap:6px;padding:6px}
  figure{margin:0}
  img{width:100%;height:${cellH}px;object-fit:contain;display:block;background:#000}
  figcaption{padding:3px 5px;background:#2a2a2a}
</style><div class="grid">${cells}</div>`;
const dir = mkdtempSync(join(tmpdir(), "sheet-"));
const page = join(dir, "sheet.html");
writeFileSync(page, html);
const chrome = process.env.CHROME_BIN || "chromium";
const W = Math.floor(cols * 430);
const H = rows * (cellH + 26) + 12;
execFileSync(chrome, ["--headless", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
  "--force-device-scale-factor=1", `--window-size=${W},${H}`, "--virtual-time-budget=8000",
  `--screenshot=${resolve(out)}`, "file:///" + page.replaceAll("\\", "/")], { stdio: "inherit" });
