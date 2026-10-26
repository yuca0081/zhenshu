package pipeline

// 目录字体：设计发明可以点名的开源展示字体（全 OFL，可自由分发）。
// URL 锁 google/fonts 仓库 commit（确定性）；下载与子集化由 ai/font_tool.py 代劳
// （vend 同款铁律：模型只有申请权，执行永远在平台侧），全字库缓存在
// data/library/vendor/fonts/，子集产物以 data URL 进各合成物的 @font-face。
// 为什么要目录：设计会发明渲染端没有的字体名（PingFang/站酷/思源…），要么钳掉
// （所有片子一个雅黑脸），要么给一份合法可下载的菜单让它点名——这是后者。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"

	"webpage-video-agent/server/internal/contract"
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

// writeFontAssetRequest 字体准备申请书（ai/font_tool.py ensure 的输入）：
// 目录匹配与样张自装声明合并落盘——LockDesign 重写时不能把自装条目擦掉。
// 两者皆空才清掉旧申请书。
func writeFontAssetRequest(p *Project, matched map[string]*CatalogFont, customs []contract.CustomFont) error {
	if len(matched) == 0 && len(customs) == 0 {
		_ = os.Remove(p.Artifact("style/font_assets.request.json"))
		return nil
	}
	fonts := make([]map[string]string, 0, len(matched)+len(customs))
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
	seenC := map[string]bool{}
	for _, c := range customs {
		k := normalizeFontName(c.Family)
		if seenC[k] {
			continue
		}
		seenC[k] = true
		fonts = append(fonts, map[string]string{
			"id": customFontID(c.Family), "version": customFontVersion(c.URL),
			"family": strings.TrimSpace(c.Family), "url": strings.TrimSpace(c.URL),
			"license": strings.TrimSpace(c.License),
		})
	}
	b, err := json.MarshalIndent(map[string]any{"fonts": fonts}, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(p.Artifact("style/font_assets.request.json"), string(b))
}

// customFontID 自装字体的缓存键（font_tool 按 id@version 判缓存）：家族名哈希
// 而不是 slug——家族可以是任意 Unicode（中文名），slug 会撞空；同家族跨项目
// 稳定命中机器级缓存。
func customFontID(family string) string {
	sum := sha256.Sum256([]byte("font:" + strings.ToLower(strings.TrimSpace(family))))
	return "custom-" + hex.EncodeToString(sum[:6])
}

// customFontVersion 自装字体的版本钉：URL 哈希。模型换来源（升版本/换镜像）
// = 换 URL = 新缓存键重新下载；同 URL 一律吃缓存，天然可复现。
func customFontVersion(url string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(url)))
	return hex.EncodeToString(sum[:6])
}

// MatchSampleFonts 扫样张 HTML 的 font-family 声明 → 目录字体匹配（prep② 首渲前
// 备字体的申请单来源：样张阶段还没有设计系统，LockDesign 的申请书尚未存在）。
func MatchSampleFonts(htmls []string) map[string]*CatalogFont {
	matched := map[string]*CatalogFont{}
	for _, html := range htmls {
		for _, m := range reFontFamilyDecl.FindAllStringSubmatch(html, -1) {
			for _, tok := range strings.Split(m[2], ",") {
				name := strings.Trim(strings.TrimSpace(tok), "'\"")
				if name == "" {
					continue
				}
				if c := matchCatalogFont(name); c != nil {
					matched[c.Family] = c
				}
			}
		}
	}
	return matched
}

// fontDeclRe 样张自装字体声明（与 vend 声明同构）：
// <!-- font: Family | 协议 | 直链URL -->（协议是模型自报的披露字段，终审页给人看）。
var fontDeclRe = regexp.MustCompile(`(?is)<!--\s*font:\s*(.*?)\s*-->`)

// fontDeclFields 单条声明按 | 切三段（家族/协议/URL）。第二刀起多余的分段并进
// URL 尾部——URL 里理论可含竖线，家族与协议不该有。
func fontDeclFields(inner string) (family, license, url string, ok bool) {
	parts := strings.SplitN(inner, "|", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	family = strings.TrimSpace(parts[0])
	license = strings.TrimSpace(parts[1])
	url = strings.TrimSpace(parts[2])
	if family == "" || url == "" || !(strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) {
		return "", "", "", false
	}
	return family, license, url, true
}

// CustomFontDecls 扫 HTML 里的自装字体声明（去重；命中目录/系统字体的声明丢弃
// ——目录钉版优先，装一份野源同名字体没有意义）。bad 返回格式不合的声明原文
// （回喂模型改写），调用方拿它组 Observation。
func CustomFontDecls(html string) (customs []contract.CustomFont, bad []string) {
	seen := map[string]bool{}
	for _, m := range fontDeclRe.FindAllStringSubmatch(html, -1) {
		family, license, url, ok := fontDeclFields(m[1])
		if !ok {
			bad = append(bad, strings.TrimSpace(m[0]))
			continue
		}
		k := normalizeFontName(family)
		if seen[k] {
			continue
		}
		if matchCatalogFont(family) != nil || osFontWhitelist[k] {
			continue // 目录/系统字体优先，声明静默降级为目录钉版
		}
		seen[k] = true
		customs = append(customs, contract.CustomFont{Family: family, License: license, URL: url})
	}
	return customs, bad
}

// CustomFontsFromHTMLs 多张样张 HTML 的自装声明聚合（去重排序，submit_style 提取
// 进 design_system 用——终审页披露与制作段钳制都吃这份清单）。
func CustomFontsFromHTMLs(htmls []string) []contract.CustomFont {
	var out []contract.CustomFont
	seen := map[string]bool{}
	for _, html := range htmls {
		cs, _ := CustomFontDecls(html)
		for _, c := range cs {
			k := normalizeFontName(c.Family)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Family < out[j].Family })
	return out
}

// CustomFontsFromRequest 申请书里的自装条目（id 前缀 custom-）：ensureSampleFonts
// 每次都写全集，申请书因此是「本项目装过什么」的持久账——修复轮重写 HTML 弄丢
// 声明也不丢账（submit 提取取两者并集的另一半）。
func CustomFontsFromRequest(p *Project) []contract.CustomFont {
	b, err := os.ReadFile(p.Artifact("style/font_assets.request.json"))
	if err != nil {
		return nil
	}
	var parsed struct {
		Fonts []struct {
			ID      string `json:"id"`
			Family  string `json:"family"`
			License string `json:"license"`
			URL     string `json:"url"`
		} `json:"fonts"`
	}
	if json.Unmarshal(b, &parsed) != nil {
		return nil
	}
	var out []contract.CustomFont
	for _, f := range parsed.Fonts {
		if strings.HasPrefix(f.ID, "custom-") && f.Family != "" && f.URL != "" {
			out = append(out, contract.CustomFont{Family: f.Family, License: f.License, URL: f.URL})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Family < out[j].Family })
	return out
}

// MergeCustomFonts 声明提取 ∪ 申请书账（按家族去重，声明侧优先——URL/协议以
// 样张里的原始声明为准），排序稳定供幂等比对。
func MergeCustomFonts(a, b []contract.CustomFont) []contract.CustomFont {
	out := append([]contract.CustomFont{}, a...)
	seen := map[string]bool{}
	for _, c := range a {
		seen[normalizeFontName(c.Family)] = true
	}
	for _, c := range b {
		if seen[normalizeFontName(c.Family)] {
			continue
		}
		seen[normalizeFontName(c.Family)] = true
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Family < out[j].Family })
	return out
}

// WriteFontAssetRequest 导出给 prep②：样张阶段按扫描结果写申请书（与 LockDesign
// 同一文件同一格式，font_tool ensure 消费；目录匹配与自装声明合并）。
func WriteFontAssetRequest(p *Project, matched map[string]*CatalogFont, customs []contract.CustomFont) error {
	return writeFontAssetRequest(p, matched, customs)
}

// MapCatalogFonts manifest 回显用：匹配结果的家族名列表（排序）。
func MapCatalogFonts(m map[string]*CatalogFont) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
