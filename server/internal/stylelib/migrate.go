package stylelib

// 化石导入（docs/style-library-cloud-storage.md §7）：PG 账本上线时把
// data/library/styles.json / vocab.json 一次性搬进 0005 表，媒体按需上传 MinIO。
//
// 幂等与崩溃安全：styles 表空才导（账本事务整体成功后化石改名 .imported——
// 崩溃在提交前 → 表仍空 → 下次重来；提交后改名前 → 表非空跳过，化石留作
// 摆设）。两台机器各有一份化石：先到者迁完改名，后到者表非空自动跳过，
// 不会拿自己的陈旧快照覆盖云端新数据。
//
// 次序先货后账：媒体先传（Stat 跳过已传，部分失败只警告不挡账——缺帧 404 可
// 容忍），账本一个事务整体落。账是提交点，孤儿货无害。

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
)

// Migrate 启动迁移入口（main 在 Library 构造后调用；文件账本模式是 no-op）。
func (l *Library) Migrate() {
	pg, ok := l.cat.(*pgCatalog)
	if !ok {
		return // 文件账本：化石就是活数据，无从迁起
	}
	if err := pg.ensureSchema(); err != nil {
		log.Printf("[stylelib] ⚠ %v", err)
		return
	}

	stylesB, stylesErr := os.ReadFile(l.stylesFossil())
	vocabB, vocabErr := os.ReadFile(l.vocabFossil())
	if stylesErr != nil && vocabErr != nil {
		return // 无化石，无可迁
	}

	empty, err := pg.Empty()
	if err != nil {
		log.Printf("[stylelib] ⚠ 账本表查询失败，跳过化石导入: %v", err)
		return
	}
	if !empty {
		log.Printf("[stylelib] 账本已有数据，跳过化石导入（本机化石未标记，可手动删）")
		return
	}

	var styles []Style
	if stylesErr == nil {
		if err := json.Unmarshal(stylesB, &styles); err != nil {
			log.Printf("[stylelib] ⚠ styles.json 化石损坏（%v），跳过导入", err)
			return
		}
	}
	fossilVocab := &Vocab{Entries: map[string]VocabEntry{}}
	if vocabErr == nil {
		if err := json.Unmarshal(vocabB, fossilVocab); err != nil || fossilVocab.Entries == nil {
			log.Printf("[stylelib] ⚠ vocab.json 化石损坏（%v），跳过导入", err)
			return
		}
	}

	// 1. 货先行：媒体上传（仅 MinIO 后端；本地后端媒体已在盘上）。快照源：
	//    style_media/<id> 优先，老条目回退 references/<refID>（与读取回退同规则）。
	if mm, ok := l.media.(*minioMedia); ok {
		for i := range styles {
			l.migrateMedia(mm, &styles[i])
		}
	}

	// 2. 账本事务：词表先于条目（校验要查词表），条目逐条校验、坏条目跳过。
	if err := l.importCatalog(pg, fossilVocab, styles); err != nil {
		log.Printf("[stylelib] ⚠ 化石导入失败（表仍为空，下次启动重试）: %v", err)
		return
	}

	// 3. 化石改名（成功标记；旧文件留作历史不再读）
	for _, f := range []struct{ from, to string }{
		{l.stylesFossil(), l.stylesFossil() + ".imported"},
		{l.vocabFossil(), l.vocabFossil() + ".imported"},
	} {
		if _, err := os.Stat(f.from); err == nil {
			_ = os.Rename(f.from, f.to)
		}
	}
	log.Printf("[stylelib] 化石导入完成：%d 词条 / %d 条目 → PG", len(fossilVocab.Entries), len(styles))
}

func (l *Library) stylesFossil() string { return filepath.Join(l.dir, "styles.json") }

func (l *Library) vocabFossil() string { return filepath.Join(l.dir, "vocab.json") }

// migrateMedia 单条目媒体上传：源目录实存优先，缺失回退参考视频目录。
func (l *Library) migrateMedia(mm *minioMedia, s *Style) {
	src := l.MediaDir(s.ID)
	if _, err := os.Stat(src); err != nil {
		if dir, ok := l.refMediaDir(s.ID); ok {
			src = dir
		} else {
			return // 无媒体可迁（成片反哺草稿本就无帧）
		}
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !ValidMediaName(name) || mm.Stat(s.ID, name) {
			continue // 白名单外 / 已传过（幂等）
		}
		if err := mm.Put(s.ID, name, filepath.Join(src, name)); err != nil {
			log.Printf("[stylelib] ⚠ 条目 %s 媒体 %s 上传失败（不挡导入，回看缺这张）: %v", s.ID, name, err)
		}
	}
}

// importCatalog 账本整体事务。词表全量 upsert；条目带词表核对后 upsert，
// 不合格条目跳过并喊出来（与文件加载时校验同一纪律）。
func (l *Library) importCatalog(pg *pgCatalog, fossilVocab *Vocab, styles []Style) error {
	return pg.db.Transaction(func(tx *gorm.DB) error {
		cat := &pgCatalog{db: tx}
		for _, e := range fossilVocab.Entries {
			if err := cat.PutVocab(e); err != nil {
				return fmt.Errorf("词条 %s 写入失败: %w", e.ID, err)
			}
		}
		for i := range styles {
			s := styles[i]
			if errs := validateEntryVocab(&s, fossilVocab); len(errs) > 0 {
				log.Printf("[stylelib] 条目 %s 校验不合格，导入时跳过: %s", s.ID, strings.Join(errs, "; "))
				continue
			}
			if err := cat.PutStyle(&s); err != nil {
				return fmt.Errorf("条目 %s 写入失败: %w", s.ID, err)
			}
		}
		return nil
	})
}
