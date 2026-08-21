// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/**
 * The Core's shading, as GLSL.
 *
 * Kept in its own file for one reason: it is a foreign language. The engine
 * beside it (`margince-core-engine.ts`) is TypeScript about lifecycle, sizing
 * and easing, and reading it should not mean scrolling past three hundred lines
 * of another grammar.
 *
 * WHAT THE OBJECT IS. Four ribbons on their own shells inside a glass ball, all
 * threaded through one shared focus, which is why they cross at a single hot
 * point. Every ribbon is drawn as a SURFACE, hit analytically where the ray
 * crosses its shell, not marched as a volume: near the silhouette a ray runs
 * almost parallel to the surface, and integrating along it smears every edge.
 * That single choice is the whole difference between crisp ribbons and a cloud.
 *
 * WHAT IS NOT HERE, and was in the study this is ported from: no page
 * background (the Core is a component and has to sit on whatever surface hosts
 * it, so it composites with alpha), and no second palette. The study carried a
 * Siri dress to argue against; the product has one.
 */

/** Fullscreen triangle from the vertex id. No attribute buffers to bind. */
export const CORE_VERT = `#version 300 es
void main(){
  vec2 p = vec2((gl_VertexID << 1) & 2, gl_VertexID & 2);
  gl_Position = vec4(p * 2.0 - 1.0, 0.0, 1.0);
}`;

export const CORE_FRAG = `#version 300 es
precision highp float;
out vec4 outColor;

uniform vec2  iResolution;
uniform vec2  uMouse;      // -1..1, eased. zero at rail size: a 34px ball has
                           // no parallax to give and jitters if asked for one
uniform float uLevel;      // 0..1 activity: energy, motion speed, core heat
uniform float uPhase;      // accumulated host-side, so a speed change bends the
                           // motion instead of teleporting it
uniform float uPulse;      // 0..1 how much the orb breathes
uniform float uTint;       // 0..1 how far the palette collapses to uTintCol
uniform vec3  uTintCol;    // the stopped-state colour: amber, grey or red
uniform float uIngest;     // 0..1 the inward "pulling material in" sweep
uniform float uPaper;      // 0 = the ball glows on a dark surface,
                           // 1 = it goes opaque and dark on light paper

const float R  = 1.0;      // orb radius in world units
const float PI = 3.14159265;
const int   NRIB = 4;      // three reads empty, five reads busy

mat3 rotY(float a){ float s=sin(a),c=cos(a); return mat3(c,0,-s, 0,1,0, s,0,c); }
mat3 rotX(float a){ float s=sin(a),c=cos(a); return mat3(1,0,0, 0,c,s, 0,-s,c); }

/* ray vs sphere centred at the origin */
bool hitSphere(vec3 ro, vec3 rd, float r, out float t0, out float t1){
  float b = dot(ro, rd);
  float c = dot(ro, ro) - r*r;
  float h = b*b - c;
  if(h < 0.0) return false;
  h = sqrt(h);
  t0 = -b - h;
  t1 = -b + h;
  return t1 > 0.0;
}

/* One ribbon, evaluated as a SURFACE rather than a volume.

   A loop is the set of directions sitting a fixed angle rho away from a centre
   direction C: a circle on the sphere. Modulating rho with the azimuth turns
   that circle into a bent band, and modulating the width with the same azimuth
   gives it a paddle that swells at one end and tapers at the other.

   rho is anchored so every loop passes exactly through a shared focus F, which
   is what makes them all cross at one bright point. */
float band(vec3 u, vec3 C, vec3 F, float phase, float width,
           float wob1, float wob2, out float rim){
  rim = 0.0;

  vec3 e1 = F - dot(F, C) * C;
  float e1len = length(e1);
  if(e1len < 1e-4) return 0.0;
  e1 /= e1len;
  vec3 e2 = cross(C, e1);

  float phi  = atan(dot(u, e2), dot(u, e1));      // azimuth around C
  float rho  = acos(clamp(dot(u, C), -1.0, 1.0)); // angle out from C
  float rho0 = acos(clamp(dot(F, C), -1.0, 1.0)); // ... at the focus

  float target = rho0 + wob1 * cos(phi + phase) + wob2 * cos(2.0 * phi - 1.7 * phase);
  float tn = abs(rho - target);

  /* paddle: wide over part of the sweep, gone over the rest */
  float swell = 0.5 + 0.5 * sin(phi + phase * 0.6);
  float sw4 = swell * swell * swell * swell;
  float w = width * (0.18 + 0.82 * sw4);
  float gate = smoothstep(0.02, 0.30, sw4);   // the band simply ends

  /* defined, but not a cutout: the edge takes a while to fall, and the surface
     is brighter down its middle than at its border */
  float fill = 1.0 - smoothstep(w * 0.42, w * 1.14, tn);
  fill *= 0.52 + 0.48 * (1.0 - clamp(tn / max(w, 1e-4), 0.0, 1.0));
  rim = (1.0 - smoothstep(w * 0.94, w * 1.10, tn)) * smoothstep(w * 0.60, w * 0.92, tn);
  float bleed = (1.0 - smoothstep(w * 0.9, w * 2.6, tn)) * 0.16;

  return (fill + bleed) * gate;
}

/* 4x4 ordered dither. A per-pixel hash also debands, but it is white noise: on
   thin high-contrast structures that reads as grain. An ordered offset spreads
   the same error over a fixed tile instead. */
float bayer(vec2 f){
  const float B[16] = float[16](
     0.0,  8.0,  2.0, 10.0,
    12.0,  4.0, 14.0,  6.0,
     3.0, 11.0,  1.0,  9.0,
    15.0,  7.0, 13.0,  5.0);
  int x = int(mod(f.x, 4.0));
  int y = int(mod(f.y, 4.0));
  return (B[y * 4 + x] + 0.5) / 16.0;
}

/* one drifting endpoint: a base direction nudged by three detuned sines */
vec3 drift(vec3 base, vec3 f, vec3 ph, float t, float amp){
  return normalize(base + amp * vec3(
    sin(t*f.x + ph.x),
    sin(t*f.y + ph.y),
    sin(t*f.z + ph.z)));
}

void main(){
  vec2 frag = gl_FragCoord.xy;
  vec2 uv = (frag - 0.5 * iResolution) / iResolution.y;

  /* camera: fixed. the ball never moves in frame, the pointer only turns what
     is inside it */
  vec3 ro = vec3(0.0, 0.0, 3.2);
  vec3 rd = normalize(vec3(uv * 1.05, -1.0));

  float ta = uPhase;

  mat3 world = rotY(ta * 0.22 + uMouse.x * 0.45)
             * rotX(-uMouse.y * 0.35 + sin(ta * 0.17) * 0.12);
  mat3 inv = transpose(world);

  /* ---------- ribbons ---------- */
  vec3 CEN[5], COL[5];
  float SHELL[5], WIDTH[5], WOB1[5], WOB2[5], PHASE[5], GAIN[5];
  float FADE[5];   // ingest: how present each ribbon is on its way in

  /* emerald, mint, amber as the single warm note, mint again, pale mint */
  vec3 BASE[5];
  BASE[0] = vec3(0.024, 0.404, 0.259);
  BASE[1] = vec3(0.204, 0.827, 0.600);
  BASE[2] = vec3(0.941, 0.663, 0.231);
  BASE[3] = vec3(0.204, 0.827, 0.600);
  BASE[4] = vec3(0.298, 0.804, 0.643);

  SHELL[0]=0.78; WIDTH[0]=0.310; WOB1[0]= 0.26; WOB2[0]= 0.11; GAIN[0]=3.3;
  SHELL[1]=0.90; WIDTH[1]=0.250; WOB1[1]=-0.31; WOB2[1]= 0.08; GAIN[1]=2.7;
  SHELL[2]=0.66; WIDTH[2]=0.350; WOB1[2]= 0.34; WOB2[2]=-0.13; GAIN[2]=2.9;
  SHELL[3]=0.92; WIDTH[3]=0.215; WOB1[3]=-0.22; WOB2[3]= 0.09; GAIN[3]=3.2;
  SHELL[4]=0.84; WIDTH[4]=0.180; WOB1[4]= 0.29; WOB2[4]=-0.10; GAIN[4]=1.8;

  /* the shared focus every loop is threaded through. it drifts, so the crossing
     wanders around the middle instead of being pinned to it */
  vec3 FOCUS = normalize(vec3(0.10 * sin(ta * 0.31), 0.12 * sin(ta * 0.23 + 1.3), 0.98));

  /* one slow breath, and a second faster one that only shows at high pulse */
  float breath = sin(ta * 1.7) * 0.6 + sin(ta * 3.1 + 1.2) * 0.4;
  float pulse  = 1.0 + 0.075 * uPulse * breath;

  for(int i = 0; i < NRIB; i++){
    COL[i] = mix(BASE[i], uTintCol, uTint) * GAIN[i];

    float fi = float(i);
    float phi = (2.0 * PI / float(NRIB)) * fi;      // evenly spaced
    vec3 f  = vec3(0.19, 0.23, 0.29) + 0.031 * fi;
    vec3 ph = vec3(1.0, 2.3, 4.1) * (fi + 1.0);
    /* the loop's radius IS the angle from its centre to the focus, so the
       centre has to sit a controlled angle away or the loop swallows the ball */
    float rho = 0.98 + 0.14 * fi;
    vec3 base = normalize(vec3(cos(phi) * tan(rho), sin(phi) * tan(rho), 1.0));
    CEN[i] = drift(base, f, ph, ta, 0.09);
    PHASE[i] = ta * (0.55 + 0.13 * fi) + 1.9 * fi;

    /* Ingest reads as a whirlpool: each ribbon migrates from the glass down to
       the core over its own cycle, spinning faster as it closes in, dissolving
       when it arrives while a fresh one grows out at the rim. The cycles are
       staggered, so there is no moment where the figure restarts. */
    FADE[i] = 1.0;
    if(uIngest > 0.001){
      /* The cycle a ribbon takes to fall to the centre. Fast enough that a
         reader sees several arrivals in the time a page takes to settle, which is
         what makes intake read as intake rather than as a slow swirl. */
      float tj = fract(uPhase * 0.30 + fi / float(NRIB));
      SHELL[i] = mix(SHELL[i], mix(0.99, 0.10, tj), uIngest);
      /* fast in, slow out: a new ribbon is there almost as soon as it appears
         at the rim, and only thins once it is being consumed */
      float grow = smoothstep(0.0, 0.10, tj);
      float eat  = 1.0 - smoothstep(0.72, 1.0, tj);
      FADE[i]  = mix(1.0, grow * eat, uIngest);
      PHASE[i] += uIngest * tj * tj * 4.5;          // spin up toward the centre
      WIDTH[i] *= mix(1.0, 0.55 + 0.45 * (1.0 - tj), uIngest);
    }
  }

  /* where the crossing point lands in the frame */
  vec3 fwv = world * (FOCUS * 0.80 * pulse);
  vec2 focusUV = fwv.xy / ((3.2 - fwv.z) * 1.05);

  /* the silhouette is a known circle in screen space. R/3.2/1.05 is the
     small-angle guess and lands ~5% inside the real edge; sin(theta) = R/|ro|
     and the ray direction is (uv * 1.05, -1), so the boundary is
     tan(theta) / 1.05. */
  float bgr = length(uv);
  float sinT = R / 3.2;
  float sr = (sinT / sqrt(1.0 - sinT * sinT)) / 1.05;

  vec3 col = vec3(0.0);
  float alpha = 0.0;

  float t0, t1;
  if(hitSphere(ro, rd, R, t0, t1)){
    vec3 acc = vec3(0.0);

    for(int j = 0; j < NRIB; j++){
      float a0, a1;
      if(!hitSphere(ro, rd, SHELL[j] * pulse, a0, a1)) continue;

      for(int h = 0; h < 2; h++){
        float th = (h == 0) ? a1 : a0;          // far crossing first
        if(th <= 0.0) continue;

        vec3 p = inv * (ro + rd * th);
        vec3 u = normalize(p);

        float rim;
        float v = band(u, CEN[j], FOCUS, PHASE[j], WIDTH[j] * (1.0 + 0.45 * uLevel),
                       WOB1[j], WOB2[j], rim);
        if(v <= 0.0 && rim <= 0.0) continue;

        /* the far side of the shell shows through the near side, dimmer */
        float depth = (h == 0) ? 0.55 : 1.0;

        /* where the ray grazes the shell, a band is seen edge-on and collapses
           to a hairline. fade those out instead of drawing wire */
        float graze = abs(dot(normalize(p), rd));
        depth *= smoothstep(0.06, 0.40, graze);
        /* and the glass darkens whatever sits behind its silhouette */
        float sil = 1.0 - 0.45 * smoothstep(0.55, 1.0, length(p));

        acc += COL[j] * v * depth * sil * FADE[j];
        if(h == 1) acc += mix(COL[j], vec3(1.0), 0.72) * rim * 0.22 * depth * sil * FADE[j];
      }
    }

    /* the crossing: every loop is threaded through FOCUS, so the hot core is
       that point projected into the frame. Material arriving is material
       consumed, so the glow answers to how far each ribbon has fallen, summed:
       the delivery fades in and out instead of blinking. */
    if(fwv.z > -0.2){
      float cd = length(uv - focusUV);
      float arrive = 0.0;
      for(int k = 0; k < NRIB; k++){
        float tk = fract(uPhase * 0.30 + float(k) / float(NRIB));
        arrive += sin(PI * tk) * pow(tk, 3.0);
      }
      float heat = 1.0 + 1.4 * uLevel + 1.5 * uIngest * arrive;
      /* tinted, not white, and small: a big white core swallows the ribbons */
      acc += vec3(0.72, 0.98, 0.88)
           * (exp(-pow(cd / 0.022, 2.0)) * 0.65 + exp(-pow(cd / 0.080, 2.0)) * 0.16) * heat;
    }

    float expo = 0.95 + 0.40 * uLevel;
    vec3 body = vec3(1.0) - exp(-acc * expo);

    /* the tonemap pulls everything toward white; push the hue back */
    body = mix(vec3(dot(body, vec3(0.299, 0.587, 0.114))), body, 1.28);

    /* ---------- glass shell ---------- */
    vec3 n = normalize(ro + rd * t0);
    float ndv = clamp(dot(n, -rd), 0.0, 1.0);

    /* three fresnel exponents, one per channel: a chromatic fringe on the rim
       for free, no second pass */
    vec3 fres = vec3(pow(1.0 - ndv, 3.05),
                     pow(1.0 - ndv, 3.30),
                     pow(1.0 - ndv, 3.62));

    float edge = smoothstep(0.55, 1.0, 1.0 - ndv);
    body *= 1.0 - 0.42 * edge;
    body += mix(vec3(0.24, 0.52, 0.44), uTintCol * 0.6, uTint) * fres * 0.16;

    /* a distinct hairline ring all the way round the ball */
    float lip = smoothstep(0.988, 1.0, 1.0 - ndv);
    body += vec3(0.62, 0.98, 0.86) * lip * 0.08;

    /* one broad tinted sheen. never white: a white highlight on a coloured body
       reads as plastic */
    vec3 L = normalize(vec3(-0.55, 0.72, 0.62) + vec3(uMouse * 0.35, 0.0));
    vec3 H = normalize(L - rd);
    body += vec3(0.74, 1.00, 0.90) * pow(max(dot(n, H), 0.0), 26.0) * 0.18;

    /* Everything above is emissive, so on a dark surface the ball is ADDED to
       whatever hosts it and its coverage is its own brightness. On paper the
       object is not translucent at all: it goes opaque and dark, and the
       ribbons glow inside it. */
    float cov = clamp(max(body.r, max(body.g, body.b)) * 1.15
                    + lip * 0.9 + max(fres.r, fres.b) * 0.30, 0.0, 1.0);

    vec2 gdir = normalize(n.xy + vec2(1e-5));
    float grad = 0.5 + 0.5 * dot(gdir, normalize(vec2(-0.7, 0.7)));   // TL to BR

    vec3 ball = mix(vec3(0.030, 0.085, 0.075), vec3(0.055, 0.145, 0.115), grad);
    ball = mix(ball, uTintCol * vec3(0.30, 0.13, 0.09), uTint * 0.85);
    /* a sphere still has to turn away from the light, or it reads as a disc */
    ball *= 0.68 + 0.42 * ndv;
    ball *= 1.0 - 0.35 * edge;

    /* the silhouette, antialiased against the one pixel it lands on */
    float px = 1.5 / iResolution.y;
    float solid = 1.0 - smoothstep(sr - px, sr + px, bgr);

    col   = mix(body, ball + body, uPaper);
    alpha = mix(cov, solid, uPaper);
  }

  /* ---------- rim stroke and outer glow, analytic ---------- */
  /* The rim is not one colour: the hue walks around the circumference, sampled
     from the same four ribbon colours by angle, with the ball's own rotation
     carried into the lookup. */
  float aang = (atan(uv.y, uv.x) + PI) / (2.0 * PI);
  float k  = fract(aang + ta * 0.035) * float(NRIB);
  int   k0 = int(floor(k));
  vec3  rimCol = normalize(mix(COL[k0], COL[int(mod(float(k0) + 1.0, float(NRIB)))], fract(k)) + 1e-4);
  rimCol = mix(rimCol, normalize(uTintCol + 1e-4), uTint);

  /* the stroke sits just OUTSIDE the silhouette, so it outlines the ball
     instead of reading as an inner bevel on it */
  float ring = exp(-pow((bgr - (sr + 0.0016)) / 0.0060, 2.0));
  float ringA = clamp(ring * (0.55 + 0.30 * uLevel), 0.0, 1.0);
  col   = mix(col, rimCol, ringA);
  alpha = max(alpha, ringA);

  /* and the light it throws outward. On paper the same falloff is the shade the
     object casts, so it darkens instead of adding. */
  float outer = max(bgr - sr, 0.0);
  vec3 glowTint = mix(rimCol, vec3(0.10, 0.55, 0.42), 0.45);
  /* Tight and quiet: this is the light the ball throws, and a wide bloom on a
     surface that packs the Core next to other things is a smudge on them rather
     than an atmosphere around it. */
  float glowA = (exp(-outer * 44.0) * 0.10 + exp(-outer * 14.0) * 0.018)
              * (0.60 + 0.40 * uLevel);
  float lit = 1.0 - uPaper;
  col   = mix(col, mix(glowTint, vec3(0.02, 0.06, 0.05), uPaper), (1.0 - alpha) * glowA);
  alpha = max(alpha, glowA * mix(1.0, 0.35, uPaper));
  col  *= 1.0 - 0.10 * lit * smoothstep(0.5, 1.5, bgr);

  /* dither on output: 8-bit banding is very visible on this palette */
  col += (bayer(frag + 2.0) - 0.5) / 255.0;

  /* premultiplied, which is what the canvas compositor expects */
  vec3 srgb = pow(max(col, 0.0), vec3(1.0/2.2));
  outColor = vec4(srgb * alpha, alpha);
}`;
