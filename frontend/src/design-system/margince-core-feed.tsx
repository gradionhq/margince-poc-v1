/**
 * Context arriving at the Core: motes spiralling in and dissolving at the glass.
 *
 * This is what the Core does when it is working — the halo answers as the light
 * lands, so the orb reads as receiving rather than merely glowing. Nothing
 * travels outward, which is the honest shape: the AI is fed by what it reads.
 *
 * Every mote carries its own path as custom properties — angle, distance,
 * duration, delay, size, opacity, and where it ends. Hard-coded rather than
 * random so the same demo looks the same every time it is shown, which matters
 * when two people compare notes afterwards.
 *
 * Internal to the Core (WDS-CORE-1): callers get one component, not three.
 */
/*
 * The artifact's HERO mote table, at the hero's own distances.
 *
 * The port shipped the onboarding gate's set by mistake: ten motes travelling
 * 112 to 144px, against the hero's twelve at 158 to 204px. Inside a 230px stage
 * that difference is the whole effect. At the gate's distances the motes are born
 * at or inside the halo's edge, so they appear from nowhere in the middle of the
 * glow; at the hero's they drift in from well outside the lit area, which is what
 * makes the Core read as RECEIVING rather than as merely sparkling.
 */
const MOTES = [
  { a: 18, d: 196, t: 4.6, dl: 0, s: 5, o: 0.9 },
  { a: 74, d: 168, t: 5.4, dl: 0.9, s: 3, o: 0.55 },
  { a: 129, d: 190, t: 4.2, dl: 2.1, s: 4, o: 0.8 },
  { a: 163, d: 158, t: 6.1, dl: 1.2, s: 3, o: 0.5 },
  { a: 207, d: 188, t: 4.9, dl: 3.4, s: 5, o: 0.85 },
  { a: 242, d: 176, t: 5.8, dl: 0.4, s: 4, o: 0.7 },
  { a: 286, d: 194, t: 4.4, dl: 2.7, s: 3, o: 0.55 },
  { a: 318, d: 164, t: 6.4, dl: 1.8, s: 5, o: 0.8 },
  { a: 341, d: 204, t: 5.1, dl: 3.9, s: 4, o: 0.75 },
  { a: 52, d: 198, t: 5.6, dl: 2.4, s: 3, o: 0.5 },
  { a: 98, d: 182, t: 4.8, dl: 4.3, s: 4, o: 0.7 },
  { a: 265, d: 172, t: 6.7, dl: 0.6, s: 3, o: 0.45 },
];

export function CoreFeed({ endAt = 48 }: { endAt?: number }) {
  return (
    <div className="core-feed">
      {MOTES.map((mote) => (
        <i
          key={`${mote.a}-${mote.dl}`}
          style={
            {
              "--a": `${mote.a}deg`,
              "--d": `${mote.d}px`,
              "--t": `${mote.t}s`,
              "--dl": `${mote.dl}s`,
              "--s": `${mote.s}px`,
              "--o": mote.o,
              // Where the mote dissolves. Smaller than the orb's radius, so it
              // vanishes inside the glass rather than on its edge.
              "--e": `${endAt}px`,
            } as React.CSSProperties
          }
        />
      ))}
    </div>
  );
}
