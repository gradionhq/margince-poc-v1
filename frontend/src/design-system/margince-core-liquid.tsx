import { useEffect, useRef } from "react";
import type { MarginceCoreState } from "./margince-core";
import { usePrefersReducedMotion } from "./motion";

/**
 * The Core's liquid, drawn in WebGL.
 *
 * Internal to `MarginceCoreScene` — nothing outside this directory mounts it, which
 * is what WDS-CORE-1 means by one primitive. Ported verbatim from the
 * prototype's shader; the component wrapper is what is new, and it exists to
 * make the lifecycle correct: the monolith started a rAF loop per canvas at
 * module scope and never stopped one.
 *
 * Two things the shader does NOT own:
 *
 *  - **Its colour.** The tint is read from the `--coreTint` token the state
 *    resolves to (see core.css), so the sphere follows the palette instead of
 *    carrying one. Without this the Core's hue was three GLSL constants, which
 *    is how it drifted from the artifact's pale mint into a saturated green that
 *    no token could correct.
 *  - **Its speed.** State drives it from JS, so the shader stays a pure function
 *    of time.
 *
 * Performance, all deliberate:
 *  - renders at a fixed 80×80 internal buffer (smoke is soft; the upscale is
 *    invisible) and caps at 24fps — together roughly 8× cheaper than dpr2@60
 *  - freezes on a hidden tab, an unfocused window, or an off-screen canvas,
 *    so an idle orb behind a modal costs nothing
 */

const FRAGMENT_SHADER = `
precision highp float;
uniform vec2 uRes; uniform float uT;
uniform vec3 uTint; uniform float uTintMix;
float hash(vec3 p){ p=fract(p*0.3183099+vec3(.1,.2,.3)); p*=17.0; return fract(p.x*p.y*p.z*(p.x+p.y+p.z)); }
float noise(vec3 x){
  vec3 i=floor(x), f=fract(x); f=f*f*(3.-2.*f);
  return mix(
    mix(mix(hash(i),hash(i+vec3(1.,0.,0.)),f.x), mix(hash(i+vec3(0.,1.,0.)),hash(i+vec3(1.,1.,0.)),f.x), f.y),
    mix(mix(hash(i+vec3(0.,0.,1.)),hash(i+vec3(1.,0.,1.)),f.x), mix(hash(i+vec3(0.,1.,1.)),hash(i+vec3(1.,1.,1.)),f.x), f.y), f.z);
}
float fbm(vec3 p){ float v=0., a=.5; for(int i=0;i<3;i++){ v+=a*noise(p); p=p*2.03+vec3(11.7,7.3,5.1); a*=.5; } return v; }
void main(){
  vec2 p=(gl_FragCoord.xy*2.-uRes)/min(uRes.x,uRes.y);
  float r=length(p);
  if(r>1.){ gl_FragColor=vec4(0.); return; }
  float z=sqrt(max(0.,1.-r*r));
  // spherical lens: the field compresses toward the rim like thick glass
  vec2 q=p*(1.15-.35*z);
  // heavier than smoke: the masses slide rather than jitter, so the fast
  // harmonics are gone and the drift runs at about half speed
  float t=uT*.15;
  // breathing: the whole liquid swells and settles on ONE rhythm. A second
  // harmonic at .68 used to ride on top, and a fast beat over a slow swell reads
  // as the liquid trembling — the drift and the advection already carry the
  // interior's life.
  float br=.5+.45*sin(uT*.26);
  q*=1.12-.18*br;
  // three masses on sum-of-sines paths — every direction, no gravity
  vec2 c1=.45*vec2(sin(t*1.6+.5)+.34*sin(t*2.5),      cos(t*1.3)+.34*sin(t*2.1+1.2));
  vec2 c2=.50*vec2(sin(t*1.0+2.8)+.34*cos(t*2.7),     sin(t*2.2+4.1)+.34*cos(t*1.7));
  vec2 c3=.40*vec2(cos(t*1.9+1.9)+.34*sin(t*1.5+3.3), sin(t*1.4+5.6)+.34*cos(t*2.3));
  float t2=uT*.13;
  // vortex flow around the drifting cores — the smoke swirls like real fluid
  vec2 v1=q-c1, v2=q-c2, v3=q-c3;
  vec2 flow= vec2(-v1.y,v1.x)/(dot(v1,v1)+.22)
           - vec2(-v2.y,v2.x)/(dot(v2,v2)+.30)
           + vec2(-v3.y,v3.x)/(dot(v3,v3)+.26);
  // flowmap advection: two phase-shifted samples crossfaded, so features drift
  // and stretch ALONG the flow. Long cycles and a weak noise-time term keep the
  // motion in the advection — that is the difference between flowing and boiling.
  float ph=fract(t2*.34), phB=fract(t2*.34+.5);
  float n1=fbm(vec3((q-flow*.40*ph )*2.2, t2*.06));
  float n2=fbm(vec3((q-flow*.40*phB)*2.2, t2*.06+11.3));
  float n=mix(n1,n2,abs(ph*2.-1.));
  float d=exp(-dot(q-c1,q-c1)/.30)
        + .85*exp(-dot(q-c2,q-c2)/.36)
        + .75*exp(-dot(q-c3,q-c3)/.24);
  d*=.7+.9*n;       // smoke texture carves the drifting masses
  d+=n*.5;          // ambient haze so the glass never looks empty
  d*=.75+.5*br;     // density breathes with the swell
  d*=.15+.85*smoothstep(1.,.72,r); // thins toward the rim so the shell reads
  float dd=clamp(d,0.,1.6);
  // silky ridge veins where the noise folds back on itself
  float vein=pow(1.-abs(2.*n-1.),3.);
  // The three constants are the FALLBACK ramp, used only when the token could
  // not be resolved (uTintMix = 0). With a tint in hand the same ramp is rebuilt
  // around it, so the shading relationships survive a hue swing to amber or red.
  vec3 cMid =mix(vec3(.08,.60,.40), uTint,                      uTintMix);
  vec3 cDeep=mix(vec3(.02,.32,.21), uTint*.42,                  uTintMix);
  vec3 cMint=mix(vec3(.58,.97,.78), mix(uTint,vec3(1.),.62),    uTintMix);
  vec3 c=mix(cMid,cDeep,smoothstep(.6,1.5,dd));
  c=mix(c,cMint,vein*.55*smoothstep(.15,.7,dd));
  c=mix(c,cMint,pow(clamp(n-.5,0.,1.),2.)*1.1*smoothstep(.2,.8,dd));
  float a=(1.-exp(-dd*2.6))*.97;
  a*=smoothstep(1.,.985,r);
  gl_FragColor=vec4(c*a,a);
}`;

const VERTEX_SHADER =
  "attribute vec2 a;void main(){gl_Position=vec4(a,0.,1.);}";

/** Internal render resolution. The smoke is soft enough that 80px upscales clean. */
const RES = 80;
const FRAME_MS = 1000 / 24;

/**
 * How fast the liquid moves per state. `unavailable` is 0 — a server we cannot
 * reach must not look busy, and the frozen frame says that better than any
 * colour does.
 */
const SPEED: Record<MarginceCoreState, number> = {
  idle: 1,
  listening: 1.35,
  working: 2.1,
  success: 0.8,
  attention: 1.5,
  error: 1.2,
  quiet: 0.35,
  unavailable: 0,
};

/**
 * Resolve `--coreTint` to linear 0..1 components.
 *
 * `getComputedStyle` does NOT resolve custom properties — asking for
 * `--coreTint` hands back the authored text, and for a `color-mix()` token that
 * is not something to parse here. Painting it onto a throwaway probe and reading
 * `color` back makes the engine do the resolving, which is the only way a token
 * defined by mixing reaches a shader uniform.
 */
function resolveTint(host: HTMLElement): [number, number, number] | null {
  const probe = document.createElement("span");
  probe.style.color = "var(--coreTint)";
  probe.style.position = "absolute";
  probe.style.pointerEvents = "none";
  host.appendChild(probe);
  const computed = getComputedStyle(probe).color;
  probe.remove();
  const parts = computed.match(/[\d.]+/g);
  if (!parts || parts.length < 3) {
    return null;
  }
  return [
    Number(parts[0]) / 255,
    Number(parts[1]) / 255,
    Number(parts[2]) / 255,
  ];
}

export function CoreLiquid({
  state = "idle",
  className = "",
}: {
  state?: MarginceCoreState;
  className?: string;
}) {
  const ref = useRef<HTMLCanvasElement>(null);
  const reduced = usePrefersReducedMotion();

  useEffect(() => {
    const canvas = ref.current;
    if (!canvas) {
      return;
    }
    const gl = canvas.getContext("webgl", {
      alpha: true,
      antialias: true,
      premultipliedAlpha: true,
    });
    if (!gl) {
      // No WebGL (or jsdom). WDS-CORE-3: the state still has to render, and it
      // renders through the stylesheet — `.core-fluid.off` paints the per-state
      // gradient. Setting a class rather than an inline background is what lets
      // the fallback vary by state without this file knowing any colour.
      canvas.classList.add("off");
      return;
    }
    canvas.classList.remove("off");

    const compile = (type: number, source: string) => {
      const shader = gl.createShader(type);
      if (!shader) {
        return null;
      }
      gl.shaderSource(shader, source);
      gl.compileShader(shader);
      return shader;
    };

    const program = gl.createProgram();
    const vs = compile(gl.VERTEX_SHADER, VERTEX_SHADER);
    const fs = compile(gl.FRAGMENT_SHADER, FRAGMENT_SHADER);
    if (!program || !vs || !fs) {
      return;
    }
    gl.attachShader(program, vs);
    gl.attachShader(program, fs);
    gl.linkProgram(program);
    // biome-ignore lint/correctness/useHookAtTopLevel: gl.useProgram is a WebGL call, not a React hook — the `use` prefix trips the heuristic
    gl.useProgram(program);

    const buffer = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
    // One oversized triangle rather than two — covers the clip volume with
    // three vertices and no shared edge to rasterize twice.
    gl.bufferData(
      gl.ARRAY_BUFFER,
      new Float32Array([-1, -1, 3, -1, -1, 3]),
      gl.STATIC_DRAW,
    );
    const attr = gl.getAttribLocation(program, "a");
    gl.enableVertexAttribArray(attr);
    gl.vertexAttribPointer(attr, 2, gl.FLOAT, false, 0, 0);

    const uRes = gl.getUniformLocation(program, "uRes");
    const uT = gl.getUniformLocation(program, "uT");
    const uTint = gl.getUniformLocation(program, "uTint");
    const uTintMix = gl.getUniformLocation(program, "uTintMix");

    const host = canvas.parentElement ?? canvas;
    const tint = resolveTint(host);
    if (tint) {
      gl.uniform3f(uTint, tint[0], tint[1], tint[2]);
      // Read as text, not painted: --coreTintMix is a plain number, so
      // getPropertyValue hands back exactly what the state block authored.
      // (--coreTint needs the probe above because a color-mix() only becomes a
      // colour by being painted. A number needs no such trick.)
      const authored = Number.parseFloat(
        getComputedStyle(host).getPropertyValue("--coreTintMix"),
      );
      gl.uniform1f(uTintMix, Number.isFinite(authored) ? authored : 0.22);
    } else {
      gl.uniform1f(uTintMix, 0);
    }

    const speed = SPEED[state];

    // A non-zero seed: at t=0 all three masses sit on top of each other, and
    // the first frame would be a plain blob.
    let time = 42.7;
    let last = performance.now();
    let accumulator = 0;
    let frame = 0;

    let drawnOnce = false;

    const render = (now: number) => {
      frame = requestAnimationFrame(render);
      // Freeze — keeping the last drawn frame — whenever nobody can see it.
      // The first frame is exempt: freezing before anything has been drawn
      // leaves an empty canvas, and "window not focused" is the normal state
      // when the page is opened in a background tab.
      const unseen =
        document.hidden || !document.hasFocus() || canvas.offsetParent === null;
      if (unseen && drawnOnce) {
        last = now;
        return;
      }
      if (drawnOnce && now - accumulator < FRAME_MS) {
        return;
      }
      drawnOnce = true;
      accumulator = now;
      const delta = Math.min((now - last) / 1000, 0.08);
      last = now;
      // Reduced motion and `unavailable` both hold time still, so the canvas
      // shows a composed frame rather than nothing — the end state, not blank.
      if (!reduced) {
        time += delta * speed;
      }
      if (canvas.width !== RES) {
        canvas.width = RES;
        canvas.height = RES;
        gl.viewport(0, 0, RES, RES);
      }
      gl.uniform2f(uRes, canvas.width, canvas.height);
      gl.uniform1f(uT, time);
      gl.clearColor(0, 0, 0, 0);
      gl.clear(gl.COLOR_BUFFER_BIT);
      gl.drawArrays(gl.TRIANGLES, 0, 3);
    };
    frame = requestAnimationFrame(render);

    return () => {
      cancelAnimationFrame(frame);
      gl.deleteProgram(program);
      gl.deleteShader(vs);
      gl.deleteShader(fs);
      gl.deleteBuffer(buffer);
    };
  }, [reduced, state]);

  return <canvas ref={ref} className={`core-fluid ${className}`.trim()} />;
}
