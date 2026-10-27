// measure.mjs <img.png> [img.png ...] —— 判卷像素实测（kit 规范 §5：判卷结论的
// 关键事实须用程序化测量裁决，多模态会 confidently 误判）。
// 输出 stdout 一行 JSON：每张图的尺寸 / 亮度 p5·p50·p95 / 唯一色数（采样）/
// 四边亮度均值（黑边检测）。原理：生成加载这批图的页面，canvas 采样后把 JSON
// 写进 <title>，chromium --dump-dom 捕获（--allow-file-access-from-files 防
// file:// 图污染 canvas）。任何一张加载失败会以 err 字段带出而不是整个崩掉。
import { execFileSync } from "node:child_process";
import { writeFileSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const imgs = process.argv.slice(2);
if (imgs.length === 0) {
  console.error("usage: measure.mjs <img.png> [img.png ...]");
  process.exit(2);
}
const fileURL = (p) => "file:///" + resolve(p).replaceAll("\\", "/");
const page = `<!doctype html><meta charset="utf-8"><canvas id="c"></canvas><script>
const IMGS = ${JSON.stringify(imgs.map((p) => fileURL(p)))};
(async () => {
  const out = [];
  const cv = document.getElementById("c");
  const cx = cv.getContext("2d", { willReadFrequently: true });
  for (const src of IMGS) {
    const item = { src };
    try {
      const img = await new Promise((ok, bad) => {
        const el = new Image(); el.onload = () => ok(el); el.onerror = () => bad(new Error("load"));
        el.src = src;
      });
      const w = img.naturalWidth, h = img.naturalHeight;
      cv.width = w; cv.height = h;
      cx.drawImage(img, 0, 0);
      const d = cx.getImageData(0, 0, w, h).data;
      const lum = []; const colors = new Set(); let top = 0, bot = 0, left = 0, right = 0;
      let nTop = 0, nBot = 0, nLeft = 0, nRight = 0;
      const band = Math.max(2, Math.floor(Math.min(w, h) * 0.03));
      for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) {
        const i = (y * w + x) * 4, r = d[i], g = d[i + 1], b = d[i + 2];
        const L = 0.299 * r + 0.587 * g + 0.114 * b;
        lum.push(L);
        if ((x * 7 + y * 13) % 9 === 0 && colors.size < 50000) colors.add((r << 16) | (g << 8) | b);
        if (y < band) { top += L; nTop++; } else if (y >= h - band) { bot += L; nBot++; }
        if (x < band) { left += L; nLeft++; } else if (x >= w - band) { right += L; nRight++; }
      }
      lum.sort((a, b) => a - b);
      const q = (p) => Math.round(lum[Math.min(lum.length - 1, Math.floor(lum.length * p))]);
      item.w = w; item.h = h;
      item.lum = { p5: q(0.05), p50: q(0.5), p95: q(0.95) };
      item.uniqueColors = colors.size;
      item.edges = { top: Math.round(top / nTop), bottom: Math.round(bot / nBot), left: Math.round(left / nLeft), right: Math.round(right / nRight) };
    } catch (e) { item.err = String(e); }
    out.push(item);
  }
  document.title = "M:" + JSON.stringify(out);
})();
</script>`;
const dir = mkdtempSync(join(tmpdir(), "measure-"));
const htmlPath = join(dir, "measure.html");
writeFileSync(htmlPath, page);
const chrome = process.env.CHROME_BIN || "chromium";
const dom = execFileSync(chrome, ["--headless", "--no-sandbox", "--disable-gpu",
  "--allow-file-access-from-files", "--virtual-time-budget=10000", "--dump-dom",
  "file:///" + htmlPath.replaceAll("\\", "/")], { encoding: "utf8", maxBuffer: 8 * 1024 * 1024 });
const m = dom.match(/<title>M:([\s\S]*?)<\/title>/);
if (!m) { console.error("measure: 页面没有回写结果（图片加载失败？）"); process.exit(3); }
console.log(m[1]);
