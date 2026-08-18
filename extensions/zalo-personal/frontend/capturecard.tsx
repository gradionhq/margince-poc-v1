import { api, QueryStates, throwProblem } from "@margince/frontend/api";
import { useCan, useCanWrite, useT } from "@margince/frontend/app";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  type Fact,
  FactList,
  Field,
  RecordPicker,
  type RecordPickerCandidate,
  SectionHeader,
  Select,
  type SelectOption,
} from "@margince/frontend/design-system";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useState } from "react";
import {
  CONNECTION_OBJECT,
  isHeld,
  STATUS_KEY,
  useConnectionStatus,
} from "./connection";

// THE CONSENT LANE: which of a rep's own Zalo conversations go into the CRM.
//
// Its own module because it is its own argument. The connection card answers "is
// this account linked, and how do I unlink it"; this one answers "what does
// linking it actually take in", which is the question the whole connector exists
// to let a person answer for themselves. They share a screen, a status read and
// the predicate for "is there an account here" — and nothing else.
//
// TWO SHAPES, NOT A LIST OF SWITCHES. The first version of this card drew one
// dropdown per contact, and on the first real account it met — 68 contacts, and
// reps have hundreds — that is a wall of identical controls nobody can find
// anybody in. What a rep actually does is name the handful of people who matter
// either way, so the card asks one question and then takes names.
//
// THE COPY IS HALF THE WORK HERE, and it is written for a salesperson who has
// never heard the word "capture": conversations go INTO the CRM, people are left
// out or chosen, and the one shape that takes everything says out loud what that
// means for a rep's family, friends and doctor. Warned, then allowed — no modal,
// no confirm step, because the shape is a legitimate answer and a rep who means
// it should not have to argue with a dialog.
//
// AND IT NEVER IMPLIES HISTORY. Personal Zalo hands over no past conversations:
// what a rep gets is what arrives after they save, plus whatever Zalo was still
// holding, a depth nobody has measured. A rep who expects last month and finds
// three days was misled by this card, and the bug they file is a real one.

const ROSTER_KEY = ["ext", "zalo-personal", "contacts"];

/**
 * The two ways a member can answer "which conversations go into the CRM?".
 *
 * `everyone_except` takes everything and subtracts a leave-out list.
 * `only_chosen` takes nothing and adds the people they pick. They are two
 * shapes of the same consent and not a switch with a default: the screen shows
 * whichever the server reports, and offers both as complete sentences a rep can
 * read on their own.
 *
 * A Set as well as the union, because the value arrives over the wire: a mode
 * this screen does not recognise must not be drawn as one it does, and guessing
 * the permissive one would tell a rep less than the truth about their own reach.
 */
const CAPTURE_MODES = new Set(["everyone_except", "only_chosen"]);

type CaptureMode = "everyone_except" | "only_chosen";

function isCaptureMode(value: unknown): value is CaptureMode {
  return typeof value === "string" && CAPTURE_MODES.has(value);
}

/**
 * What the roster can say about one person, which is the STORED shape rather
 * than anything a reader sees: `allow` is on the pick-list, `block` is on the
 * leave-out list, `none` is on neither. The screen never shows these three
 * words — it shows one list, and which of them puts somebody on it depends on
 * the mode ({@link listMarker}).
 */
const CONTACT_MODES = new Set(["allow", "block", "none"]);

type ContactMode = "allow" | "block" | "none";

function isContactMode(value: unknown): value is ContactMode {
  return typeof value === "string" && CONTACT_MODES.has(value);
}

/**
 * Which stored verdict means "on the list this mode shows".
 *
 * The two modes read the SAME roster: in `everyone_except` the list on screen is
 * the blocks, in `only_chosen` it is the allows. Each mode therefore keeps its
 * own list, and switching back finds the earlier one intact rather than
 * rewriting it — a rep who tries the other shape has not thrown anything away.
 */
function listMarker(mode: CaptureMode): ContactMode {
  return mode === "everyone_except" ? "block" : "allow";
}

/**
 * One person as the list draws them.
 *
 * `name` is what a reader sees and `displayName` is what Zalo actually said, and
 * they are kept apart because only the second may be SENT: the name falls back
 * to the channel id so a row is never blank, and posting that fallback back to
 * the server would store an id as somebody's name — which is exactly the value
 * the degraded list then reads people by.
 */
type Contact = Readonly<{
  id: string;
  name: string;
  displayName?: string;
  mode: ContactMode;
}>;

/** One person's placement as the save states it. */
type SavedVerdict = Readonly<{
  channel_user_id: string;
  mode: ContactMode;
  display_name?: string;
}>;

/** What a save says: the shape, and only the placements that changed. */
type SavePayload = Readonly<{
  capture_mode: CaptureMode;
  entries?: SavedVerdict[];
}>;

/** How many people the search offers at once. A list to read, not to scroll. */
const PICKER_LIMIT = 8;

/**
 * The member's own contacts, and their verdict on each.
 *
 * DEFAULT DENY is the mechanism, so the list is drawn as three verdicts rather
 * than as checkboxes: `none` is not an absent choice waiting to be tidied up, it
 * is the resting state in which nothing about that contact is captured. `block`
 * sits beside it as a refinement — a member who wants to be sure about one
 * person can say so — and neither of them is what does the work.
 *
 * The roster arrives from the server already merged: whatever Zalo reported,
 * plus every contact this member has already ruled on. So a roster call that
 * failed upstream leaves this card with the stored entries alone, and it still
 * works — the verdicts are editable and savable without Zalo answering at all,
 * which is the state a member must not be locked out of. That is also why a
 * missing `display_name` falls back to the channel id rather than to an empty
 * row: an entry nobody can name is still an entry somebody chose.
 *
 * `roster_available` is how the server SAYS which of those two answers this is,
 * and the screen repeats it rather than inferring: an empty degraded list and an
 * account with no contacts look identical from here and mean opposite things.
 */
function useContactRoster() {
  return useQuery({
    queryKey: ROSTER_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/ext/zalo-personal/contacts",
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
      if (!Array.isArray(data?.contacts)) {
        throw new Error("the contact list carried no `contacts` array");
      }
      // Judged, because the contract makes it the difference between two things
      // a member must not be told interchangeably: "you know nobody" and "we
      // could not ask Zalo". Read as true when the body did not carry it, an
      // empty degraded list would read as an empty contact list, and a member
      // would go looking for people the server never got to name.
      if (typeof data.roster_available !== "boolean") {
        throw new Error("the contact list carried no `roster_available` field");
      }
      return {
        rosterAvailable: data.roster_available,
        contacts: data.contacts.map(readContact),
      };
    },
  });
}

/**
 * One roster entry, validated rather than trusted.
 *
 * A bad entry fails the whole read instead of being dropped, and the asymmetry
 * is deliberate: dropping one would show a member FEWER verdicts than they hold,
 * and the one that vanishes is as likely to be a block as an allow — a screen
 * quietly omitting somebody they shut out is worse than a screen that admits it
 * could not read the list.
 */
function readContact(
  entry: Readonly<{
    channel_user_id: string;
    display_name?: string;
    mode: string;
  }>,
): Contact {
  if (entry.channel_user_id === "") {
    throw new Error("a contact carried no `channel_user_id`");
  }
  if (!isContactMode(entry.mode)) {
    throw new Error("a contact carried no known `mode`");
  }
  return {
    id: entry.channel_user_id,
    name: entry.display_name ?? entry.channel_user_id,
    displayName: entry.display_name,
    mode: entry.mode,
  };
}

/**
 * The chooser: which conversations go into the CRM.
 *
 * It appears only once there is an account to choose FOR. Drawn beside the
 * invitation to scan it would ask somebody to rule on a contact list that does
 * not exist yet, and drawn while the status read is still in flight it would
 * claim an account this screen has not established.
 *
 * ONE SEARCH BOX RATHER THAN A CONTROL PER PERSON. The first version of this
 * card drew a dropdown for every contact, which on a real roster of 68 people —
 * and reps have hundreds — is a wall of identical controls nobody can find
 * anybody in. What a rep actually does is name the handful of people that matter
 * either way, so the card is a shape, a search box, and the people on the list.
 */
export function CaptureCard() {
  const t = useT();
  const canRead = useCan(CONNECTION_OBJECT, "read");
  // The same grant that starts a login: choosing what goes into the CRM is an
  // update to the member's own connection, and the server judges it the same way.
  const canChoose = useCanWrite(CONNECTION_OBJECT, "update");
  // The same query key the connection card reads, so this is the same request
  // and not a second one.
  const status = useConnectionStatus(canRead);
  if (!canRead || !isHeld(status.data?.connection)) {
    return null;
  }
  return (
    <Card>
      <SectionHeader
        title={t("extZaloPersonal.capture.title")}
        sub={t("extZaloPersonal.capture.sub")}
      />
      <CaptureChoice
        canChoose={canChoose}
        savedMode={savedShape(status.data?.connection?.capture_mode)}
        savedCount={status.data?.allowedCount}
      />
    </Card>
  );
}

/**
 * The shape the server saved, or `undefined` for a rep who has not answered.
 *
 * ABSENT IS A STATE, not a missing setting to fill in with a guess: the contract
 * is explicit that a connection with no shape captures nothing, and that there is
 * no default, because defaulting to the permissive one would have this CRM read
 * somebody's whole personal chat life without anybody deciding to. So the screen
 * shows "not chosen yet" and asks, rather than pre-selecting an answer and
 * inviting a rep to save what looks like the state they were already in.
 *
 * An unrecognised value is refused rather than drawn as one of the two: a screen
 * that renders a shape it does not understand tells a rep something about their
 * own reach that it does not know.
 */
function savedShape(value: unknown): CaptureMode | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (!isCaptureMode(value)) {
    throw new Error("the connection carried an unknown capture mode");
  }
  return value;
}

/**
 * The shape, the people, and the save.
 *
 * `savedMode` and the roster are the SERVER's answer; `pickedMode` and `changes`
 * are what the member has done since. Keeping them apart is what lets the save
 * send only what differs, and it is why a lost response is harmless: the re-read
 * arrives, the difference empties, and the screen stops claiming there is
 * anything unsaved without this component ever deciding whether the save it did
 * not hear back from landed.
 */
function CaptureChoice({
  canChoose,
  savedMode,
  savedCount,
}: Readonly<{
  canChoose: boolean;
  savedMode?: CaptureMode;
  savedCount?: number;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const roster = useContactRoster();
  // `null` rather than the server's value copied into state: nothing to
  // re-synchronise when the poll answers, and an untouched screen stays
  // distinguishable from one where the rep picked the shape the server had.
  const [pickedMode, setPickedMode] = useState<CaptureMode | null>(null);
  const [changes, setChanges] = useState<Readonly<Record<string, ContactMode>>>(
    {},
  );

  const save = useMutation({
    // The whole document arrives as a VARIABLE rather than off render state: the
    // press belongs to the render that drew the button, so what it carries
    // cannot be older than the list the member was looking at.
    mutationFn: async (payload: SavePayload) => {
      const { error, response } = await api.PUT(
        "/ext/zalo-personal/allowlist",
        { body: payload },
      );
      if (error || !response.ok) {
        throwProblem(error);
      }
    },
    // onSettled rather than onSuccess, and both keys: the save is what switches
    // capture on, so the connection's own card is stale too — and a request that
    // failed did not necessarily fail to SAVE.
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: ROSTER_KEY });
      await queryClient.invalidateQueries({ queryKey: STATUS_KEY });
    },
  });

  const mode = pickedMode ?? savedMode;
  // No shape, no list: which people matter depends entirely on the answer, so
  // until there is one there is nothing a list of names could mean. `allow` is
  // the inert stand-in for the filter below, whose result nothing renders yet.
  const marker = mode === undefined ? "allow" : listMarker(mode);
  const contacts = roster.data?.contacts ?? [];
  const listed = contacts.filter(
    (contact) => (changes[contact.id] ?? contact.mode) === marker,
  );
  const entries = changedPlacements(contacts, changes);
  // `pickedMode !== null` FIRST: an untouched screen holds null, and comparing
  // that with the server's shape reports every screen as unsaved — which would
  // arm the save before anybody had asked for anything and then never let it
  // settle after one.
  const unsaved =
    entries.length > 0 || (pickedMode !== null && pickedMode !== savedMode);

  // The search reads the roster ALREADY HELD — no request per keystroke, and
  // nothing new to ask for: `GET /contacts` returned the whole roster, so
  // finding somebody in it is a filter and not a round trip. Memoised because
  // RecordPicker treats a new function as a new search space and empties its
  // candidates; an identity that churned on every render would clear the list
  // under somebody mid-search.
  const searchRoster = useCallback(
    async (query: string) => {
      const needle = query.trim().toLowerCase();
      return contacts
        .filter((contact) => (changes[contact.id] ?? contact.mode) !== marker)
        .filter((contact) => contact.name.toLowerCase().includes(needle))
        .slice(0, PICKER_LIMIT)
        .map((contact) => ({ id: contact.id, name: contact.name }));
    },
    [contacts, changes, marker],
  );

  return (
    <>
      <ShapeChoice
        mode={mode}
        canChoose={canChoose && !save.isPending}
        onChoose={(next) => setPickedMode(asCaptureMode(next))}
      />
      {mode === undefined ? (
        <EmptyState>{t("extZaloPersonal.capture.chooseFirst")}</EmptyState>
      ) : (
        <PeopleSection
          mode={mode}
          roster={roster}
          listed={listed}
          searchRoster={searchRoster}
          canChoose={canChoose}
          // Kept apart from the grant: a save in flight makes the controls inert
          // WITHOUT taking them off screen, which is what a rep needs to still
          // be looking at if the save comes back refused.
          busy={save.isPending}
          onPick={(id) =>
            setChanges((current) => ({ ...current, [id]: marker }))
          }
          onTakeOff={(id) =>
            setChanges((current) => ({ ...current, [id]: "none" }))
          }
        />
      )}

      <FactList facts={[nowFact(t, mode, savedCount)]} />
      <p>{t("extZaloPersonal.capture.forward")}</p>
      <p>{t("extZaloPersonal.capture.bothSides")}</p>
      <p>{t("extZaloPersonal.capture.yours")}</p>

      {unsaved ? <p>{t("extZaloPersonal.capture.unsaved")}</p> : null}
      {canChoose && mode !== undefined ? (
        <div className="card-actions">
          <Button
            variant="primary"
            disabled={!unsaved || save.isPending}
            onClick={() =>
              save.mutate(
                // `entries` omitted rather than sent empty when only the shape
                // changed: a save is one statement about what happens next, and
                // an empty list is not one of the things being said.
                entries.length > 0
                  ? { capture_mode: mode, entries }
                  : { capture_mode: mode },
              )
            }
          >
            {t(
              save.isPending
                ? "extZaloPersonal.capture.saving"
                : "extZaloPersonal.capture.save",
            )}
          </Button>
        </div>
      ) : null}
      {/* role="alert": the failure appears after the press that caused it, and a
          member who believes a save landed stops watching for the messages that
          are not arriving. */}
      {save.isError ? (
        <p role="alert" className="co-error">
          {t("extZaloPersonal.capture.saveFailed")}
        </p>
      ) : null}
    </>
  );
}

/**
 * The one question, and the warning that belongs to one of its answers.
 *
 * A Select rather than two radio buttons, and the reason is the surface rather
 * than the design: no radio is published to the extension tier, and a unit
 * inventing one would be a second spelling of a control the core already owns.
 * The two options are written as whole sentences, which is what a radio pair
 * would have given for free and what this has to earn in the copy.
 */
function ShapeChoice({
  mode,
  canChoose,
  onChoose,
}: Readonly<{
  mode?: CaptureMode;
  canChoose: boolean;
  onChoose: (value: string) => void;
}>) {
  const t = useT();
  return (
    <>
      <Field label={t("extZaloPersonal.capture.chooseOne")}>
        {(control) => (
          <Select
            {...control}
            options={modeOptions(t)}
            // "" matches no option, so the control's face is the placeholder: a
            // rep who has not answered is shown that, not one of the answers.
            value={mode ?? ""}
            placeholder={t("extZaloPersonal.capture.shapeUnset")}
            disabled={!canChoose}
            onChange={onChoose}
          />
        )}
      </Field>
      {/* DELIBERATELY not a modal and not a confirm step: the shape is allowed,
          and a rep who means it should not have to argue with a dialog. One
          sentence, at the moment of choosing, saying what actually happens.
          role="status" because it appears in response to that choice — a warning
          a screen reader never announces is a warning nobody was given. */}
      {mode === "everyone_except" ? (
        <p role="status">
          <Badge tone="warn">{t("extZaloPersonal.capture.warnTag")}</Badge>{" "}
          {t("extZaloPersonal.capture.warnEveryone")}
        </p>
      ) : null}
    </>
  );
}

/**
 * The people this shape needs named: one search box, and who is on the list.
 */
function PeopleSection({
  mode,
  roster,
  listed,
  searchRoster,
  canChoose,
  busy,
  onPick,
  onTakeOff,
}: Readonly<{
  mode: CaptureMode;
  roster: ReturnType<typeof useContactRoster>;
  listed: readonly Contact[];
  searchRoster: (query: string) => Promise<RecordPickerCandidate[]>;
  canChoose: boolean;
  busy: boolean;
  onPick: (id: string) => void;
  onTakeOff: (id: string) => void;
}>) {
  const t = useT();
  return (
    <>
      <SectionHeader level={3} title={t(listTitleKey(mode))} />
      <QueryStates query={roster}>
        {/* Zalo did not answer, so the search cannot find anybody new — said
            plainly, because the people already on the list ARE here and
            removable, and that is the property a rep must never lose. */}
        {roster.data?.rosterAvailable === false ? (
          <p>{t("extZaloPersonal.capture.noRoster")}</p>
        ) : null}
        {canChoose ? (
          <RecordPicker
            label={t(pickerLabelKey(mode))}
            searchTargets={searchRoster}
            disabled={busy}
            onPick={(candidate) => onPick(candidate.id)}
          />
        ) : null}
        <PeopleOnList
          people={listed}
          mode={mode}
          canChoose={canChoose && !busy}
          onTakeOff={onTakeOff}
        />
      </QueryStates>
    </>
  );
}

/**
 * Who is on the list, each with a way off it.
 *
 * A plain list plus a button per row, because no removable-token control is
 * published on the extension surface and a unit ships no stylesheet of its own —
 * so this is composed from what is published rather than invented as a second
 * spelling of a chip. The accessible name of each button carries the PERSON:
 * eight buttons all announcing "take off" tell a screen-reader reader nothing
 * about which one they are on.
 */
function PeopleOnList({
  people,
  mode,
  canChoose,
  onTakeOff,
}: Readonly<{
  people: readonly Contact[];
  mode: CaptureMode;
  canChoose: boolean;
  onTakeOff: (id: string) => void;
}>) {
  const t = useT();
  if (people.length === 0) {
    // The two empty lists are opposite claims — everything goes in, or nothing
    // does — so they cannot share a sentence.
    return (
      <EmptyState>
        {t(
          mode === "everyone_except"
            ? "extZaloPersonal.capture.emptyEveryone"
            : "extZaloPersonal.capture.emptyChosen",
        )}
      </EmptyState>
    );
  }
  return (
    <ul>
      {people.map((person) => (
        <li key={person.id}>
          {person.name}{" "}
          {canChoose ? (
            <Button
              small
              aria-label={t("extZaloPersonal.capture.removeOne", {
                name: person.name,
              })}
              onClick={() => onTakeOff(person.id)}
            >
              {t("extZaloPersonal.capture.remove")}
            </Button>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

/**
 * What the save would state: the placements that DIFFER from what the server
 * last reported, and nothing else.
 *
 * An entry the document does not name is left exactly as it was, so sending only
 * the difference is what the contract asks for — and a rep's untouched contacts
 * are not re-decided by a save they made about somebody else.
 */
function changedPlacements(
  contacts: readonly Contact[],
  changes: Readonly<Record<string, ContactMode>>,
): SavedVerdict[] {
  const entries: SavedVerdict[] = [];
  for (const contact of contacts) {
    const placed = changes[contact.id];
    if (placed !== undefined && placed !== contact.mode) {
      entries.push({
        channel_user_id: contact.id,
        mode: placed,
        display_name: contact.displayName,
      });
    }
  }
  return entries;
}

/**
 * The chosen shape, refused rather than coerced.
 *
 * The options come from this file, so a value that is not one of them means the
 * control and the vocabulary have drifted apart — and a silent return would
 * leave the control showing a shape the screen is not in.
 */
function asCaptureMode(value: string): CaptureMode {
  if (!isCaptureMode(value)) {
    throw new Error("the shape picker offered an unknown mode");
  }
  return value;
}

/**
 * The two shapes, each written as a whole thought.
 *
 * Neither reads as a setting name ("all", "custom"): a rep meets this control
 * once, and a two-word label that needs the heading to make sense is a label
 * they have to decode rather than read.
 */
function modeOptions(t: ReturnType<typeof useT>): SelectOption[] {
  return [
    {
      value: "everyone_except",
      label: t("extZaloPersonal.capture.modeEveryone"),
    },
    { value: "only_chosen", label: t("extZaloPersonal.capture.modeChosen") },
  ];
}

function listTitleKey(mode: CaptureMode): `extZaloPersonal.capture.${string}` {
  return mode === "everyone_except"
    ? "extZaloPersonal.capture.listEveryone"
    : "extZaloPersonal.capture.listChosen";
}

function pickerLabelKey(
  mode: CaptureMode,
): `extZaloPersonal.capture.${string}` {
  return mode === "everyone_except"
    ? "extZaloPersonal.capture.pickerEveryone"
    : "extZaloPersonal.capture.pickerChosen";
}

/**
 * What is going into the CRM RIGHT NOW — the saved state, not the draft.
 *
 * The two shapes are not two spellings of one number: `only_chosen` has a count
 * the server keeps, and `everyone_except` has no honest number at all, because
 * "everyone you talk to" is not something either side has counted. Printing a
 * count there would invite a rep to read it as the whole reach of the thing.
 */
function nowFact(
  t: ReturnType<typeof useT>,
  mode: CaptureMode | undefined,
  savedCount?: number,
): Fact {
  const term = t("extZaloPersonal.capture.now");
  if (mode === undefined) {
    return {
      key: "now",
      term,
      value: t("extZaloPersonal.capture.nowNothing"),
    };
  }
  if (mode === "everyone_except") {
    return {
      key: "now",
      term,
      value: t("extZaloPersonal.capture.nowEveryone"),
      note: t("extZaloPersonal.capture.nowExceptNote"),
    };
  }
  return {
    key: "now",
    term,
    // Absent, the count is a fact the server did not report: read as zero it
    // would claim "nothing of yours is saved", which is a claim, and a rep who
    // believes it goes looking for the choices they already made.
    value:
      savedCount === undefined
        ? t("extZaloPersonal.capture.nowUnknown")
        : String(savedCount),
    note: savedCount === 0 ? t("extZaloPersonal.capture.nowNone") : undefined,
  };
}
