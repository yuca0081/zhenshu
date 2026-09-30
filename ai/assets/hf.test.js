// hf.js 守卫逻辑单测：mock gsap（不依赖浏览器/网络，node 直接跑）
// 用法：node ai/assets/hf.test.js
'use strict';
const assert = require('assert');

// ── mock 环境 ──────────────────────────────────────────────
global.window = global;                       // hf 挂到 window/globalThis
let lastCall = null;                          // 记录 tl 上的调用
function mockTimeline() {
  return {
    calls: [],
    time: function () { return this._t || 0; },
    _t: 0,
    set: function (target, props, at) { this.calls.push(['set', target, props, at]); lastCall = this.calls[this.calls.length - 1]; },
    to: function (target, props, at) { this.calls.push(['to', target, props, at]); lastCall = this.calls[this.calls.length - 1]; },
    fromTo: function (target, from, to, at) { this.calls.push(['fromTo', target, from, to, at]); lastCall = this.calls[this.calls.length - 1]; },
  };
}
global.gsap = { timeline: function () { return mockTimeline(); } };
global.document = { querySelector: function () { return null; } };  // count 的选择器走它
global.__timelines = {};
const hf = require('./hf.js');

// ── timeline：注册 __timelines、paused ─────────────────────
const tl = hf.timeline('seg01');
assert.strictEqual(global.__timelines['seg01'], tl);
assert.ok(lastCall === null);                 // gsap.timeline 本身被调，无 tl 调用

// ── 白名单通过 + 参数转发（at 缺省 0）──────────────────────
hf.tween(tl, '#a', { opacity: 1, y: 0, duration: .5, ease: 'power2.out' }, .2);
assert.deepStrictEqual(lastCall, ['to', '#a', { opacity: 1, y: 0, duration: .5, ease: 'power2.out' }, .2]);
hf.set(tl, '#a', { opacity: 0 });
assert.deepStrictEqual(lastCall, ['set', '#a', { opacity: 0 }, 0]);
hf.fromTo(tl, '#bar', { scaleX: 0 }, { scaleX: 1, duration: .8 }, 1.1);
assert.deepStrictEqual(lastCall, ['fromTo', '#bar', { scaleX: 0 }, { scaleX: 1, duration: .8 }, 1.1]);

// ── 违规属性：抛错 + 教学建议 ───────────────────────────────
assert.throws(() => hf.tween(tl, '#a', { letterSpacing: '10px', duration: .6 }), /letterSpacing[\s\S]*拆成单字元素/);
assert.throws(() => hf.tween(tl, '#a', { width: 100 }), /width[\s\S]*scaleX/);
assert.throws(() => hf.set(tl, '#a', { fontSize: 64 }), /fontSize[\s\S]*scale/);
assert.throws(() => hf.fromTo(tl, '#a', { top: 0 }, { top: 100 }), /fromTo\(from\)[\s\S]*用 y/);
assert.throws(() => hf.tween(tl, '#a', { 什么乱七八糟: 1 }), /不在白名单/);

// ── eachFrame：tLocal 数学 + 非法入参 ──────────────────────
{
  const t2 = mockTimeline();
  let got = null;
  hf.eachFrame(t2, 12.95, function (t) { got = t; }, 0.5);
  const toCall = t2.calls[0];
  assert.strictEqual(toCall[0], 'to');
  t2._t = 3.5; toCall[2].onUpdate();          // seek 到 3.5s
  assert.strictEqual(got, 3.0);               // 画布内时间 = 3.5 - 0.5
  t2._t = 0.2; toCall[2].onUpdate();          // 未到 at → 钳 0
  assert.strictEqual(got, 0);
  assert.throws(() => hf.eachFrame(t2, 1, 'not-fn'), /第 3 参必须是 function/);
  assert.throws(() => hf.eachFrame(t2, 0, function () {}), /duration 必须为正数/);
}

// ── count：进度插值 + fmt ─────────────────────────────────
{
  const t3 = mockTimeline();
  const node = { textContent: '' };
  hf.count(t3, node, 0, 3.6, 0.5, 5.6, function (v) { return '+' + v.toFixed(2); });
  const c = t3.calls[0];
  t3._t = 0.5; c[2].onUpdate();               // 进度 0
  assert.strictEqual(node.textContent, '+0.00');
  t3._t = 3.3; c[2].onUpdate();               // 进度 0.5
  assert.strictEqual(node.textContent, '+1.80');
  t3._t = 99; c[2].onUpdate();                // 进度钳 1
  assert.strictEqual(node.textContent, '+3.60');
  assert.throws(() => hf.count(t3, '#missing', 0, 1, 0, 1), /元素未找到/);
}

// ── gsap 缺失时的可读报错 ──────────────────────────────────
{
  const saved = global.gsap;
  delete global.gsap;
  assert.throws(() => hf.timeline('x'), /gsap 未加载/);
  global.gsap = saved;
}

console.log('hf.test.js: all passed (timeline/白名单/转发/eachFrame/count)');
