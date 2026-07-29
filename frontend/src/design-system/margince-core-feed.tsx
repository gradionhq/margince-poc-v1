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
const MOTES = [
  { a: 22, d: 132, t: 4.6, dl: 0, s: 4, o: 0.85 },
  { a: 78, d: 118, t: 5.4, dl: 0.9, s: 3, o: 0.5 },
  { a: 134, d: 140, t: 4.2, dl: 2.1, s: 4, o: 0.75 },
  { a: 171, d: 112, t: 6.1, dl: 1.2, s: 3, o: 0.45 },
  { a: 214, d: 136, t: 4.9, dl: 3.4, s: 4, o: 0.8 },
  { a: 249, d: 124, t: 5.8, dl: 0.4, s: 3, o: 0.6 },
  { a: 292, d: 144, t: 4.4, dl: 2.7, s: 3, o: 0.5 },
  { a: 326, d: 116, t: 6.4, dl: 1.8, s: 4, o: 0.75 },
  { a: 348, d: 138, t: 5.1, dl: 3.9, s: 3, o: 0.55 },
  { a: 57, d: 128, t: 5.6, dl: 2.4, s: 4, o: 0.7 },
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
