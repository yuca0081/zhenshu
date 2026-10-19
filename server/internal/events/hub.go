// Package events：项目级事件枢纽。SSE 实时广播 + 最近事件重放（断线补齐）。
package events

import (
	"encoding/json"
	"sync"
	"time"
)

// Type 取值（清单只列常见的——新增事件种类不强制同步这里，别把清单当白名单）：
//
//	msg / agent_msg / tool_call / tool_result / stage / progress / error / done /
//	cancelled / style_confirmed / style_applied / prep_confirmed / prep_updated /
//	rework-started 类业务里程碑
type Event struct {
	Type   string `json:"type"`
	Stage  string `json:"stage,omitempty"`  // stage 事件：tts|compositions|assemble|check|render
	Detail string `json:"detail,omitempty"` // stage 事件：running|done|error: …
	TS     string `json:"ts"`
}

const replayCap = 64

type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan Event]struct{}
	last map[string][]Event
}

func NewHub() *Hub {
	return &Hub{subs: map[string]map[chan Event]struct{}{}, last: map[string][]Event{}}
}

// Subscribe 返回 事件流 + 取消函数 + 最近事件（重放）。
func (h *Hub) Subscribe(projectID string) (ch chan Event, replay []Event, cancel func()) {
	ch = make(chan Event, 64)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[projectID] == nil {
		h.subs[projectID] = map[chan Event]struct{}{}
	}
	h.subs[projectID][ch] = struct{}{}
	replay = append(replay, h.last[projectID]...)
	return ch, replay, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if m := h.subs[projectID]; m != nil {
			delete(m, ch)
			// 不 close(ch)：Emit 在锁外向快照 channel 非阻塞发送，这里 close 构成
			// send-on-closed 竞态（SSE 断连撞上管线发事件即 panic，且崩的是 producer
			// goroutine，gin recovery 罩不住）。读端随请求 ctx 退出，channel 交给 GC。
			// 订阅者清零即删条目：SSE 断连后 map 永不清理会让长期运行的服务内存单调增长
			if len(m) == 0 {
				delete(h.subs, projectID)
			}
		}
	}
}

// Emit 广播（慢消费者丢事件不阻塞管线）并记入重放窗。
func (h *Hub) Emit(projectID, typ, stage, detail string) {
	ev := Event{Type: typ, Stage: stage, Detail: detail, TS: time.Now().Format(time.RFC3339)}
	h.mu.Lock()
	if h.last[projectID] = append(h.last[projectID], ev); len(h.last[projectID]) > replayCap {
		h.last[projectID] = h.last[projectID][len(h.last[projectID])-replayCap:]
	}
	subs := make([]chan Event, 0, len(h.subs[projectID]))
	for c := range h.subs[projectID] {
		subs = append(subs, c)
	}
	h.mu.Unlock()
	for _, c := range subs {
		select {
		case c <- ev:
		default: // 队列满：丢弃（前端以 stage/done 事件驱动全量刷新，丢中间事件无害）
		}
	}
}

// Forget 项目删除时清掉它的重放窗：last map 只增不减会让删掉的项目在 SSE 侧
// 留下永远重放的幽灵记录（subs 条目断连即清，last 之前没人清）。
func (h *Hub) Forget(projectID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.last, projectID)
}

func (e Event) JSON() string {
	b, _ := json.Marshal(e)
	return string(b)
}
