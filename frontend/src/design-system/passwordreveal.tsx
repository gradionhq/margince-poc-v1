import { Eye, EyeOff } from "lucide-react";
import { type ReactElement, useState } from "react";
import "./passwordreveal.css";

/**
 * A password field's reveal control, and the input type that goes with it.
 *
 * The affordance existed already — on the sign-in and reset screens, inside
 * `auth.tsx`, reachable from nowhere else. Its own docstring there said that
 * EVERY password field on the surface carries one; three of the five in the
 * product did not, including both `autoComplete="new-password"` fields on the
 * change-password card. That is the pair with the stronger claim on it: a
 * mistyped credential on sign-in is refused by the server in one round trip,
 * while a mistyped NEW password simply becomes the password.
 *
 * A hook rather than a component, because the two halves have to stay together:
 * the button's pressed state and the input's `type` are one fact, and a caller
 * given only the button would hold that fact itself and be able to get it
 * wrong. It returns both, and the caller wires them into a `Field` — the button
 * as `trailing`, so it sits inside the focus ring rather than beside it.
 *
 * Copy arrives translated, like everywhere else in this directory: the labels
 * are the caller's words, and the hook only decides which of the two is
 * current.
 */
export function usePasswordReveal(
  labels: Readonly<{ show: string; hide: string }>,
): Readonly<{
  type: "text" | "password";
  shown: boolean;
  trailing: ReactElement;
}> {
  const [shown, setShown] = useState(false);
  const label = shown ? labels.hide : labels.show;
  return {
    type: shown ? "text" : "password",
    shown,
    trailing: (
      // One button with `aria-pressed` rather than two: the name says which way
      // it will go, the state says where it is. `title` as well, because the
      // control is an icon and a sighted pointer user gets no name otherwise.
      <button
        type="button"
        className="field-reveal"
        aria-pressed={shown}
        aria-label={label}
        title={label}
        onClick={() => setShown((current) => !current)}
      >
        {shown ? <EyeOff aria-hidden /> : <Eye aria-hidden />}
      </button>
    ),
  };
}
