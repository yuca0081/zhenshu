// licenses.mjs —— 列出 node_modules 实装依赖的 name/version/license（JSON 一行）。
// 毕业检查③的机械对账面：画师在 kit.json 里自报的 deps 与实装清单对不上算不过。
// 零依赖；没有 node_modules（零依赖画材包）输出 []。
import { readdirSync, readFileSync, existsSync } from "node:fs";

const out = [];
if (existsSync("node_modules")) {
  for (const e of readdirSync("node_modules", { withFileTypes: true })) {
    if (!e.isDirectory() || e.name.startsWith(".")) continue;
    const dirs = e.name.startsWith("@")
      ? readdirSync(`node_modules/${e.name}`, { withFileTypes: true })
          .filter((d) => d.isDirectory() && !d.name.startsWith("."))
          .map((d) => `${e.name}/${d.name}`)
      : [e.name];
    for (const d of dirs) {
      try {
        const p = JSON.parse(readFileSync(`node_modules/${d}/package.json`, "utf8"));
        out.push({ name: p.name, version: p.version || "", license: p.license || "" });
      } catch { /* 半截安装的包不算数 */ }
    }
  }
}
console.log(JSON.stringify(out));
