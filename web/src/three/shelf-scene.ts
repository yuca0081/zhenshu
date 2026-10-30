import * as THREE from 'three'
import { RoomEnvironment } from 'three/examples/jsm/environments/RoomEnvironment.js'

/* ============================================================
   项目三维玻璃书架（移植自 yuca-next shelf-scene.ts，改造成项目陈列）
   - 横向浅弧排列的玻璃板，每块内嵌一张「封面」（CanvasTexture 画项目名）
   - 选中项向前浮出，其余退暗
   - 主题跟随 CSS 令牌（--glass-transmission / --glass-tint / --panel ...）
   - 主题切换时背景平面与材质颜色在渲染循环里缓变，与 DOM 650ms 同步
   ============================================================ */

export interface ShelfItem {
  id: string
  title: string
  date: string
  status?: string
}

export interface ShelfSceneOptions {
  onSelect?: (item: ShelfItem, index: number) => void
  onIndexChange?: (index: number) => void
}

interface Panel {
  group: THREE.Group
  paper: THREE.Mesh<THREE.PlaneGeometry, THREE.MeshBasicMaterial>
  glass: THREE.Mesh<THREE.BoxGeometry, THREE.MeshPhysicalMaterial>
  item: ShelfItem
  canvas: HTMLCanvasElement
  texture: THREE.CanvasTexture
}

const SPACING = 1.72
const PANEL_W = 1.34
const PANEL_H = 1.94
const LERP = 0.085

function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

function lerpColor(target: THREE.Color, hex: string, k: number) {
  const c = new THREE.Color(hex)
  target.lerp(c, k)
}

export class ShelfScene {
  private container: HTMLElement
  private opts: ShelfSceneOptions
  private renderer: THREE.WebGLRenderer
  private scene: THREE.Scene
  private camera: THREE.PerspectiveCamera
  private row: THREE.Group
  private bgPlane: THREE.Mesh<THREE.PlaneGeometry, THREE.MeshBasicMaterial>
  private contact: THREE.Mesh<THREE.PlaneGeometry, THREE.MeshBasicMaterial>
  private panels: Panel[] = []
  private items: ShelfItem[] = []
  private raf = 0
  private clock = new THREE.Clock()
  private focus = 0
  private rowIndex = 0 // 惯性插值用的连续行位置
  private disposed = false
  private pointerDown = false
  private dragAccum = 0
  private raycaster = new THREE.Raycaster()
  private pointer = new THREE.Vector2(-9, -9)
  private hoverIndex = -1
  private interactive = true
  private resizeObs: ResizeObserver
  private bgTarget = new THREE.Color('#0e141d')

  constructor(container: HTMLElement, opts: ShelfSceneOptions = {}) {
    this.container = container
    this.opts = opts

    this.renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true })
    this.renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2))
    this.renderer.toneMapping = THREE.ACESFilmicToneMapping
    this.renderer.toneMappingExposure = 1.05
    container.appendChild(this.renderer.domElement)
    this.renderer.domElement.style.cssText = 'width:100%;height:100%;display:block;touch-action:pan-y'

    this.scene = new THREE.Scene()
    const pmrem = new THREE.PMREMGenerator(this.renderer)
    this.scene.environment = pmrem.fromScene(new RoomEnvironment(), 0.04).texture

    this.camera = new THREE.PerspectiveCamera(30, 1, 0.1, 60)
    this.camera.position.set(0, 0.14, 5.1)
    this.camera.lookAt(0, 0, 0)

    // 场内背景平面：给 transmission 提供可折射的底色（DOM 背景 CSS 仍负责兜底）
    this.bgPlane = new THREE.Mesh(
      new THREE.PlaneGeometry(80, 50),
      new THREE.MeshBasicMaterial({ color: this.bgTarget.clone() }),
    )
    this.bgPlane.position.z = -9
    this.scene.add(this.bgPlane)

    // 接地软影（径向渐变贴图）
    this.contact = new THREE.Mesh(
      new THREE.PlaneGeometry(2.6, 1.1),
      new THREE.MeshBasicMaterial({ transparent: true, depthWrite: false }),
    )
    this.contact.rotation.x = -Math.PI / 2
    this.contact.position.y = -PANEL_H / 2 - 0.16
    this.contact.material.map = makeContactTexture()
    this.scene.add(this.contact)

    this.row = new THREE.Group()
    this.scene.add(this.row)

    // 灯光：环境贴图之外补一盏侧光，让玻璃边缘有高光带
    const key = new THREE.DirectionalLight(0xffffff, 1.6)
    key.position.set(2.5, 3.2, 3.5)
    this.scene.add(key)
    const rim = new THREE.DirectionalLight(0xffffff, 0.5)
    rim.position.set(-3, 1.5, -2)
    this.scene.add(rim)

    this.bindEvents()
    this.resizeObs = new ResizeObserver(() => this.resize())
    this.resizeObs.observe(container)
    this.resize()
    this.applyTheme()
    this.loop()
  }

  /* ---------- 对外 API ---------- */

  setItems(items: ShelfItem[]) {
    this.clearPanels()
    this.items = items
    this.focus = 0
    this.rowIndex = 0
    items.forEach((item, i) => this.panels.push(this.makePanel(item, i)))
    this.layout(true)
    // 入场组装：从更散、更远、更暗的姿态缓动归位
    for (const p of this.panels) {
      const t = p.group.userData.target as { x: number; z: number }
      p.group.position.x = t.x * 1.5
      p.group.position.z = t.z - 1.6
      p.paper.material.color.setScalar(0)
    }
    this.updateTextures()
    // 板建好后按当前主题重刷玻璃参数（构造期的 applyTheme 时还没有板，
    // 板上只带着日间默认值，夜间会白成一排灯箱）
    this.applyTheme()
  }

  setIndex(i: number) {
    const n = this.items.length
    if (!n) return
    this.focus = ((i % n) + n) % n
    this.layout(false)
    this.opts.onIndexChange?.(this.focus)
  }

  next() {
    this.setIndex(this.focus + 1)
  }

  prev() {
    this.setIndex(this.focus - 1)
  }

  get index() {
    return this.focus
  }

  /** 详情浮层等场景下临时禁用键鼠交互 */
  setInteractive(v: boolean) {
    this.interactive = v
  }

  /** 读取当前 CSS 令牌并应用到材质；主题切换时调用 */
  applyTheme() {
    const tintArr = cssVar('--glass-tint').split(',').map((v) => parseFloat(v) / 255)
    const tint = new THREE.Color(tintArr[0] || 0.9, tintArr[1] || 0.9, tintArr[2] || 0.9)
    const bg = cssVar('--bg') || '#0e141d'

    this.bgTarget.set(bg)
    for (const p of this.panels) {
      p.glass.material.color.lerp(tint, 1)
      p.glass.material.needsUpdate = true
    }
    this.updateTextures()
  }

  dispose() {
    this.disposed = true
    cancelAnimationFrame(this.raf)
    this.resizeObs.disconnect()
    const el = this.renderer.domElement
    el.removeEventListener('pointerdown', this.onPointerDown)
    window.removeEventListener('pointermove', this.onPointerMove)
    window.removeEventListener('pointerup', this.onPointerUp)
    el.removeEventListener('click', this.onClick)
    window.removeEventListener('keydown', this.onKeyDown)
    this.clearPanels()
    this.scene.environment?.dispose()
    this.renderer.dispose()
    el.remove()
  }

  /* ---------- 内部 ---------- */

  private makePanel(item: ShelfItem, index: number): Panel {
    const group = new THREE.Group()

    // 封面纸面
    const canvas = document.createElement('canvas')
    canvas.width = 512
    canvas.height = 740
    const texture = new THREE.CanvasTexture(canvas)
    texture.colorSpace = THREE.SRGBColorSpace
    texture.anisotropy = 4
    const paper = new THREE.Mesh(
      new THREE.PlaneGeometry(PANEL_W, PANEL_H),
      // 不透明：纸面在 opaque 队列先画，前面透射玻璃才能采样到它
      new THREE.MeshBasicMaterial({ map: texture, toneMapped: false }),
    )
    paper.position.z = 0.012

    // 玻璃板（前薄壳）：轻量假玻璃——普通透明混合 + 清漆高光，不走 transmission
    // 透射管线（实测透射缓冲区在部分环境洗出白幕盖住纸面；薄纱混合观感等价且稳定）
    const glass = new THREE.Mesh(
      new THREE.BoxGeometry(PANEL_W + 0.16, PANEL_H + 0.16, 0.05),
      new THREE.MeshPhysicalMaterial({
        color: 0xffffff,
        transparent: true,
        opacity: 0.16,
        roughness: 0.18,
        metalness: 0,
        clearcoat: 0.9,
        clearcoatRoughness: 0.18,
        depthWrite: false,
      }),
    )
    // 压低环境反射强度：RoomEnvironment 顶部亮窗会在玻璃上洗出白幕，盖住纸面
    glass.material.envMapIntensity = 0.5

    group.add(paper, glass)
    group.userData.index = index
    this.row.add(group)

    return { group, paper, glass, item, canvas, texture }
  }

  private clearPanels() {
    for (const p of this.panels) {
      this.row.remove(p.group)
      p.paper.geometry.dispose()
      p.paper.material.dispose()
      p.glass.geometry.dispose()
      p.glass.material.dispose()
      p.texture.dispose()
    }
    this.panels = []
  }

  /** 按弧线摆位；immediate=true 时跳过动画 */
  private layout(immediate: boolean) {
    const n = this.items.length
    for (let i = 0; i < n; i++) {
      const p = this.panels[i]
      const off = i - this.focus
      const target = {
        x: off * SPACING,
        z: -Math.min(Math.abs(off), 6) * 0.22 + (off === 0 ? 0.5 : 0),
        ry: -off * 0.085,
        // 选中板全亮，侧板按距离压暗（颜色标量调制，避免透明排序问题）
        dim: off === 0 ? 1 : Math.max(0.22, 0.72 - Math.abs(off) * 0.09),
      }
      p.group.userData.target = target
      if (immediate) {
        p.group.position.set(target.x, 0, target.z)
        p.group.rotation.y = target.ry
        p.paper.material.color.setScalar(target.dim)
      }
      // 视锥裁剪：远离焦点的面板隐藏，省绘制
      p.group.visible = Math.abs(off) <= 9
    }
    if (immediate) this.rowIndex = this.focus
  }

  private updateTextures() {
    const paperCss = cssVar('--panel') || 'rgba(20,28,39,.97)'
    const ink = cssVar('--ink') || '#e9edf3'
    const muted = cssVar('--muted') || '#8590a2'
    const accent = cssVar('--accent') || '#9dc0e8'
    const line = cssVar('--line') || 'rgba(255,255,255,.14)'

    for (let i = 0; i < this.panels.length; i++) {
      const p = this.panels[i]
      const ctx = p.canvas.getContext('2d')!
      const W = p.canvas.width
      const H = p.canvas.height
      ctx.clearRect(0, 0, W, H)

      // 封面底
      ctx.fillStyle = paperCss
      ctx.fillRect(0, 0, W, H)

      // 顶部 EN 微标 + 零填充编号
      ctx.fillStyle = muted
      ctx.font = "500 22px 'MiSans VF', sans-serif"
      ctx.textBaseline = 'top'
      ctx.fillText('PROJECT', 44, 46)
      ctx.textAlign = 'right'
      ctx.fillStyle = accent
      ctx.font = "600 26px 'MiSans VF', sans-serif"
      ctx.fillText(String(i + 1).padStart(2, '0'), W - 44, 45)
      ctx.textAlign = 'left'

      // 发丝线
      ctx.strokeStyle = line
      ctx.lineWidth = 1.5
      ctx.beginPath()
      ctx.moveTo(44, 96)
      ctx.lineTo(W - 44, 96)
      ctx.stroke()

      // 项目名（CJK 按字符折行）
      ctx.fillStyle = ink
      ctx.font = "600 46px 'MiSans VF', sans-serif"
      const lines = wrapCjk(ctx, p.item.title, W - 88, 6)
      let ty = 150
      for (const ln of lines) {
        ctx.fillText(ln, 44, ty)
        ty += 62
      }

      // 底部：状态（右）+ 日期（左）+ 强调色短划
      ctx.textBaseline = 'bottom'
      ctx.fillStyle = muted
      ctx.font = "500 22px 'MiSans VF', sans-serif"
      ctx.fillText(p.item.date, 44, H - 44)
      if (p.item.status) {
        ctx.textAlign = 'right'
        ctx.fillText(p.item.status, W - 44, H - 44)
        ctx.textAlign = 'left'
      }
      ctx.fillStyle = accent
      ctx.fillRect(44, H - 26, 36, 4)

      p.texture.needsUpdate = true
    }
  }

  private resize() {
    const w = this.container.clientWidth || 1
    const h = this.container.clientHeight || 1
    this.renderer.setSize(w, h)
    this.camera.aspect = w / h
    // 窄屏拉远，保证面板完整
    this.camera.position.z = w / h < 1.05 ? 6.6 : 5.1
    this.camera.updateProjectionMatrix()
  }

  private bindEvents() {
    const el = this.renderer.domElement
    el.addEventListener('pointerdown', this.onPointerDown)
    window.addEventListener('pointermove', this.onPointerMove)
    window.addEventListener('pointerup', this.onPointerUp)
    el.addEventListener('click', this.onClick)
    window.addEventListener('keydown', this.onKeyDown)
  }

  private onPointerDown = () => {
    this.pointerDown = true
    this.dragAccum = 0
  }

  private onPointerMove = (e: PointerEvent) => {
    const rect = this.renderer.domElement.getBoundingClientRect()
    this.pointer.x = ((e.clientX - rect.left) / rect.width) * 2 - 1
    this.pointer.y = -((e.clientY - rect.top) / rect.height) * 2 + 1

    if (this.pointerDown) {
      this.dragAccum += e.movementX
      // 拖动累计一个面板宽度 → 翻页
      if (Math.abs(this.dragAccum) > 130) {
        this.dragAccum > 0 ? this.prev() : this.next()
        this.dragAccum = 0
      }
    }
  }

  private onPointerUp = () => {
    this.pointerDown = false
  }

  private onClick = () => {
    if (!this.interactive) return
    // 拖拽后的 click 不触发打开
    if (Math.abs(this.dragAccum) > 6) return
    if (this.hoverIndex >= 0) {
      if (this.hoverIndex === this.focus) {
        this.opts.onSelect?.(this.items[this.focus], this.focus)
      } else {
        this.setIndex(this.hoverIndex)
      }
    }
  }

  private onKeyDown = (e: KeyboardEvent) => {
    if (!this.interactive) return
    const tag = (e.target as HTMLElement)?.tagName
    if (tag === 'INPUT' || tag === 'TEXTAREA' || (e.target as HTMLElement)?.isContentEditable) return
    if (e.key === 'ArrowRight') this.next()
    else if (e.key === 'ArrowLeft') this.prev()
    else if (e.key === 'Enter') this.opts.onSelect?.(this.items[this.focus], this.focus)
  }

  private loop = () => {
    if (this.disposed) return
    this.raf = requestAnimationFrame(this.loop)
    const dt = Math.min(this.clock.getDelta(), 0.05)

    // 行位置缓动
    this.rowIndex += (this.focus - this.rowIndex) * Math.min(1, dt * 60 * LERP)
    this.row.position.x = -this.rowIndex * SPACING

    // 面板各自缓动到目标位
    for (const p of this.panels) {
      const t = p.group.userData.target as
        | { x: number; z: number; ry: number; dim: number }
        | undefined
      if (!t) continue
      const k = Math.min(1, dt * 60 * LERP)
      p.group.position.x += (t.x - p.group.position.x) * k
      p.group.position.z += (t.z - p.group.position.z) * k
      p.group.rotation.y += (t.ry - p.group.rotation.y) * k
      const cur = p.paper.material.color
      const next = cur.r + (t.dim - cur.r) * k
      if (Math.abs(next - cur.r) > 0.001) cur.setScalar(next)
    }

    // 背景色向主题缓变
    lerpColor(this.bgPlane.material.color, `#${this.bgTarget.getHexString()}`, Math.min(1, dt * 4))
    this.contact.position.x = this.row.children[this.focus]?.position.x ?? 0

    // hover 拾取
    if (!this.pointerDown) {
      this.raycaster.setFromCamera(this.pointer, this.camera)
      const hits = this.raycaster.intersectObjects(this.row.children, true)
      const hit = hits[0]
      const idx = hit ? findPanelIndex(this.row, hit.object) : -1
      if (idx !== this.hoverIndex) {
        this.hoverIndex = idx
        this.renderer.domElement.style.cursor = idx >= 0 ? 'pointer' : 'default'
      }
    }

    this.renderer.render(this.scene, this.camera)
  }
}

function findPanelIndex(row: THREE.Group, obj: THREE.Object3D): number {
  let o: THREE.Object3D | null = obj
  while (o && o.parent !== row) o = o.parent
  return o ? row.children.indexOf(o) : -1
}

function makeContactTexture(): THREE.Texture {
  const c = document.createElement('canvas')
  c.width = 256
  c.height = 128
  const ctx = c.getContext('2d')!
  const g = ctx.createRadialGradient(128, 64, 8, 128, 64, 120)
  g.addColorStop(0, 'rgba(0,0,0,0.28)')
  g.addColorStop(1, 'rgba(0,0,0,0)')
  ctx.fillStyle = g
  ctx.fillRect(0, 0, 256, 128)
  return new THREE.CanvasTexture(c)
}

/** CJK 文本按字符贪心折行 */
function wrapCjk(ctx: CanvasRenderingContext2D, text: string, maxWidth: number, maxLines: number): string[] {
  const lines: string[] = []
  let cur = ''
  for (const ch of text) {
    if (ctx.measureText(cur + ch).width > maxWidth) {
      lines.push(cur)
      cur = ch
      if (lines.length === maxLines) return lines
    } else {
      cur += ch
    }
  }
  if (cur && lines.length < maxLines) lines.push(cur)
  return lines
}
