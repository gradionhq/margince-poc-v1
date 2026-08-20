import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useId,
  useMemo,
  useState,
} from "react";
import { ConfirmModal } from "../design-system/confirmmodal";
import { useT } from "../i18n";

/**
 * Unsaved edits, and what happens when the reader leaves without saving them.
 *
 * Today the answer is nothing: there is no `beforeunload` anywhere in this
 * frontend and no navigation guard of any kind, so every draft on a settings
 * page — a rewritten signature, a retyped installation name, a voice profile
 * somebody spent ten minutes on — is destroyed silently by a click on the
 * sidebar. Silently is the part that matters. A form that warns you is annoying;
 * a form that discards your work without a word teaches you not to trust it with
 * anything long.
 *
 * Two mechanisms, because there are two ways to leave and only one of them is
 * ours to intercept.
 *
 * A RELOAD or a closed tab is the browser's to ask about, through
 * `beforeunload`, and the browser's own dialog is the only thing allowed to
 * appear there — a page cannot render its own confirm at that point, and every
 * attempt to reads as a page fighting the window.
 *
 * A move WITHIN the app is ours, and it is asked with this product's own
 * `ConfirmModal`. It is scoped to the surface that holds the draft rather than
 * installed over the router, and that scoping is deliberate: a guard wired into
 * `hashchange` also intercepts Back and Forward, where undoing the browser's own
 * history gesture means rewriting history entries underneath the reader. The
 * failure mode of a guard that gets that wrong is broken navigation for
 * everybody, which is far worse than the loss it prevents. So a surface holds
 * its OWN content until the question is answered, the URL is allowed to move,
 * and the browser's gestures are left alone.
 */

type UnsavedRegistry = Readonly<{
  /** Whether anything inside this scope has an edit that has not been written. */
  dirty: boolean;
  claim: (token: string, dirty: boolean) => void;
  release: (token: string) => void;
}>;

const UnsavedContext = createContext<UnsavedRegistry | null>(null);

/**
 * Declare that this component holds an unsaved edit, or no longer does.
 *
 * A hook rather than a prop chain because the draft and the guard are usually
 * several components apart — the card that owns the textarea is inside the tab
 * that is inside the screen that would hold the answer — and threading a boolean
 * up through that is how one card ends up guarded and the next one does not.
 *
 * Safe to call outside a provider: it reports nothing, which is the honest
 * behaviour for a card rendered somewhere no guard is installed (a story, a
 * test, a record page). A hook that threw there would make the provider a
 * requirement of every caller rather than of the surfaces that want guarding.
 */
export function useUnsavedGuard(dirty: boolean): void {
  const registry = useContext(UnsavedContext);
  // A stable identity per mounting, so two instances of the same card — two
  // rows, two panels — are two claims rather than one that the second overwrites.
  const token = useId();

  useEffect(() => {
    registry?.claim(token, dirty);
  }, [registry, token, dirty]);

  // Released on unmount, and this is the one that has to be right: a card that
  // left the tree still holding a claim would guard the surface against a draft
  // that no longer exists, and nothing the reader can do would clear it.
  useEffect(() => () => registry?.release(token), [registry, token]);
}

/**
 * The window's own question, asked only while something is actually unsaved.
 *
 * Registered and unregistered with the flag rather than installed once and
 * consulted, because a listener that is merely present is enough to make some
 * browsers treat the page as unload-blocking, which costs it the back/forward
 * cache. The text is the browser's — every engine has ignored a custom string
 * for a decade — so `preventDefault` is the whole of the API worth using.
 */
function useBeforeUnload(dirty: boolean): void {
  useEffect(() => {
    if (!dirty) {
      return;
    }
    const ask = (event: BeforeUnloadEvent) => event.preventDefault();
    globalThis.addEventListener("beforeunload", ask);
    return () => globalThis.removeEventListener("beforeunload", ask);
  }, [dirty]);
}

/**
 * Holds `children` steady while an unsaved edit is on screen and the address
 * changes underneath it.
 *
 * `address` is whatever the caller counts as "somewhere else" — a settings tab
 * id, a record id. When it changes and something is dirty, the guard keeps
 * rendering the address it was already showing and asks. Discarding shows the
 * new one; keeping sends the reader back to the one they were editing, which is
 * the only outcome that leaves their work where they can still save it.
 *
 * `onKeep` is how the caller puts the address back, because only the caller
 * knows what the address IS — this component holds content, not routes.
 */
export function UnsavedGuard<Address extends string>({
  address,
  onKeep,
  children,
}: Readonly<{
  address: Address;
  onKeep: (address: Address) => void;
  children: (address: Address) => ReactNode;
}>) {
  const t = useT();
  const [claims, setClaims] = useState<ReadonlyMap<string, boolean>>(new Map());
  // The address whose content is on screen. It follows `address` freely until a
  // draft is at stake, and then stops until the question is answered.
  const [shown, setShown] = useState(address);

  const dirty = [...claims.values()].some(Boolean);
  useBeforeUnload(dirty);

  const claim = useCallback((token: string, next: boolean) => {
    setClaims((held) => {
      if (held.get(token) === next) {
        return held;
      }
      const grown = new Map(held);
      grown.set(token, next);
      return grown;
    });
  }, []);

  const release = useCallback((token: string) => {
    setClaims((held) => {
      if (!held.has(token)) {
        return held;
      }
      const shrunk = new Map(held);
      shrunk.delete(token);
      return shrunk;
    });
  }, []);

  const registry = useMemo<UnsavedRegistry>(
    () => ({ dirty, claim, release }),
    [dirty, claim, release],
  );

  // Catching up is not a state change, so it happens during render rather than
  // in an effect: an effect would paint the OLD content for one frame against
  // the NEW page heading the shell has already swapped, which reads as the page
  // half-navigating.
  if (address !== shown && !dirty) {
    setShown(address);
  }

  const asking = address !== shown && dirty;

  return (
    <UnsavedContext.Provider value={registry}>
      {children(shown)}
      <ConfirmModal
        open={asking}
        title={t("unsaved.title")}
        confirmLabel={t("unsaved.discard")}
        confirmVariant="danger"
        // Closing the question is the SAFE answer, so Escape and the backdrop
        // both keep the edit. A dialog whose dismissal destroys work is a
        // dialog that punishes the reader for not reading it.
        onClose={() => onKeep(shown)}
        onConfirm={() => setShown(address)}
      >
        <p className="t-small">{t("unsaved.body")}</p>
      </ConfirmModal>
    </UnsavedContext.Provider>
  );
}
