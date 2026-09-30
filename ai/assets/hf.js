/*!
 * hf.js — HyperFrames 受控动画层（守卫 API）
 * ------------------------------------------------------------------
 * 为什么存在：渲染是「逐帧 seek 截图」——每一帧把时间轴拨到精确时刻再截图。
 * 只有合成器属性（transform 系 + opacity）能亚像素插值、拨到任何时刻渲染结果
 * 确定；排版/布局属性（letterSpacing/width/fontSize…）会整数像素跳变，成片抖动。
 *
 * 本层把这些物理约束焊进 API：白名单之外的属性**当场抛错**并给出替代建议，
 * 模型在生成/检查阶段即被打回，而不是渲染出废片后靠 lint 追责。
 *
 * 用法（画面脚本内，hf.js 已由工程模板在 gsap 之后注入）：
 *   var tl = hf.timeline('seg01');                 // paused 时间轴 + 注册 __timelines
 *   hf.set(tl, '#title', {opacity: 0});            // 定格初值
 *   hf.tween(tl, '#title', {opacity: 1, y: 0, duration: .5, ease: 'power2.out'}, .2);
 *   hf.fromTo(tl, '#bar', {scaleX: 0}, {scaleX: 1, duration: .8}, 1.1);
 *   hf.eachFrame(tl, 12.95, function (t) { draw(t); });        // canvas 程序化动画
 *   hf.count(tl, '#num', 0, 3.6, 0, 5.6, function (v) { return '+' + v.toFixed(2); });
 *
 * 白名单属性：opacity x y xPercent yPercent scale scaleX scaleY rotation rotate
 *             rotationX rotationY skewX skewY z transformOrigin
 * 配置键（非动画属性）：duration ease delay stagger repeat yoyo repeatDelay
 *             immediateRender overwrite force3D paused id keyframes
 *             onUpdate onStart onComplete callbackScope
 * 高频违规的替代建议见 SUGGEST；canvas/数字滚动走 eachFrame/count 封装。
 * ES5 风格：与画面脚本约定一致，不依赖构建期语法。
 */
(function (root, factory) {
  var api = factory(root);
  if (typeof module !== 'undefined' && module.exports) { module.exports = api; }
  root.hf = api;
})(typeof window !== 'undefined' ? window : globalThis, function (root) {
  'use strict';

  var ALLOWED = {
    opacity: 1, x: 1, y: 1, xPercent: 1, yPercent: 1,
    scale: 1, scaleX: 1, scaleY: 1,
    rotation: 1, rotate: 1, rotationX: 1, rotationY: 1,
    skewX: 1, skewY: 1, z: 1, transformOrigin: 1
  };
  var CONFIG = {
    duration: 1, ease: 1, delay: 1, stagger: 1, repeat: 1, yoyo: 1,
    repeatDelay: 1, immediateRender: 1, overwrite: 1, force3D: 1,
    paused: 1, id: 1, keyframes: 1,
    onUpdate: 1, onStart: 1, onComplete: 1, callbackScope: 1
  };
  // 教学式报错：违规属性 → 等价且逐帧安全的做法（与检查器口径一致）
  var SUGGEST = {
    width: '宽度增长用 scaleX；确需精确宽度时 CSS 定格，不做补间',
    height: '高度增长用 scaleY',
    letterSpacing: '拆成单字元素再动每个字的 x（字距展开效果）',
    fontSize: '用 scale 缩放整块文字',
    marginLeft: '用 x 位移', marginRight: '用 x 位移', marginTop: '用 y 位移', marginBottom: '用 y 位移',
    top: '用 y（transform 位移）', left: '用 x', right: '用 x', bottom: '用 y',
    color: '颜色渐变改用半透明叠层的 opacity，或 eachFrame 里自绘',
    backgroundColor: '同 color：叠层 opacity 或自绘',
    textShadow: '静态定格；脉冲效果用半透明光晕层动 opacity',
    boxShadow: '静态定格'
  };

  function G() {
    if (!root.gsap) {
      throw new Error('hf: gsap 未加载——hf.js 必须引在 assets/gsap.min.js 之后');
    }
    return root.gsap;
  }

  function checkProps(api, props) {
    var bad = [];
    for (var k in props) {
      if (ALLOWED[k] || CONFIG[k]) { continue; }
      bad.push(k);
    }
    if (bad.length) {
      var tips = [];
      for (var i = 0; i < bad.length; i++) {
        tips.push('  ✗ ' + bad[i] + (SUGGEST[bad[i]] ? ' → ' + SUGGEST[bad[i]] : ' → 不在白名单，改用 transform/opacity 实现'));
      }
      throw new Error('hf.' + api + ': 属性被拒（逐帧 seek 渲染会跳变或不可复现）:\n'
        + tips.join('\n')
        + '\n白名单: ' + Object.keys(ALLOWED).join('/') + '\n配置键: ' + Object.keys(CONFIG).join('/'));
    }
  }

  function at_(at) { return typeof at === 'number' ? at : 0; }

  function timeline(id) {
    root.__timelines = root.__timelines || {};
    var tl = G().timeline({ paused: true });
    root.__timelines[id] = tl;
    return tl;
  }

  function set(tl, target, props, at) {
    checkProps('set', props);
    tl.set(target, props, at_(at));
    return tl;
  }

  function tween(tl, target, props, at) {
    checkProps('tween', props);
    tl.to(target, props, at_(at));
    return tl;
  }

  function fromTo(tl, target, from, to, at) {
    checkProps('fromTo(from)', from);
    checkProps('fromTo(to)', to);
    tl.fromTo(target, from, to, at_(at));
    return tl;
  }

  // eachFrame：canvas/程序化动画的标准驱动。draw(t) 收到「本画布内经过的时间」，
  // 必须是纯函数（随机数用固定种子每帧重生成），seek 到任意时刻画面可复现。
  function eachFrame(tl, dur, draw, at) {
    if (typeof draw !== 'function') { throw new Error('hf.eachFrame: 第 3 参必须是 function(t)'); }
    if (!(dur > 0)) { throw new Error('hf.eachFrame: duration 必须为正数'); }
    tl.to({}, {
      duration: dur, ease: 'none',
      onUpdate: function () { draw(Math.max(0, tl.time() - at_(at)), tl); }
    }, at_(at));
    return tl;
  }

  // count：数字滚动（读数/统计）。el 为选择器或节点；fmt 可选，默认保留两位小数。
  function count(tl, el, from, to, at, dur, fmt) {
    var node = typeof el === 'string' ? document.querySelector(el) : el;
    if (!node) { throw new Error('hf.count: 元素未找到 ' + el); }
    if (!(dur > 0)) { throw new Error('hf.count: duration 必须为正数'); }
    var f = fmt || function (v) { return String(Math.round(v * 100) / 100); };
    tl.to({}, {
      duration: dur, ease: 'none',
      onUpdate: function () {
        var p = Math.min(1, Math.max(0, (tl.time() - at_(at)) / dur));
        node.textContent = f(from + (to - from) * p);
      }
    }, at_(at));
    return tl;
  }

  return { timeline: timeline, set: set, tween: tween, fromTo: fromTo, eachFrame: eachFrame, count: count };
});
