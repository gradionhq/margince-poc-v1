import { useEffect, useRef, useState } from "react";
import type { MarginceCoreState } from "./margince-core";
import { usePrefersReducedMotion } from "./motion";
import {
  isWindowFocused,
  retainWindowFocusSignal,
  subscribeToWindowFocus,
} from "./window-focus";

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
 *  - renders into a small internal buffer sized from the DISPLAYED width and
 *    capped at 160 (see `coreBufferSize`), and caps at 24fps — a hero costs
 *    roughly a fifth of what the same sphere would at dpr2@60
 *  - the 24fps cap is spent, not polled: each drawn frame schedules the next
 *    through a timer, so the main thread wakes ~24 times a second instead of at
 *    the display's refresh rate (120Hz on the machines this is developed on) to
 *    discard four callbacks out of five
 *  - STOPS — not throttles — whenever nothing would change: a hidden tab, a
 *    window that does not have focus, a canvas scrolled or routed off screen, and
 *    a state whose liquid does not move at all (`unavailable`, or any state under
 *    reduced motion, where one composed frame IS the whole animation). Each of
 *    those has an event that ends it, so the loop is woken by the event rather
 *    than by asking every frame — the layout read this used to do per callback
 *    (`canvas.offsetParent`) forced style and layout at refresh rate for an
 *    answer that only changes when a route does.
 *  - one triangle, no MSAA, no clear (the draw covers every pixel of the
 *    viewport), and no per-frame layout reads
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
  float w=abs(ph*2.-1.);
  float n=mix(n1,n2,w);
  float d=exp(-dot(q-c1,q-c1)/.30)
        + .85*exp(-dot(q-c2,q-c2)/.36)
        + .75*exp(-dot(q-c3,q-c3)/.24);
  d*=.7+.9*n;       // smoke texture carves the drifting masses
  // Ambient haze. HALF of what it was: at .5 it filled the space between the
  // masses and the sphere read as green mist in a ball. Jade is depth first and
  // light second, and the voids have to stay dark or nothing reads as deep.
  d+=n*.26;
  d*=.75+.5*br;     // density breathes with the swell
  d*=.15+.85*smoothstep(1.,.72,r); // thins toward the rim so the shell reads
  float dd=clamp(d,0.,1.6);
  // silky ridge veins where the noise folds back on itself
  float vein=pow(1.-abs(2.*n-1.),3.);
  /*
   * The filaments — threads of light caught in the moving liquid. Three things
   * make them read as strands rather than as decoration, and each is load-bearing:
   *
   *  - **Stretched into the flow.** pow(1-|2f-1|,k) traces the field's 0.5 level
   *    set, and the level set of an isotropic field is a CLOSED CONTOUR: on its
   *    own this term draws loops, and no exponent turns a loop into a strand.
   *    Rotating the sample into the flow's frame and compressing one axis is what
   *    elongates those contours along the current.
   *  - **Advected on the same two-phase crossfade as the masses**, so they travel
   *    with the liquid instead of boiling in place.
   *  - **Gated on the flow's own speed.** Ungated they sit at one brightness
   *    across the whole sphere, which reads as cracks in the glass rather than as
   *    structure being carried. Bright in the fast water, faint in the slack.
   *
   * One noise per phase, not a whole fbm: a second octave was visibly busier
   * without being better, and it cost a third again as much.
   */
  vec2 fd=normalize(flow+vec2(1e-4,1e-4));
  mat2 fr=mat2(fd.x,fd.y,-fd.y,fd.x);
  vec2 sa=fr*(q-flow*.40*ph ); sa.x*=.30;
  vec2 sb=fr*(q-flow*.40*phB); sb.x*=.30;
  float f=mix(noise(vec3(sa*6.4, t2*.10)), noise(vec3(sb*6.4, t2*.10+4.2)), w);
  // The density gate opens EARLIER than the emission's does, and that gap is
  // deliberate: with the ambient stop dark, gating both at the same density put
  // every visible thing in one lit patch and left the rest of the sphere an empty
  // dark field. The threads reach into the shade, where they are structure without
  // being light.
  float fil=pow(1.-abs(2.*f-1.), 8.+7.*n)
          * smoothstep(.20,.88,dd)
          * clamp(length(flow)*.42, .18, 1.);
  // Subsurface: light that entered the stone, scattered, and left again. The
  // depth z was already in hand for the lens, so this costs nothing new.
  float sss=pow(z,1.7);
  float fres=pow(1.-z,3.6);   // the polished edge
  // The three constants are the FALLBACK ramp, used only when the token could
  // not be resolved (uTintMix = 0). With a tint in hand the same ramp is rebuilt
  // around it, so the shading relationships survive a hue swing to amber or red.
  // The AMBIENT stop — the liquid where nothing is happening — and it is the
  // sphere's floor, not its middle. Measured at its old value it sat at luminance
  // .455, so the darkest pixel anywhere in the Core was a mid tone and there was
  // nothing for the fire to be bright against. The dense masses are lit by the
  // emitters below; this is the dark they are lit against.
  vec3 cMid =mix(vec3(.045,.30,.20), uTint*.62,                 uTintMix);
  // The dark end, and it is DARK: the shade a light has to be seen against. At
  // its old value the deepest liquid still sat above mid grey, so the sphere had
  // a glow with nothing to glow out of. Same hue as the rest of the ramp, a third
  // of the luminance.
  vec3 cDeep=mix(vec3(.01,.16,.105), uTint*.26,                 uTintMix);
  // Its white target is a GREEN-white, not a neutral one. Pulling a green toward
  // pure white raises blue fastest — the highlight went cyan against the body and
  // put a fringe on everything it touched.
  vec3 cMint=mix(vec3(.62,.97,.72), mix(uTint,vec3(.95,1.,.90),.62), uTintMix);
  // The emission colour, and the reason it is its own stop: cMint is the mint the
  // glass REFLECTS, so it is mixed most of the way to white and desaturates as it
  // brightens. A source does the opposite — it gets more saturated the hotter it
  // is. This one keeps its chroma at full brightness, which is the difference
  // between a pale sphere and a lit one.
  // The emitter is the tint SCALED, not the tint mixed toward some brighter
  // colour, and that is the whole point: multiplying a triple by a scalar leaves
  // its channel ratios alone, so the emission is the brand's exact hue at a
  // higher luminance. Mixing toward a hand-picked bright green instead put the
  // glow at 122° against the brand's 159° — a spring green lit next to a jade
  // one, which is visible as a clash long before anyone measures it. The fallback
  // is the fallback ramp's own mid scaled the same way, so both branches agree.
  vec3 cGlow=mix(vec3(.15,1.,.68), uTint*1.9, uTintMix);
  vec3 c=mix(cMid,cDeep,smoothstep(.55,1.45,dd));
  // The broad vein system, pulled back from .55. It and the filaments are two
  // ridge systems over the same field: at full strength they overlay into a
  // scribble and neither is legible.
  c=mix(c,cMint,vein*.34*smoothstep(.15,.7,dd));
  // Pulled back from .85. cMint is near-white, so this term was lifting the whole
  // mid-range toward it — a broad wash that flattened exactly the range the depth
  // has to live in.
  c=mix(c,cMint,pow(clamp(n-.5,0.,1.),2.)*.58*smoothstep(.2,.8,dd));
  /*
   * DEPTH, applied to the base ONLY — before a single emitter runs.
   *
   * This ordering is the whole trick. Deepening at the end would drag the glow
   * down with the shade and the sphere would just get darker; deepening the base
   * first means the darks fall away and the light added afterwards stands out
   * against them. Bright parts glow because the dark parts got darker.
   *
   * Hue-preserving: a gamma on the LUMINANCE, with all three channels scaled by
   * the same factor. Gamma-ing the channels independently would pull the ratios
   * apart and slide the hue toward whichever channel is largest, which for this
   * ramp is a slide toward green.
   */
  float L=dot(c,vec3(.2126,.7152,.0722));
  c*= L>1e-4 ? pow(L,1.42)/L : 1.;
  // The strands mix toward the EMISSIVE stop, not the reflective one. cMint is
  // mixed most of the way to white and so carries blue: against the green body it
  // put a cyan fringe on every thread, which read as chromatic aberration rather
  // than as light. .46, not higher — past that the threads stop being in the
  // liquid and start being drawn on it.
  c=mix(c,cGlow,fil*.46);
  /*
   * EMISSION — the inner light, and the thing the sphere had none of. Everything
   * above only ever MIXED between ramp stops, which can lighten a colour but can
   * never make it brighter than the brightest stop: a liquid lit entirely by
   * being paler. Adding light instead of interpolating toward it is what lets the
   * dense masses read as the source.
   *
   * Three emitters, deliberately not one flat centre glow — a uniform hot middle
   * reads as a torch pointed at the glass:
   *  - the MASSES, so the light lives in the moving liquid and travels with it
   *  - the FILAMENTS, hottest of all, so the threads are light rather than paint
   *  - the DEPTH, a soft bloom through the body of the stone
   *
   * The limb multiply below then cuts it back at the edge, so emission never
   * flattens the roundness the dark limb buys.
   */
  float emit = smoothstep(.42,1.30,dd)*.26
             + fil*.44
             + sss*.12;
  // Reinhard rolloff on the emission, not a clamp. The three terms can sum past
  // 1 where a hot filament crosses a dense mass, and a hard clip there drives
  // whichever channel saturates first — green — and the hue slides off the brand
  // exactly at the brightest, most-looked-at pixels. This compresses the highs
  // instead, so the hot spots keep their chroma.
  emit = emit/(1.+emit*.75);
  c += cGlow*emit*1.7;
  // The dark limb. A cabochon of jade is DARKEST just inside its edge, because
  // that is the longest path light takes through the stone. Without it a sphere
  // reads as a flat lit disc however good its interior is — this one line does
  // more for the roundness than everything above it.
  c*=1.-.20*pow(1.-z,2.2);
  // The rim, now the ONLY thing drawing the sphere's edge: the shell's 1.5px
  // hairline is gone, because an outline is what a lit body does not have. So it
  // is stronger than it was, and it is emissive rather than a mix — an edge made
  // of light reads as a boundary without reading as a drawn line.
  c += cGlow*fres*.30;
  /*
   * HUE-PRESERVING highlight rolloff, and the reason it exists is measured: with
   * the emission added, green railed at 1.0 across about 7% of the sphere. That
   * is not merely "too bright" — once one channel clips, the hue of the pixel is
   * decided by whatever the OTHER two happened to be, and since every stop in
   * this ramp is a blue-leaning green (jade is), the hottest and most-looked-at
   * pixels drifted cyan. Balancing the emitter's own channels did not fix it,
   * because the clipping was in the sum, not in one term.
   *
   * So: find the brightest channel, soft-knee THAT, and scale all three by the
   * same factor. The ratio between them is untouched, so a hot pixel becomes a
   * more saturated green and never a different colour. Nothing can exceed 1 after
   * this, which means the driver's own clamp never gets to pick a hue for us.
   */
  float mx=max(max(c.r,c.g),c.b);
  // Knee at .95, not .72. Below the knee nothing is touched, so the only thing
  // this compresses is genuine overflow — at .72 it was flattening the whole top
  // end, which is exactly the range the glow needs, and the sphere lost its
  // brights to a safety measure meant only to stop clipping.
  float rolled=mx/(1.+max(mx-.95,0.));
  c*= mx>1e-4 ? rolled/mx : 1.;
  /*
   * The liquid is a BODY, not a veil — and this is what makes the dark parts
   * dark. Alpha used to track density alone, which meant the voids deepened for
   * contrast were also the most transparent: on a light page the surface flooded
   * straight through them and the darkest pixel in the sphere came back as
   * luminance .54, lighter than mid grey. No colour change can fix that, because
   * a dark colour at low alpha over white is not dark. So the shade gets opacity
   * to be dark IN, and density now modulates the top .26 rather than the whole
   * range. The glass reads as glass from the shell's highlights and rim, which sit
   * above this and are unaffected.
   */
  float a=.74+.26*(1.-exp(-dd*2.6));
  // The rim stays present even where the liquid is thin, so the stone has an
  // edge instead of fading out into the glass.
  a=mix(a,1.,fres*.34);
  a*=smoothstep(1.,.985,r);
  gl_FragColor=vec4(c*a,a);
}`;

const VERTEX_SHADER =
  "attribute vec2 a;void main(){gl_Position=vec4(a,0.,1.);}";

const FRAME_MS = 1000 / 24;

/** Below this the filaments alias into sparkle rather than reading as threads. */
const MIN_BUFFER = 96;
/** Above this nothing new is visible at any size the Core is ever drawn at. */
const MAX_BUFFER = 160;

/**
 * The internal buffer for a sphere displayed at `cssPx` across.
 *
 * This was a flat 80 for every Core, and that single number was the ceiling on
 * the interior: at a 172px hero it is a 2.15× upscale, so any thread finer than
 * about four display pixels dissolved before it reached the screen. The liquid
 * was not lacking detail, it was lacking somewhere to put it.
 *
 * Deriving it from the displayed size is what lets ONE shader serve a 126px
 * workbench orb and a 230px hero without either paying for the other, and the
 * clamp at both ends is what keeps that honest — a caller that sizes a Core to
 * fill a page does not get to buy fragments it cannot show.
 *
 * MIN_BUFFER only ever raises the buffer TOWARD the displayed size, never past
 * it: it exists because upscaling aliases the filaments, and a sphere drawn at
 * 32px in the shell's rail is not being upscaled — it is being downscaled, where
 * fragments beyond 1:1 buy nothing at all. Floored at 96 that orb was rendering
 * nine times the pixels it shows, permanently, in chrome that is on every screen.
 *
 * Deliberately NOT multiplied by devicePixelRatio. Matching a 2× display would
 * cost four times the fragments to sharpen a subject that is blurred glass and
 * soft smoke; the shell's highlight and rim are CSS and stay crisp on their own.
 */
export function coreBufferSize(cssPx: number): number {
  // Called before first layout, where clientWidth is 0. The hero is the common
  // case, so bias the guess there; a ResizeObserver corrects it on the next frame.
  if (!Number.isFinite(cssPx) || cssPx <= 0) {
    return MAX_BUFFER;
  }
  const displayed = Math.round(cssPx);
  const floor = Math.min(MIN_BUFFER, displayed);
  return Math.min(MAX_BUFFER, Math.max(floor, Math.round(cssPx * 0.9)));
}

// Some WebGL-capable hosts (older embedded webviews) have no ResizeObserver,
// or one whose constructor throws; the render effect's `.off` class is
// already cleared by the time it gets here, so either would leave the canvas
// transparent instead of painting the shader if left unguarded. Pulled out of
// the effect itself so that guard doesn't count toward its complexity.
function tryCreateResizeObserver(
  onWidth: (width: number) => void,
): ResizeObserver | null {
  if (typeof ResizeObserver === "undefined") {
    return null;
  }
  try {
    return new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width;
      if (width !== undefined) {
        onWidth(width);
      }
    });
  } catch {
    return null;
  }
}

/**
 * Whether the canvas is on screen, reported as it changes.
 *
 * The same shape of guard as `tryCreateResizeObserver`, for the same hosts and
 * one more reason: a missing IntersectionObserver must leave the Core RUNNING.
 * Failing the other way would freeze the sphere on whatever frame it happened to
 * hold, which is indistinguishable from a broken shader.
 */
function tryObserveOnScreen(
  canvas: HTMLCanvasElement,
  onChange: (onScreen: boolean) => void,
): IntersectionObserver | null {
  if (typeof IntersectionObserver === "undefined") {
    return null;
  }
  try {
    const observer = new IntersectionObserver((entries) => {
      const latest = entries[entries.length - 1];
      if (latest) {
        onChange(latest.isIntersecting);
      }
    });
    observer.observe(canvas);
    return observer;
  } catch {
    return null;
  }
}

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

/**
 * Compile and link the shader pair, or return null having released everything.
 *
 * Null is the ONLY failure signal, and every rung of the build reports through
 * it: a driver that rejects the shader still hands back non-null objects, so
 * without the explicit `COMPILE_STATUS` and `LINK_STATUS` reads the caller gets
 * a program that draws nothing and cannot tell it apart from one that works.
 * That is a blank sphere on the machines least able to report it.
 *
 * The shaders are deleted on the way out of BOTH paths. After a successful link
 * the program holds its own reference, so dropping ours here is the release, not
 * a leak — and it leaves the caller one object to clean up instead of three.
 */
function buildProgram(gl: WebGLRenderingContext): WebGLProgram | null {
  const compile = (type: number, source: string) => {
    const shader = gl.createShader(type);
    if (!shader) {
      return null;
    }
    gl.shaderSource(shader, source);
    gl.compileShader(shader);
    if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
      gl.deleteShader(shader);
      return null;
    }
    return shader;
  };

  const program = gl.createProgram();
  const vs = compile(gl.VERTEX_SHADER, VERTEX_SHADER);
  const fs = compile(gl.FRAGMENT_SHADER, FRAGMENT_SHADER);
  const release = () => {
    if (vs) {
      gl.deleteShader(vs);
    }
    if (fs) {
      gl.deleteShader(fs);
    }
  };
  if (!program || !vs || !fs) {
    release();
    if (program) {
      gl.deleteProgram(program);
    }
    return null;
  }
  gl.attachShader(program, vs);
  gl.attachShader(program, fs);
  gl.linkProgram(program);
  const linked = gl.getProgramParameter(program, gl.LINK_STATUS);
  release();
  if (!linked) {
    gl.deleteProgram(program);
    return null;
  }
  return program;
}

/**
 * The draw loop: 24fps while somebody is looking, and NOTHING otherwise.
 *
 * Three ideas, each of which was a cost before:
 *
 *  - **The frame budget is slept, not polled.** A frame schedules the next one
 *    through a timer and takes a single rAF to land on a paint. Gating inside a
 *    self-rescheduling rAF loop instead meant one callback per display refresh —
 *    120 a second on this machine to draw 24 — and each of those callbacks did
 *    the visibility check below.
 *  - **Invisible means stopped, not throttled**, and it is decided by events
 *    rather than by asking. The old check read `canvas.offsetParent`, which
 *    forces style and layout, at refresh rate, for an answer that changes when a
 *    route or a scroll position does — so the cheapest possible frame was also
 *    the one that made the browser do the most work. Only conditions whose END
 *    has an event may pause the loop; see `seen()`.
 *  - **A still liquid draws once.** `unavailable` holds time still, and so does
 *    reduced motion; there the first composed frame is the entire animation and
 *    the loop parks after it. `wake()` is how a resize gets one more.
 *
 * Returns the handle the effect owns: `stop` releases every listener and pending
 * callback, `wake` asks for a frame if the loop is parked and can be seen.
 */
function runLiquidLoop({
  gl,
  canvas,
  uRes,
  uT,
  speed,
  reduced,
  bufferSize,
}: Readonly<{
  gl: WebGLRenderingContext;
  canvas: HTMLCanvasElement;
  uRes: WebGLUniformLocation | null;
  uT: WebGLUniformLocation | null;
  /** 0 holds the liquid still — see SPEED. */
  speed: number;
  reduced: boolean;
  /** The buffer edge, read per DRAWN frame — never per callback. */
  bufferSize: () => number;
}>): Readonly<{ stop: () => void; wake: () => void }> {
  // A non-zero seed: at t=0 all three masses sit on top of each other, and the
  // first frame would be a plain blob.
  let time = 42.7;
  let last = performance.now();
  let frame = 0;
  let timer = 0;
  let drawnOnce = false;
  let onScreen = true;
  // Seeded by the subscription below, which opens by delivering the state it
  // already holds. The initial value is what that delivery is compared against,
  // so it reads the same source rather than assuming focus.
  let windowFocused = isWindowFocused();
  const still = reduced || speed === 0;
  /*
   * The buffer edge THIS loop has configured, and why it is not read back off
   * the canvas: a loop belongs to one program, and every loop starts with none
   * of its uniforms set. Comparing against `canvas.width` looks equivalent and
   * is not — a new program over a canvas that is already the right size matches
   * on the first frame, so the size block is skipped and `uRes` stays at its
   * default of (0,0). The shader divides by `min(uRes.x, uRes.y)`, so the sphere
   * comes back empty and stays empty. That is one state change away on any
   * screen (`quiet` → `working`) and it is what a restored context does too.
   * Zero means "nothing configured yet", which no real size can be.
   */
  let configured = 0;

  const draw = (now: number) => {
    const delta = Math.min((now - last) / 1000, 0.08);
    last = now;
    // Reduced motion and `unavailable` both hold time still, so the canvas shows
    // a composed frame rather than nothing — the end state, not blank.
    if (!reduced) {
      time += delta * speed;
    }
    const size = bufferSize();
    if (configured !== size) {
      configured = size;
      canvas.width = size;
      canvas.height = size;
      gl.viewport(0, 0, size, size);
      // With the buffer. It is square, and its edge is all this uniform carries,
      // so re-sending it every frame was a state change per draw for a value that
      // changes at a breakpoint.
      gl.uniform2f(uRes, size, size);
    }
    gl.uniform1f(uT, time);
    // No clear. One triangle covers the whole viewport and the shader writes
    // every fragment in it — transparent outside the sphere, by its own early
    // return — so a clear first would be painting the buffer twice.
    gl.drawArrays(gl.TRIANGLES, 0, 3);
    drawnOnce = true;
  };

  /**
   * Whether anybody is watching the canvas: in the viewport, in a visible tab,
   * in a window that has focus.
   *
   * A pause is only safe if something is guaranteed to END it, so every term
   * here is a condition whose end arrives as an event — the
   * IntersectionObserver, `visibilitychange`, and the window's own `focus`.
   * Focus qualifies because it is TRACKED rather than asked: `window-focus.ts`
   * holds the `focus`/`blur` pair and pushes each change in here, and the only
   * time it reads `document.hasFocus()` is to seed the state at subscribe time.
   * Polling it inside this predicate instead would be a stop with no guaranteed
   * way back — the answer is only true for the instant it is asked, so a resume
   * that lands between two frames is never observed and the sphere stays frozen
   * on screen for the rest of the session, which is exactly what a broken shader
   * looks like.
   */
  const seen = () => onScreen && !document.hidden && windowFocused;

  const pump = (now: number) => {
    frame = 0;
    // A lost context ends the loop rather than pausing it: every object it draws
    // with belonged to the dead context. CoreLiquid's own listener owns the
    // recovery, and NOT rescheduling here is what stops this run spinning until
    // that arrives.
    if (gl.isContextLost()) {
      return;
    }
    // Freeze on the last drawn frame whenever nobody can see it; an event starts
    // it again. The FIRST frame is exempt — freezing before anything is drawn
    // leaves an empty canvas, which is what a page opened in a background tab
    // would show the moment it is looked at.
    if (drawnOnce && !seen()) {
      return;
    }
    draw(now);
    if (still) {
      return;
    }
    timer = window.setTimeout(() => {
      timer = 0;
      frame = requestAnimationFrame(pump);
    }, FRAME_MS);
  };

  const wake = () => {
    if (frame || timer || !seen()) {
      return;
    }
    // The clock restarts on resume, so a liquid parked for ten minutes continues
    // from where it stopped instead of jumping by the length of the pause.
    last = performance.now();
    frame = requestAnimationFrame(pump);
  };

  document.addEventListener("visibilitychange", wake);
  // The subscription opens by restating the state it already holds, which is what
  // seeds `windowFocused` above. A restatement is not a resume: waking on it would
  // buy a frame for a loop that has not started yet, and one for every Core that
  // mounts into a window that never lost focus.
  const releaseFocus = subscribeToWindowFocus((focused) => {
    if (focused === windowFocused) {
      return;
    }
    windowFocused = focused;
    wake();
  });
  const onScreenObserver = tryObserveOnScreen(canvas, (visible) => {
    onScreen = visible;
    wake();
  });
  // Not through `wake()`: the first frame is owed even to a Core that mounts
  // into a blurred or hidden tab, because an undrawn canvas is blank and a blank
  // sphere is indistinguishable from a broken one. `pump` grants exactly that
  // one and then parks (see `drawnOnce` there).
  frame = requestAnimationFrame(pump);

  return {
    wake,
    stop: () => {
      cancelAnimationFrame(frame);
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", wake);
      releaseFocus();
      onScreenObserver?.disconnect();
    },
  };
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
  // Bumped by `webglcontextrestored` to re-run the effect. A lost context
  // invalidates every object built from it, so recovery is a rebuild from
  // scratch rather than a resume, and re-running the setup IS that rebuild.
  const [contextEpoch, setContextEpoch] = useState(0);

  /*
   * Loss and recovery are watched for the canvas's WHOLE life, deliberately
   * apart from the render effect below.
   *
   * Sharing that effect looked equivalent and was not. The render effect is
   * torn down whenever `state` or the motion preference changes, so a prop
   * change arriving DURING a lost context removed this listener, and the
   * replacement run exited early because the context was still gone. The
   * recovery then had nothing listening for it and the Core stayed on the CSS
   * fallback for the rest of the session, from an ordinary sign-in changing
   * `idle` to `working` at the wrong moment.
   *
   * `preventDefault` on the loss is what makes recovery possible at all: the
   * browser only fires `webglcontextrestored` if the default is prevented.
   */
  useEffect(() => {
    const canvas = ref.current;
    if (!canvas) {
      return;
    }
    const onLost = (event: Event) => {
      event.preventDefault();
      canvas.classList.add("off");
    };
    // The rebuild: a restored context invalidates every object built from the
    // old one, so recovery is a fresh setup rather than a resume, and bumping
    // the epoch is what re-runs it.
    const onRestored = () => setContextEpoch((epoch) => epoch + 1);
    canvas.addEventListener("webglcontextlost", onLost);
    canvas.addEventListener("webglcontextrestored", onRestored);
    return () => {
      canvas.removeEventListener("webglcontextlost", onLost);
      canvas.removeEventListener("webglcontextrestored", onRestored);
    };
  }, []);

  /*
   * The stylesheet's half of the same stillness: `margince-core.css` pauses the
   * breath, the sheen, the halo and the feed off `data-window-blurred`, and that
   * attribute only exists while something holds the signal.
   *
   * Held here, for the canvas's whole life, rather than inside the draw loop:
   * the loop is never created on the non-GPU rung (WDS-CORE-3) or in a host with
   * no WebGL at all, and that is precisely where the CSS rhythms ARE the Core.
   * Every Core mounts exactly one of these (WDS-CORE-1), so the signal is live
   * whenever a Core is on the page and released with the last one.
   */
  useEffect(retainWindowFocusSignal, []);

  // biome-ignore lint/correctness/useExhaustiveDependencies(contextEpoch): the effect never reads it, which is the point. It is the rebuild trigger, bumped by the `webglcontextrestored` listener above once the GPU hands the context back.
  useEffect(() => {
    const canvas = ref.current;
    if (!canvas) {
      return;
    }
    // WDS-CORE-3: the state still has to render, and it renders through the
    // stylesheet — `.core-fluid.off` paints the per-state gradient. Setting a
    // class rather than an inline background is what lets the fallback vary by
    // state without this file knowing any colour.
    //
    // Every GPU exit takes this path, not just a missing context: a driver that
    // rejects the shader hands back non-null objects and then draws nothing, so
    // an unchecked compile is indistinguishable from a working Core until you
    // look at an empty square.
    const fallBackToCSS = () => canvas.classList.add("off");

    const gl = canvas.getContext("webgl", {
      alpha: true,
      // A 25px sphere in the shell's rail must not be the reason a laptop switches
      // to its discrete GPU: that costs battery for the whole session, for a
      // subject whose entire frame is one triangle. The hint is advisory — a
      // machine with one GPU ignores it — which is why it is safe to state
      // unconditionally rather than per call site.
      powerPreference: "low-power",
      // Off, and not an oversight: the whole frame is ONE triangle covering the
      // clip volume, so there is no geometry edge for MSAA to smooth. It would
      // multiply the samples per pixel to antialias nothing.
      antialias: false,
      premultipliedAlpha: true,
    });
    if (!gl) {
      // No WebGL, or jsdom.
      fallBackToCSS();
      return;
    }

    const program = buildProgram(gl);
    if (!program) {
      fallBackToCSS();
      return;
    }
    // biome-ignore lint/correctness/useHookAtTopLevel: gl.useProgram is a WebGL call, not a React hook — the `use` prefix trips the heuristic
    gl.useProgram(program);
    // Only now is there something to draw. Clearing the class earlier meant a
    // rejected shader left the canvas transparent with the CSS state removed.
    canvas.classList.remove("off");

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

    // The buffer follows the displayed size, so a Core that changes size — a
    // breakpoint crossing, a layout that re-sizes its column — gets the
    // resolution it now deserves instead of the one it mounted with. Read here
    // rather than every frame because `clientWidth` forces layout.
    let bufferSize = coreBufferSize(canvas.clientWidth);
    const loop = runLiquidLoop({
      gl,
      canvas,
      uRes,
      uT,
      speed: SPEED[state],
      reduced,
      bufferSize: () => bufferSize,
    });
    // A missing or throwing ResizeObserver (see tryCreateResizeObserver)
    // means render continues at the size the canvas mounted with rather than
    // losing the frame entirely.
    const observer = tryCreateResizeObserver((width) => {
      bufferSize = coreBufferSize(width);
      // A Core whose liquid does not move has already drawn its one frame and
      // parked; resizing it needs one more, or it keeps showing the old buffer
      // stretched to the new box.
      loop.wake();
    });
    observer?.observe(canvas);

    return () => {
      loop.stop();
      observer?.disconnect();
      gl.deleteProgram(program);
      gl.deleteBuffer(buffer);
    };
  }, [reduced, state, contextEpoch]);

  return <canvas ref={ref} className={`core-fluid ${className}`.trim()} />;
}
