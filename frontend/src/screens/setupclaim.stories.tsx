import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { SetupClaimScreen } from "./setupclaim";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

/**
 * The first-run claim (ADR-0105).
 *
 * An installation whose deployment file names no `bootstrap_admin` holds no
 * organization. Its boundary would otherwise render "installation not ready" —
 * true, and a dead end. This screen is what stands there when the installation
 * is instead WAITING to be claimed, and it is the only screen in the product
 * that creates an account without one.
 *
 * The states worth looking at are the ones a screenshot settles rather than an
 * assertion: whether the warning that this account is the installation's ROOT
 * reads as a warning, whether the token field reads as something copied from a
 * server log rather than a password, and whether a refusal is legible next to a
 * form the person has just filled in. The three refusals differ in meaning, not
 * just wording — a wrong token, an installation someone else already claimed,
 * and a field to fix are three different next actions — so each is a story.
 */
const meta: Meta<typeof SetupClaimScreen> = {
  title: "Screens/Setup claim",
  component: SetupClaimScreen,
  parameters: { layout: "fullscreen" },
  decorators: [
    (Story) => (
      <StoryProviders>
        <Story />
      </StoryProviders>
    ),
  ],
  args: { onClaimed: () => {} },
};
export default meta;

type Story = StoryObj<typeof SetupClaimScreen>;

/**
 * fill drives the form to the state a person reaches before pressing submit.
 * Shared by every story below, so the refusals differ only in what the server
 * answers — which is the thing each of them is about.
 */
async function fill(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  const user = userEvent.setup();
  await user.type(
    canvas.getByLabelText(/setup token/i),
    "9f2c-not-a-real-token",
  );
  await user.type(
    canvas.getByLabelText(/organization name/i),
    "Brandt Automotive",
  );
  await user.type(canvas.getByLabelText(/your name/i), "Ilse Brandt");
  await user.type(canvas.getByLabelText(/your email/i), "ilse@brandt.example");
  await user.type(
    canvas.getByLabelText(/choose a password/i),
    "correct horse battery",
  );
  return { canvas, user };
}

/** submitAgainst stubs the claim endpoint with one status and presses submit. */
async function submitAgainst(canvasElement: HTMLElement, status: number) {
  installFetchStub({
    "POST /setup/claim": () => jsonResponse({}, status),
  });
  const { canvas, user } = await fill(canvasElement);
  await user.click(
    canvas.getByRole("button", { name: /create the organization/i }),
  );
}

/** The screen an operator's first visit lands on. */
export const Empty: Story = {};

/**
 * Filled in and ready. The submit button is enabled only once every field is
 * present and the password clears the floor the server applies, so "ready" is a
 * visible state rather than something discovered by pressing it.
 */
export const Ready: Story = {
  play: async ({ canvasElement }) => {
    await fill(canvasElement);
  },
};

/**
 * A password below the floor. The refusal takes the field's `error` slot — the
 * danger tone and an `aria-invalid` outline, where it used to ride the same grey
 * `hint` as the neutral rule it replaced — and the button stays disabled, so the
 * refusal happens here rather than as a 422 after a round trip the person has
 * already waited for.
 */
export const PasswordTooShort: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const user = userEvent.setup();
    await user.type(
      canvas.getByLabelText(/setup token/i),
      "9f2c-not-a-real-token",
    );
    await user.type(
      canvas.getByLabelText(/organization name/i),
      "Brandt Automotive",
    );
    await user.type(canvas.getByLabelText(/your name/i), "Ilse Brandt");
    await user.type(
      canvas.getByLabelText(/your email/i),
      "ilse@brandt.example",
    );
    await user.type(canvas.getByLabelText(/choose a password/i), "short");
  },
};

/** The token is not this installation's — check the server log from first start. */
export const WrongToken: Story = {
  play: async ({ canvasElement }) => {
    await submitAgainst(canvasElement, 401);
  },
};

/**
 * Someone else got there first. This is the state a second operator sees, and
 * it must not read as a typo: the next action is to sign in, not to retype.
 */
export const AlreadyClaimed: Story = {
  play: async ({ canvasElement }) => {
    await submitAgainst(canvasElement, 409);
  },
};
