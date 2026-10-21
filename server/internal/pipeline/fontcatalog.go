package pipeline

// 目录字体：设计发明可以点名的开源展示字体（全 OFL，可自由分发）。
// URL 锁 google/fonts 仓库 commit（确定性）；下载与子集化由 ai/font_tool.py 代劳
// （vend 同款铁律：模型只有申请权，执行永远在平台侧），全字库缓存在
// data/library/vendor/fonts/，子集产物以 data URL 进各合成物的 @font-face。
// 为什么要目录：设计会发明渲染端没有的字体名（PingFang/站酷/思源…），要么钳掉
// （所有片子一个雅黑脸），要么给一份合法可下载的菜单让它点名——这是后者。

import (
	"encoding/json"
	"os"
	"strings"
)

type CatalogFont struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Family  string `json:"family"` // CSS family 规范写法（声明与用法逐字一致）
	Repo    string `json:"repo"`   // google/fonts 仓库内路径 ofl/<dir>/<file>
	License string `json:"license"`
	// URL 非空 = 非 google/fonts 源的直连下载（如 GitHub release 资产走
	// api.github.com assets 端点——本部署唯一可达通道）；Repo 与 URL 二选一。
	URL string `json:"url,omitempty"`
}

// fontsCommit google/fonts 仓库锁定的 commit。升字体版本 = 换这里 + 重预热缓存
// （python ai/font_tool.py 的 _cached_ttf 按 id@version 判缓存）。
const fontsCommit = "bd8f81ddb5c7"

var fontCatalog = []CatalogFont{
	{ID: "zcool-qingke-huangyou", Version: fontsCommit, Family: "ZCOOL QingKe HuangYou",
		Repo: "ofl/zcoolqingkehuangyou/ZCOOLQingKeHuangYou-Regular.ttf", License: "OFL"},
	{ID: "zcool-kuaile", Version: fontsCommit, Family: "ZCOOL KuaiLe",
		Repo: "ofl/zcoolkuaile/ZCOOLKuaiLe-Regular.ttf", License: "OFL"},
	{ID: "ma-shan-zheng", Version: fontsCommit, Family: "Ma Shan Zheng",
		Repo: "ofl/mashanzheng/MaShanZheng-Regular.ttf", License: "OFL"},
	{ID: "zhi-mang-xing", Version: fontsCommit, Family: "Zhi Mang Xing",
		Repo: "ofl/zhimangxing/ZhiMangXing-Regular.ttf", License: "OFL"},
	{ID: "long-cang", Version: fontsCommit, Family: "Long Cang",
		Repo: "ofl/longcang/LongCang-Regular.ttf", License: "OFL"},
	{ID: "liu-jian-mao-cao", Version: fontsCommit, Family: "Liu Jian Mao Cao",
		Repo: "ofl/liujianmaocao/LiuJianMaoCao-Regular.ttf", License: "OFL"},
	// 缝合像素 12px 简体（OFL，TakWolf/fusion-pixel-font）：库内唯一中文像素字体，
	// 像素风条目的标题/正文主力。release 资产是 zip（内含分语言 ttf，取 zh_hans），
	// 走 api.github.com assets 端点 + octet-stream（font_tool 侧解包）。
	// 升版本 = 换 Version 与 URL 里的 asset id（asset id 随 release 固定不变）。
	{ID: "fusion-pixel-12", Version: "2026.10.16", Family: "Fusion Pixel 12px Prop zh-Hans",
		URL: "https://api.github.com/repos/TakWolf/fusion-pixel-font/releases/assets/646862498", License: "OFL"},
}

// fontCatalogAliases 中文名/连写别名：模型点名时写「站酷庆科黄油体」也要接得住。
var fontCatalogAliases = map[string]string{
	"站酷庆科黄油体": "zcool-qingke-huangyou", "站酷庆科黄油": "zcool-qingke-huangyou",
	"站酷快乐体": "zcool-kuaile", "站酷快乐": "zcool-kuaile",
	"马善政毛笔楷书": "ma-shan-zheng", "马善政楷书": "ma-shan-zheng", "马善政": "ma-shan-zheng",
	"志莽行书": "zhi-mang-xing",
	"龙藏体":  "long-cang", "龙藏": "long-cang",
	"刘建毛草": "liu-jian-mao-cao",
	"缝合像素": "fusion-pixel-12", "缝合像素体": "fusion-pixel-12", "像素字体": "fusion-pixel-12",
	"fusion pixel": "fusion-pixel-12", "fusion pixel 12": "fusion-pixel-12",
}

// fontCatalogURL 下载入口走 GitHub API raw 模式：raw.githubusercontent 与 jsDelivr
// 在本部署网络不可达，api.github.com 可达且同样 commit 锁定（font_tool 侧带
// Accept: application/vnd.github.raw 头）。
func fontCatalogURL(repo, version string) string {
	return "https://api.github.com/repos/google/fonts/contents/" + repo + "?ref=" + version
}

func normalizeFontName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(s)), " "))
}

// matchCatalogFont 家族名/别名/ID 归一化匹配（大小写与多余空白不敏感）。
func matchCatalogFont(name string) *CatalogFont {
	n := normalizeFontName(name)
	if id, ok := fontCatalogAliases[n]; ok {
		for i := range fontCatalog {
			if fontCatalog[i].ID == id {
				return &fontCatalog[i]
			}
		}
	}
	for i := range fontCatalog {
		f := &fontCatalog[i]
		if n == normalizeFontName(f.Family) || n == f.ID || n == strings.ReplaceAll(f.ID, "-", "") {
			return f
		}
	}
	return nil
}

// FontCatalogList 设计提示词用的一句话菜单（家族规范名全列）。
func FontCatalogList() string {
	parts := make([]string, 0, len(fontCatalog))
	for _, f := range fontCatalog {
		parts = append(parts, f.Family)
	}
	return strings.Join(parts, "、")
}

// writeFontAssetRequest 目录字体准备申请书（ai/font_tool.py ensure 的输入）。
// 字体栈改写后在 LockDesign 调用；无目录字体时清掉旧申请书。
func writeFontAssetRequest(p *Project, matched map[string]*CatalogFont) error {
	if len(matched) == 0 {
		_ = os.Remove(p.Artifact("style/font_assets.request.json"))
		return nil
	}
	fonts := make([]map[string]string, 0, len(matched))
	seen := map[string]bool{}
	for _, f := range matched {
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		url := f.URL
		if url == "" {
			url = fontCatalogURL(f.Repo, f.Version)
		}
		fonts = append(fonts, map[string]string{
			"id": f.ID, "version": f.Version, "family": f.Family,
			"url": url, "license": f.License,
		})
	}
	b, err := json.MarshalIndent(map[string]any{"fonts": fonts}, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(p.Artifact("style/font_assets.request.json"), string(b))
}
