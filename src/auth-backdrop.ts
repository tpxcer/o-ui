// 登录页动态背景：稀疏的代码字符缓慢漂浮；点击空白处时字符从点击点迸出，聚成一颗橙色的心后散去。
// 系统开启“减少动态效果”时不运行动画；登录页移除后自动停止。
const glyphs = ['{', '}', '<', '/>', '=>', ';', '()', '[]', '0', '1', 'fn', '&&', '::', '#', '$', '*', '%', '!=', 'let', 'go'];
const pick = () => glyphs[Math.floor(Math.random() * glyphs.length)];

type Drift = { x: number; y: number; vy: number; glyph: string; alpha: number; size: number; phase: number };
type Spark = { x: number; y: number; sx: number; sy: number; tx: number; ty: number; bx: number; by: number; glyph: string; hue: number; size: number };
type Burst = { born: number; sparks: Spark[] };

// 心形参数方程（单位约 ±16 × ±17），轮廓点之外再按隐式方程随机取内部点，让心形更饱满。
function heartPoints(count: number, scale: number): [number, number][] {
  const points: [number, number][] = [];
  const outline = Math.round(count * .7);
  for (let i = 0; i < outline; i++) {
    const t = i / outline * Math.PI * 2;
    points.push([16 * Math.sin(t) ** 3 * scale, -(13 * Math.cos(t) - 5 * Math.cos(2 * t) - 2 * Math.cos(3 * t) - Math.cos(4 * t)) * scale]);
  }
  while (points.length < count) {
    const x = Math.random() * 2.4 - 1.2, y = Math.random() * 2.4 - 1.2;
    if ((x * x + y * y - 1) ** 3 - x * x * y ** 3 <= 0) points.push([x * 14 * scale, -y * 14 * scale + 2 * scale]);
  }
  return points;
}

export function startBackdrop(root: HTMLElement): () => void {
  if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return () => {};
  const canvas = document.createElement('canvas');
  canvas.className = 'auth-backdrop';
  canvas.setAttribute('aria-hidden', 'true');
  root.prepend(canvas);
  const context = canvas.getContext('2d');
  if (!context) return () => canvas.remove();
  let width = 0, height = 0, frame = 0, stopped = false;
  const drifts: Drift[] = [], bursts: Burst[] = [];
  const style = getComputedStyle(document.documentElement);
  const ink = style.getPropertyValue('--muted').trim() || '#8b8e99', accent = style.getPropertyValue('--blue').trim() || '#6b9cf7';

  function resize(): void {
    const ratio = Math.min(window.devicePixelRatio || 1, 2);
    width = root.clientWidth; height = root.clientHeight;
    canvas.width = width * ratio; canvas.height = height * ratio;
    canvas.style.width = `${width}px`; canvas.style.height = `${height}px`;
    context!.setTransform(ratio, 0, 0, ratio, 0, 0);
    const wanted = Math.min(70, Math.round(width * height / 22000));
    while (drifts.length < wanted) drifts.push({ x: Math.random() * width, y: Math.random() * height, vy: .12 + Math.random() * .35, glyph: pick(), alpha: .08 + Math.random() * .22, size: 11 + Math.random() * 6, phase: Math.random() * Math.PI * 2 });
    drifts.length = wanted;
  }

  function burst(x: number, y: number): void {
    const scale = Math.min(9, Math.max(5, Math.min(width, height) / 90));
    const targets = heartPoints(110, scale);
    bursts.push({ born: performance.now(), sparks: targets.map(([dx, dy]) => {
      const angle = Math.random() * Math.PI * 2, speed = 40 + Math.random() * 120;
      return { x, y, sx: x, sy: y, bx: x + Math.cos(angle) * speed, by: y + Math.sin(angle) * speed, tx: x + dx, ty: y + dy, glyph: pick(), hue: 18 + Math.random() * 16, size: 10 + Math.random() * 5 };
    }) });
    if (bursts.length > 4) bursts.shift();
  }

  const ease = (n: number) => 1 - (1 - n) ** 3;
  function draw(now: number): void {
    if (stopped) return;
    context!.clearRect(0, 0, width, height);
    context!.textAlign = 'center';
    context!.textBaseline = 'middle';
    for (const d of drifts) {
      d.y -= d.vy;
      if (d.y < -20) { d.y = height + 20; d.x = Math.random() * width; d.glyph = pick(); }
      const twinkle = .6 + .4 * Math.sin(now / 1400 + d.phase);
      context!.globalAlpha = d.alpha * twinkle;
      context!.fillStyle = d.phase > 5 ? accent : ink;
      context!.font = `${d.size}px ui-monospace, SFMono-Regular, Menlo, monospace`;
      context!.fillText(d.glyph, d.x, d.y);
    }
    // 每次点击：0–0.35秒迸散，0.35–1.25秒聚成心形，停留并轻微跳动，2.2秒后淡出消失。
    for (let i = bursts.length - 1; i >= 0; i--) {
      const age = now - bursts[i].born;
      if (age > 2900) { bursts.splice(i, 1); continue; }
      const beat = 1 + .04 * Math.sin(age / 90) * (age > 1250 && age < 2200 ? 1 : 0);
      for (const s of bursts[i].sparks) {
        if (age < 350) { const p = ease(age / 350); s.x = s.sx + (s.bx - s.sx) * p; s.y = s.sy + (s.by - s.sy) * p; }
        else if (age < 1250) { const p = ease((age - 350) / 900); s.x = s.bx + (s.tx - s.bx) * p; s.y = s.by + (s.ty - s.by) * p; }
        else { const cx = bursts[i].sparks[0].sx, cy = bursts[i].sparks[0].sy; s.x = cx + (s.tx - cx) * beat; s.y = cy + (s.ty - cy) * beat + (age > 2200 ? (age - 2200) / 25 : 0); }
        context!.globalAlpha = age > 2200 ? Math.max(0, 1 - (age - 2200) / 700) : Math.min(1, age / 200);
        context!.fillStyle = `hsl(${s.hue} 100% 60%)`;
        context!.font = `600 ${s.size}px ui-monospace, SFMono-Regular, Menlo, monospace`;
        context!.fillText(s.glyph, s.x, s.y);
      }
    }
    context!.globalAlpha = 1;
    frame = requestAnimationFrame(draw);
  }

  const onClick = (event: MouseEvent) => {
    if ((event.target as Element).closest?.('.auth-card')) return;
    const box = root.getBoundingClientRect();
    burst(event.clientX - box.left, event.clientY - box.top);
  };
  resize();
  window.addEventListener('resize', resize);
  root.addEventListener('click', onClick);
  frame = requestAnimationFrame(draw);
  return () => {
    stopped = true;
    cancelAnimationFrame(frame);
    window.removeEventListener('resize', resize);
    root.removeEventListener('click', onClick);
    canvas.remove();
  };
}
